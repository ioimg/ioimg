package server

// celeris adapter: the production transport writes straight into the celeris
// context. celeris.Adapt would buffer the whole body first, which costs one
// extra copy of the response per request.

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/goceleris/celeris"
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
	for k, vs := range w.h {
		if strings.EqualFold(k, "Content-Type") {
			continue
		}
		for _, v := range vs {
			w.c.SetHeader(k, v)
		}
	}
	if ct == "" {
		ct = "application/octet-stream"
	}
	_ = w.c.Blob(status, ct, w.body)
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
