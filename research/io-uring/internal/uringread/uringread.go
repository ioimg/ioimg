// Package uringread reads files through io_uring (proof-of-concept harness).
//
// Proof-of-concept only: one ring per worker goroutine, openat + read + close
// submitted sequentially (no linked-file chaining), so the syscall count drops
// from os.ReadFile's openat+read+close+stat to three io_uring_enter calls.
package uringread

import (
	"os"
	"runtime"
	"sync"

	"github.com/miretskiy/dio/v2/ringo"
)

type req struct {
	path string
	out  chan result
}

type result struct {
	data []byte
	err  error
}

var (
	once  sync.Once
	queue chan req
	dead  bool
)

func start() {
	n := runtime.GOMAXPROCS(0)
	queue = make(chan req, 256)
	for i := 0; i < n; i++ {
		go worker()
	}
}

func worker() {
	runtime.LockOSThread()
	ring, err := ringo.New(ringo.WithDepth(8))
	if err != nil {
		dead = true
		return
	}
	buf := make([]byte, 512<<10)
	for r := range queue {
		r.out <- do(ring, buf, r.path)
	}
}

func do(ring *ringo.Ring, buf []byte, path string) result {
	h, err := ring.Push(ringo.OpenAt(ringo.AtCWD(), path, os.O_RDONLY, 0))
	if err != nil {
		return result{err: err}
	}
	if _, err := ring.SubmitAndWait(1); err != nil {
		return result{err: err}
	}
	fd := -1
	for c := range ring.Reap() {
		if c.Handle == h {
			if c.Err != nil {
				return result{err: c.Err}
			}
			fd = int(c.Result)
		}
	}
	if fd < 0 {
		return result{err: os.ErrNotExist}
	}
	h, err = ring.Push(ringo.Read(ringo.BorrowedFD(fd), buf, 0))
	if err != nil {
		return result{err: err}
	}
	if _, err := ring.SubmitAndWait(1); err != nil {
		return result{err: err}
	}
	n := -1
	for c := range ring.Reap() {
		if c.Handle == h {
			if c.Err != nil {
				return result{err: c.Err}
			}
			n = int(c.Result)
		}
	}
	if _, err := ring.Push(ringo.CloseFD(fd)); err != nil {
		return result{err: err}
	}
	if _, err := ring.SubmitAndWait(1); err != nil {
		return result{err: err}
	}
	for range ring.Reap() {
	}
	if n < 0 {
		return result{err: os.ErrInvalid}
	}
	out := make([]byte, n)
	copy(out, buf[:n])
	return result{data: out}
}

// ReadFile reads path via io_uring. It falls back to os.ReadFile when the ring
// cannot be created.
func ReadFile(path string) ([]byte, error) {
	once.Do(start)
	if dead {
		return os.ReadFile(path)
	}
	out := make(chan result, 1)
	queue <- req{path: path, out: out}
	r := <-out
	return r.data, r.err
}
