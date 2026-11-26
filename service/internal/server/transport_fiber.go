package server

// Fiber adapter: the handler is the net/http one, so both transports stay
// byte-identical. fasthttp normalises "//" in the URI path, which would corrupt
// the source URL, so the raw request target is read from the header instead of
// c.Path().

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/gofiber/fiber/v3"
	"github.com/valyala/fasthttp"
)

type fiberWriter struct {
	c       fiber.Ctx
	h       http.Header
	status  int
	flushed bool
}

func (w *fiberWriter) Header() http.Header { return w.h }

func (w *fiberWriter) flush() {
	if w.flushed {
		return
	}
	w.flushed = true
	for k, vs := range w.h {
		for _, v := range vs {
			w.c.Response().Header.Add(k, v)
		}
	}
	code := w.status
	if code == 0 {
		code = http.StatusOK
	}
	w.c.Status(code)
}

func (w *fiberWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.flush()
}

func (w *fiberWriter) Write(b []byte) (int, error) {
	w.flush()
	return w.c.Write(b)
}

// fiberHeaders copies every request header. See celerisHeaders for why the
// hand-picked subset was wrong: the project header, pass-through headers, and
// X-Forwarded-* must reach the handler or those features are silently dead on
// this transport.
func fiberHeaders(req *fasthttp.Request) http.Header {
	hdr := make(http.Header, 16)
	req.Header.VisitAll(func(k, v []byte) {
		hdr.Add(string(k), string(v))
	})
	return hdr
}

// FiberHandler serves the data plane through Fiber.
func (s *Server) FiberHandler(c fiber.Ctx) error {
	req := c.Request()
	hdr := fiberHeaders(req)
	raw := string(req.Header.RequestURI())
	path, query := raw, ""
	if i := strings.IndexByte(raw, '?'); i >= 0 {
		path, query = raw[:i], raw[i+1:]
	}
	r := &http.Request{
		Method:     string(c.Method()),
		Host:       string(req.Host()),
		RequestURI: raw,
		URL:        &url.URL{Path: path, RawQuery: query},
		Header:     hdr,
	}
	w := &fiberWriter{c: c, h: make(http.Header, 16)}
	s.dispatch(w, r)
	w.flush()
	return nil
}
