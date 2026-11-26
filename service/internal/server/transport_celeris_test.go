package server

import (
	"net/http"
	"strconv"
	"testing"

	"github.com/goceleris/celeris/celeristest"
)

// A body above celerisStreamThreshold must not be handed to Blob: the io_uring
// engine closes the connection once un-ACKed bytes pass 4 MiB, which used to
// truncate large raw_ responses to an empty body. celeristest' recorder cannot
// express incre-mental streaming, so this asserts the documented alternative:
// without a Streamer the adapter fails loudly instead of advertising a
// Content-Length it cannot honour.
func TestCelerisLargeBodyDoesNotTruncateSilently(t *testing.T) {
	c, rec := celeristest.NewContextT(t, http.MethodGet, "/raw_1/http://example.test/a.png")
	defer celeristest.ReleaseContext(c)

	w := &celerisWriter{c: c, h: make(http.Header, 4)}
	body := make([]byte, celerisStreamThreshold+1)
	w.Header().Set("Content-Type", "image/png")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(body); err != nil {
		t.Fatalf("write: %v", err)
	}

	if rec.StatusCode == http.StatusOK {
		t.Fatalf("oversized body returned 200 on a non-streaming engine")
	}
	if got := rec.Header("content-length"); got == strconv.Itoa(len(body)) {
		t.Fatalf("advertised content-length %q for a body the transport cannot send", got)
	}
}

func TestCelerisSmallBodyUsesBlob(t *testing.T) {
	c, rec := celeristest.NewContextT(t, http.MethodGet, "/healthz")
	defer celeristest.ReleaseContext(c)

	w := &celerisWriter{c: c, h: make(http.Header, 4)}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	body := []byte("ok")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(body); err != nil {
		t.Fatalf("write: %v", err)
	}
	if rec.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.StatusCode)
	}
	if rec.BodyString() != "ok" {
		t.Fatalf("body = %q, want %q", rec.BodyString(), "ok")
	}
}

// The project header picks the tenant, so a transport that forwards only a
// hand-picked set of headers silently disables per-project routing. This pins
// the full copy: an arbitrary header, a header the adapter used to special-case,
// and a repeated header must all survive.
func TestCelerisForwardsAllRequestHeaders(t *testing.T) {
	c, _ := celeristest.NewContextT(t, http.MethodGet, "/healthz",
		celeristest.WithHeader("X-IOIMG-Project", "p_demo"),
		celeristest.WithHeader("Accept", "image/avif"),
		celeristest.WithHeader("Range", "bytes=0-99"),
		celeristest.WithHeader("X-Forwarded-For", "203.0.113.7"),
	)
	defer celeristest.ReleaseContext(c)

	hdr := celerisHeaders(c)
	for name, want := range map[string]string{
		"X-IOIMG-Project": "p_demo",
		"Accept":          "image/avif",
		"Range":           "bytes=0-99",
		"X-Forwarded-For": "203.0.113.7",
	} {
		if got := hdr.Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
}
