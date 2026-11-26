package server

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/ioimg/ioimg/service/internal/imaging"
	"github.com/ioimg/ioimg/service/pkg/ops"
)

func TestBestFormatHeadersAndETag(t *testing.T) {
	h := newHarness(t, nil)
	source := h.origin.URL + "/photo.jpg"

	resp := h.get(t, "/w_40,f_best,q_60/"+source, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	chosen := ops.Format(strings.TrimPrefix(resp.Header.Get("X-IOIMG-Format"), "image/"))
	if chosen != ops.FormatJPEG && chosen != ops.FormatWebP && chosen != ops.FormatAVIF {
		t.Fatalf("X-IOIMG-Format = %q, want a concrete image format", resp.Header.Get("X-IOIMG-Format"))
	}
	if got, want := resp.Header.Get("Content-Type"), imaging.ContentType(chosen); got != want {
		t.Fatalf("Content-Type = %q, want %q", got, want)
	}
	if got := imaging.ContentTypeFromBytes(body); got != resp.Header.Get("Content-Type") {
		t.Fatalf("body is %s, header says %s", got, resp.Header.Get("Content-Type"))
	}
	etag := resp.Header.Get("ETag")
	if etag == "" || !strings.Contains(etag, string(chosen)) {
		t.Fatalf("ETag %q does not carry the winning format %q", etag, chosen)
	}

	// The cache hit must resolve the same winner from the cached bytes and
	// answer a matching If-None-Match with 304.
	resp = h.get(t, "/w_40,f_best,q_60/"+source, map[string]string{"If-None-Match": etag})
	if resp.StatusCode != http.StatusNotModified {
		t.Fatalf("want 304, got %d", resp.StatusCode)
	}
	resp.Body.Close()
}
