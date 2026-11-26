package server

// celeris adapter: the production transport writes straight into the celeris
// context. celeris.Adapt would buffer the whole body first, which costs one
// extra copy of the response per request.

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/goceleris/celeris"
)

// celeris' io_uring/epoll engines close a connection when un-ACKed response
// bytes exceed 4 MiB (maxSendQueueBytes), which truncated every raw_ pass-
// through larger than that to an empty body. Bodies above this threshold go
// through the incremental StreamWriter in fixed chunks instead, keeping the
// pending-send window small. 1 MiB leaves room for the response headers plus
// several chunks in flight on a slow link.
const (
	celerisStreamThreshold = 1 << 20
	celerisStreamChunk     = 256 << 10
)

type celerisWriter struct {
	c       *celeris.Context
	h       http.Header
	status  int
	body    []byte
	flushed bool
}

func (w *celerisWriter) Header() http.Header { return w.h }

func (w *celerisWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
}

func (w *celerisWriter) Write(b []byte) (int, error) {
	w.body = b
	w.flush()
	return len(b), nil
}

func (w *celerisWriter) flush() {
	if w.flushed {
		return
	}
	w.flushed = true
	status := w.status
	if status == 0 {
		status = http.StatusOK
	}
	ct := w.h.Get("Content-Type")
	if ct == "" {
		ct = "application/octet-stream"
	}
	// celeris writes content-type and content-length itself, so passing the
	// net/http duplicates through SetHeader would emit both twice.
	var extra [][2]string
	for k, vs := range w.h {
		if strings.EqualFold(k, "Content-Type") || strings.EqualFold(k, "Content-Length") {
			continue
		}
		for _, v := range vs {
			extra = append(extra, [2]string{strings.ToLower(k), v})
		}
	}

	if len(w.body) <= celerisStreamThreshold {
		for _, kv := range extra {
			w.c.SetHeader(kv[0], kv[1])
		}
		_ = w.c.Blob(status, ct, w.body)
		return
	}

	// A detached connection raises the engine's pending-send cap from 4 MiB to
	// 64 MiB (the raw_ ceiling), and is the documented precondition for
	// StreamWriter on the native engines. done() returns the Context to the pool.
	done := w.c.Detach()
	defer done()
	sw := w.c.StreamWriter()
	if sw == nil {
		// Engines without incremental streaming cannot send this body at all.
		// Buffer nothing: report the truncation instead of lying about the size.
		_ = w.c.Blob(http.StatusInternalServerError, "text/plain; charset=utf-8", []byte("response too large for this transport\n"))
		return
	}
	headers := make([][2]string, 0, len(extra)+2)
	headers = append(headers, [2]string{"content-type", ct})
	headers = append(headers, [2]string{"content-length", strconv.Itoa(len(w.body))})
	headers = append(headers, extra...)
	if err := sw.WriteHeader(status, headers); err != nil {
		return
	}
	for off := 0; off < len(w.body); off += celerisStreamChunk {
		end := off + celerisStreamChunk
		if end > len(w.body) {
			end = len(w.body)
		}
		if _, err := sw.Write(w.body[off:end]); err != nil {
			break
		}
		// Flush after each chunk: the engine closes the connection once un-ACKed bytes
		// pass 4 MiB, so the pending window has to be drained as we go.
		if err := sw.Flush(); err != nil {
			break
		}
	}
	_ = sw.Close()
}

// CelerisHandler serves the data plane through celeris.
func (s *Server) CelerisHandler(c *celeris.Context) error {
	hdr := make(http.Header, 4)
	if v := c.Header("Accept"); v != "" {
		hdr.Set("Accept", v)
	}
	if v := c.Header("If-None-Match"); v != "" {
		hdr.Set("If-None-Match", v)
	}
	if v := c.Header("X-Request-Id"); v != "" {
		hdr.Set("X-Request-Id", v)
	}
	r := &http.Request{
		Method:     c.Method(),
		Host:       c.Host(),
		RequestURI: c.Path(),
		URL:        &url.URL{Path: c.Path(), RawQuery: c.RawQuery()},
		Header:     hdr,
	}
	w := &celerisWriter{c: c, h: make(http.Header, 16)}
	s.dispatch(w, r)
	w.flush()
	return nil
}
