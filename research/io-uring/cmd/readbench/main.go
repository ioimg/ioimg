// Micro-benchmark: cache-file read only, no HTTP.
package main

import (
	"flag"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ioimg/ioimg/research/io-uring/internal/uringread"
)

func main() {
	path := flag.String("f", "", "file to read")
	conc := flag.Int("c", 8, "goroutines")
	dur := flag.Duration("d", 5*time.Second, "duration")
	mode := flag.String("mode", "os", "os|uring|linked")
	flag.Parse()

	var read func(string) ([]byte, error)
	switch *mode {
	case "os":
		read = os.ReadFile
	case "uring":
		read = uringread.ReadFile
	case "linked":
		read = uringread.ReadFileLinked
	}
	var n, bad int64
	var stop atomic.Bool
	var wg sync.WaitGroup
	for i := 0; i < *conc; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for !stop.Load() {
				if _, err := read(*path); err != nil {
					atomic.AddInt64(&bad, 1)
				}
				atomic.AddInt64(&n, 1)
			}
		}()
	}
	time.Sleep(*dur)
	stop.Store(true)
	wg.Wait()
	el := dur.Seconds()
	fmt.Printf("mode=%-6s c=%d ops/s=%.0f ns/op=%.2f bad=%d\n", *mode, *conc, float64(n)/el, float64(el)*1e9/float64(n), bad)
}
