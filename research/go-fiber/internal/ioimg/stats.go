package ioimg

import (
	"fmt"
	"strings"
	"sync/atomic"
)

type Stats struct {
	Requests    atomic.Uint64
	Hits        atomic.Uint64
	Misses      atomic.Uint64
	Errors      atomic.Uint64
	BytesOut    atomic.Uint64
	TransformNs atomic.Uint64
	CacheReadNs atomic.Uint64
}

func (s *Stats) Render() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# TYPE ioimg_requests_total counter\n")
	fmt.Fprintf(&b, "ioimg_requests_total %d\n", s.Requests.Load())
	fmt.Fprintf(&b, "ioimg_cache_hits_total %d\n", s.Hits.Load())
	fmt.Fprintf(&b, "ioimg_cache_misses_total %d\n", s.Misses.Load())
	fmt.Fprintf(&b, "ioimg_errors_total %d\n", s.Errors.Load())
	fmt.Fprintf(&b, "ioimg_bytes_out_total %d\n", s.BytesOut.Load())
	fmt.Fprintf(&b, "ioimg_transform_ns_total %d\n", s.TransformNs.Load())
	fmt.Fprintf(&b, "ioimg_cache_read_ns_total %d\n", s.CacheReadNs.Load())
	return b.String()
}
