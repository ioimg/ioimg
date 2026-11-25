// Batch cache-warming benchmark: N files read per round through
//   1) sequential os.ReadFile
//   2) 16 goroutines os.ReadFile
//   3) io_uring, one linked openat->read->close chain per file, ONE enter
//      per batch, fixed files + fixed buffers (buffered, page cache allowed)
//   4) same as (3) but O_DIRECT (device read, page cache bypassed)
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"unsafe"
	"time"

	"github.com/miretskiy/dio/v2/ringo"
	"golang.org/x/sys/unix"
)

const fileSize = 16384 // multiple of 4096 so O_DIRECT is valid

func main() {
	dir := flag.String("dir", "/home/mrrizkin/.cache/bbdata", "dataset dir")
	files := flag.Int("n", 1024, "number of files")
	batch := flag.Int("batch", 64, "files per batch")
	rounds := flag.Int("rounds", 20, "rounds per mode")
	mode := flag.String("mode", "os-seq", "os-seq|os-par|uring|uring-direct")
	flag.Parse()

	paths := make([]string, *files)
	for i := 0; i < *files; i++ {
		paths[i] = filepath.Join(*dir, fmt.Sprintf("f%04d.bin", i))
	}

	// Cold-ish: evict these files from the page cache without root.
	if os.Getenv("COLD") != "" {
		for _, p := range paths {
			f, err := os.Open(p)
			if err != nil {
				continue
			}
			_ = unix.Fadvise(int(f.Fd()), 0, 0, unix.FADV_DONTNEED)
			f.Close()
		}
	}

	var bytesRead int64
	t0 := time.Now()
	switch *mode {
	case "os-seq":
		for r := 0; r < *rounds; r++ {
			for _, p := range paths {
				b, err := os.ReadFile(p)
				if err != nil {
					panic(err)
				}
				bytesRead += int64(len(b))
			}
		}
	case "os-par":
		var wg sync.WaitGroup
		for r := 0; r < *rounds; r++ {
			sem := make(chan struct{}, 16)
			for _, p := range paths {
				wg.Add(1)
				sem <- struct{}{}
				go func(p string) {
					defer func() { <-sem; wg.Done() }()
					b, err := os.ReadFile(p)
					if err != nil {
						return
					}
					bytesRead += int64(len(b))
				}(p)
			}
			wg.Wait()
		}
	case "os-direct", "os-par-direct":
		raw := make([]byte, fileSize+4096)
		off := (4096 - int(uintptr(unsafe.Pointer(&raw[0]))%4096)) % 4096
		buf := raw[off : off+fileSize]
		one := func(p string) int64 {
			fd, err := unix.Open(p, unix.O_RDONLY|unix.O_DIRECT, 0)
			if err != nil {
				return 0
			}
			defer unix.Close(fd)
			n, err := unix.Read(fd, buf)
			if err != nil || n < 0 {
				return 0
			}
			return int64(n)
		}
		if *mode == "os-direct" {
			for r := 0; r < *rounds; r++ {
				for _, p := range paths {
					bytesRead += one(p)
				}
			}
		} else {
			var wg sync.WaitGroup
			for r := 0; r < *rounds; r++ {
				sem := make(chan struct{}, 16)
				for _, p := range paths {
					wg.Add(1)
					sem <- struct{}{}
					go func(p string) {
						defer func() { <-sem; wg.Done() }()
						buf2 := make([]byte, fileSize+4096)
						o := (4096 - int(uintptr(unsafe.Pointer(&buf2[0]))%4096)) % 4096
						fd, err := unix.Open(p, unix.O_RDONLY|unix.O_DIRECT, 0)
						if err != nil {
							return
						}
						n, err := unix.Read(fd, buf2[o:o+fileSize])
						unix.Close(fd)
						if err == nil && n > 0 {
							bytesRead += int64(n)
						}
					}(p)
				}
				wg.Wait()
			}
		}
	case "uring", "uring-direct":
		flags := os.O_RDONLY
		if *mode == "uring-direct" {
			flags |= unix.O_DIRECT
		}
		w := newBatcher(*batch, flags)
		defer w.close()
		for r := 0; r < *rounds; r++ {
			for i := 0; i < len(paths); i += *batch {
				end := i + *batch
				if end > len(paths) {
					end = len(paths)
				}
				n, err := w.readAll(paths[i:end])
				if err != nil {
					panic(err)
				}
				bytesRead += n
			}
		}
	default:
		panic("bad mode")
	}
	el := time.Since(t0).Seconds()
	fmt.Printf("mode=%-12s files=%d batch=%d rounds=%d files/s=%.0f MB/s=%.1f\n",
		*mode, *files, *batch, *rounds, float64(*files)*float64(*rounds)/el, float64(bytesRead)/el/1e6)
}

type batcher struct {
	ring    *ringo.Ring
	slots   *ringo.FixedFiles
	reg     *ringo.FixedBuffers
	slab    []byte
	bufs    [][]byte
	handles [][3]ringo.Handle
	size    int
	flags   int
	errs    int
}

func newBatcher(n int, flags int) *batcher {
	ring, err := ringo.New(ringo.WithDepth(uint32(n*3 + 8)))
	if err != nil {
		panic(err)
	}
	slots, err := ring.RegisterSparseFiles(uint32(n))
	if err != nil {
		panic(err)
	}
	raw := make([]byte, n*fileSize+4096)
	off := (4096 - int(uintptr(unsafe.Pointer(&raw[0]))%4096)) % 4096
	slab := raw[off : off+n*fileSize]
	bufs := make([][]byte, n)
	for i := range bufs {
		bufs[i] = slab[i*fileSize : (i+1)*fileSize]
	}
	reg, err := ring.RegisterBuffers(bufs...)
	if err != nil {
		panic(err)
	}
	return &batcher{ring: ring, slots: slots, reg: reg, slab: slab, bufs: bufs, size: n, flags: flags, handles: make([][3]ringo.Handle, n)}
}

// readAll submits one linked openat->read->close chain per path and waits for
// every completion with a single io_uring_enter.
func (b *batcher) readAll(paths []string) (int64, error) {
	if len(paths) > b.size {
		return 0, fmt.Errorf("batch larger than ring capacity")
	}
	total := 0
	for i, p := range paths {
		slot, err := b.slots.File(uint32(i))
		if err != nil {
			return 0, err
		}
		fb, err := b.reg.Buffer(uint32(i))
		if err != nil {
			return 0, err
		}
		hs, err := b.ring.PushLinked(
			ringo.OpenAtDirect(ringo.AtCWD(), p, b.flags, 0, slot),
			ringo.Then(ringo.LinkHard, ringo.ReadFixed(ringo.FixedFD(slot), fb, 0)),
			ringo.Then(ringo.LinkHard, ringo.CloseDirect(slot)),
		)
		if err != nil {
			return 0, err
		}
		b.handles[i] = [3]ringo.Handle{hs[0], hs[1], hs[2]}
		total += len(hs)
	}
	if _, err := b.ring.SubmitAndWait(total); err != nil {
		return 0, err
	}
	read := map[ringo.Handle]int{}
	for i := range paths {
		read[b.handles[i][1]] = i
	}
	var n int64
	seen := 0
	for seen < total {
		for c := range b.ring.Reap() {
			seen++
			if _, ok := read[c.Handle]; ok {
				if c.Err != nil {
					if b.errs < 3 {
						fmt.Fprintf(os.Stderr, "read error: %v\n", c.Err)
					}
					b.errs++
					continue
				}
				n += int64(c.Result)
			}
		}
	}
	return n, nil
}

func (b *batcher) close() { b.ring.Close() }
