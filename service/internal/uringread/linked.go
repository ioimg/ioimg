// Linked-file variant: openat → read → close submitted as ONE linked sequence,
// so a cache read costs a single io_uring_enter. Uses registered (fixed) file
// slots and one registered buffer per worker, so the kernel never has to
// resolve a path handle or a buffer address per op.
package uringread

import (
	"os"
	"runtime"
	"sync"

	"github.com/miretskiy/dio/v2/ringo"
)

type lreq struct {
	path string
	out  chan lres
}

type lres struct {
	data []byte
	err  error
}

var (
	linkedOnce sync.Once
	linkedQ    chan lreq
	linkedDead bool
)

func startLinked() {
	n := runtime.GOMAXPROCS(0)
	linkedQ = make(chan lreq, 256)
	for i := 0; i < n; i++ {
		go linkedWorker()
	}
}

func linkedWorker() {
	runtime.LockOSThread()
	ring, err := ringo.New(ringo.WithDepth(8))
	if err != nil {
		linkedDead = true
		return
	}
	slots, err := ring.RegisterSparseFiles(4)
	if err != nil {
		linkedDead = true
		return
	}
	slot, err := slots.File(0)
	if err != nil {
		linkedDead = true
		return
	}
	buf := make([]byte, 512<<10)
	buffers, err := ring.RegisterBuffers(buf)
	if err != nil {
		linkedDead = true
		return
	}
	fb, err := buffers.Buffer(0)
	if err != nil {
		linkedDead = true
		return
	}
	for r := range linkedQ {
		r.out <- linkedDo(ring, slot, fb, buf, r.path)
	}
}

func linkedDo(ring *ringo.Ring, slot ringo.FixedFile, fb ringo.FixedBuffer, buf []byte, path string) lres {
	// openat (into fixed slot) --hard link--> readv (from fixed slot, fixed
	// buffer) --hard link--> close (fixed slot). One submission, one enter.
	open := ringo.OpenAtDirect(ringo.AtCWD(), path, os.O_RDONLY, 0, slot)
	read := ringo.ReadFixed(ringo.FixedFD(slot), fb, 0)
	closeOp := ringo.CloseDirect(slot)

	handles, err := ring.PushLinked(open,
		ringo.Then(ringo.LinkHard, read),
		ringo.Then(ringo.LinkHard, closeOp),
	)
	if err != nil {
		return lres{err: err}
	}
	if _, err := ring.SubmitAndWait(len(handles)); err != nil {
		return lres{err: err}
	}
	n := -1
	var firstErr error
	seen := 0
	// Wait for every completion (open, read, close) before the slot is reused.
	for seen < len(handles) {
		for c := range ring.Reap() {
			seen++
			if c.Handle == handles[1] {
				if c.Err != nil && firstErr == nil {
					firstErr = c.Err
				} else if c.Err == nil {
					n = int(c.Result)
				}
			}
		}
	}
	if firstErr != nil {
		return lres{err: firstErr}
	}
	if n < 0 {
		return lres{err: os.ErrInvalid}
	}
	out := make([]byte, n)
	copy(out, buf[:n])
	return lres{data: out}
}

// ReadFileLinked reads path with one linked io_uring submission.
func ReadFileLinked(path string) ([]byte, error) {
	linkedOnce.Do(startLinked)
	if linkedDead {
		return os.ReadFile(path)
	}
	out := make(chan lres, 1)
	linkedQ <- lreq{path: path, out: out}
	r := <-out
	return r.data, r.err
}
