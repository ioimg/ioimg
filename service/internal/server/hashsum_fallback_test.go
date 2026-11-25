package server

import (
	"crypto/md5"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/ioimg/ioimg/service/pkg/ops"
)

// TestHashsumVerified checks hs_ passes on a matching digest and fails with 422
// on a mismatch, before any pixels are decoded.
func TestHashsumVerified(t *testing.T) {
	h := newHarness(t, nil)
	src := h.origin.URL + "/photo.jpg"
	sum := sha256.Sum256(jpegBytes(t))
	good := hex.EncodeToString(sum[:])

	path := fmt.Sprintf("/w_32,f_jpg,hs_sha256:%s/%s", good, src)
	resp := h.get(t, path, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("matching hashsum: %d", resp.StatusCode)
	}
	resp.Body.Close()

	bad := strings.Repeat("0", 64)
	resp = h.get(t, fmt.Sprintf("/w_32,f_jpg,hs_sha256:%s/%s", bad, src), nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("mismatched hashsum: %d, want 422", resp.StatusCode)
	}
	if body := readBody(t, resp); !strings.Contains(body, "hashsum_mismatch") {
		t.Fatalf("error body %q", body)
	}
}

// TestFallbackImageServed checks fiu_ replaces a missing source, and that the
// fallback goes through the same allowlist-guarded fetcher.
func TestFallbackImageServed(t *testing.T) {
	h := newHarness(t, nil)
	fallback := h.origin.URL + "/photo.jpg"
	enc := base64.RawURLEncoding.EncodeToString([]byte(fallback))
	path := fmt.Sprintf("/w_32,f_jpg,fiu_%s/%s/missing.jpg", enc, h.origin.URL)

	resp := h.get(t, path, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("fallback: %d, want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("X-IOIMG-Fallback"); got != "1" {
		t.Errorf("X-IOIMG-Fallback = %q, want 1", got)
	}
	if body := readBody(t, resp); len(body) == 0 {
		t.Error("fallback returned an empty body")
	}
}

// TestFallbackNotUsedWhenSourceWorks keeps the happy path untouched.
func TestFallbackNotUsedWhenSourceWorks(t *testing.T) {
	h := newHarness(t, nil)
	fallback := base64.RawURLEncoding.EncodeToString([]byte(h.origin.URL + "/missing2.jpg"))
	resp := h.get(t, fmt.Sprintf("/w_32,f_jpg,fiu_%s/%s/photo.jpg", fallback, h.origin.URL), nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("source present: %d", resp.StatusCode)
	}
	if got := resp.Header.Get("X-IOIMG-Fallback"); got != "" {
		t.Errorf("unexpected fallback header %q", got)
	}
}

// TestFallbackAlsoUnreachable keeps the original source error.
func TestFallbackAlsoUnreachable(t *testing.T) {
	h := newHarness(t, nil)
	fallback := base64.RawURLEncoding.EncodeToString([]byte(h.origin.URL + "/missing.jpg"))
	resp := h.get(t, fmt.Sprintf("/w_32,f_jpg,fiu_%s/%s/missing.jpg", fallback, h.origin.URL), nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("both missing: %d, want 404", resp.StatusCode)
	}
}

func TestHashsumMatches(t *testing.T) {
	body := []byte("ioimg")

	md5sum := md5.Sum(body)
	if !hashsumMatches(ops.Hashsum{Type: "md5", Hex: hex.EncodeToString(md5sum[:])}, body) {
		t.Error("matching md5 rejected")
	}
	if hashsumMatches(ops.Hashsum{Type: "md5", Hex: strings.Repeat("0", 32)}, body) {
		t.Error("wrong md5 accepted")
	}
	// An unknown type can only be reached when parsing was bypassed; it must not
	// silently reject traffic.
	if !hashsumMatches(ops.Hashsum{Type: "bogus", Hex: "00"}, body) {
		t.Error("unknown type should not reject")
	}

	sum := sha256.Sum256(body)
	if !hashsumMatches(ops.Hashsum{Type: "sha256", Hex: hex.EncodeToString(sum[:])}, body) {
		t.Error("matching sha256 rejected")
	}
	if hashsumMatches(ops.Hashsum{Type: "sha256", Hex: strings.Repeat("a", 64)}, body) {
		t.Error("wrong sha256 accepted")
	}
	if !hashsumMatches(ops.Hashsum{Type: "sha512", Hex: hex.EncodeToString(func() []byte { s := sha512.Sum512(body); return s[:] }())}, body) {
		t.Error("matching sha512 rejected")
	}
}

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
