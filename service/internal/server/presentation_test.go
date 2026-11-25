package server

import (
	"net/http"
	"strings"
	"testing"

	"github.com/ioimg/ioimg/service/internal/config"
	"github.com/ioimg/ioimg/service/pkg/ops"
)

// TestFilenameSetsContentDisposition covers fn_: it is presentation only, so it
// must reach the response header without changing the stored bytes.
func TestFilenameSetsContentDisposition(t *testing.T) {
	h := newHarness(t, nil)
	base := h.srv.URL + "/w_400,f_jpeg/" + h.origin.URL + "/photo.jpg"

	resp, err := http.Get(h.srv.URL + "/w_400,f_jpeg,fn_hero.jpg/" + h.origin.URL + "/photo.jpg")
	if err != nil {
		t.Fatal(err)
	}
	body := make([]byte, 1<<20)
	n, _ := resp.Body.Read(body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if cd := resp.Header.Get("Content-Disposition"); cd != `inline; filename="hero.jpg"` {
		t.Fatalf("Content-Disposition = %q", cd)
	}

	plain, err := http.Get(base)
	if err != nil {
		t.Fatal(err)
	}
	plainBody := make([]byte, 1<<20)
	m, _ := plain.Body.Read(plainBody)
	_ = plain.Body.Close()
	if string(body[:n]) != string(plainBody[:m]) {
		t.Fatal("fn_ changed the response body")
	}
}

// TestCachebusterDoesNotSplitCache pins the whole point of cb_: a new token
// busts the CDN, not the origin cache.
func TestCachebusterDoesNotSplitCache(t *testing.T) {
	h := newHarness(t, nil)
	get := func(cb string) *http.Response {
		resp, err := http.Get(h.srv.URL + "/w_400,f_jpeg,cb_" + cb + "/" + h.origin.URL + "/photo.jpg")
		if err != nil {
			t.Fatal(err)
		}
		_, _ = resp.Body.Read(make([]byte, 1<<20))
		_ = resp.Body.Close()
		return resp
	}
	first := get("v1")
	if first.StatusCode != http.StatusOK || first.Header.Get("X-IOIMG-Cache") != "MISS" {
		t.Fatalf("first: %d %s", first.StatusCode, first.Header.Get("X-IOIMG-Cache"))
	}
	busted := get("v2")
	if state := busted.Header.Get("X-IOIMG-Cache"); !strings.HasPrefix(state, "HIT") {
		t.Fatalf("different cb_ re-fetched and re-transformed: %s", state)
	}
}

// TestCachebusterIsSigned keeps a CDN-bypass token from being forged on top of
// an otherwise valid signed URL: the token is part of the signed canonical
// form, so swapping it must fail closed.
func TestCachebusterIsSigned(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.SignedOnly = true })
	opsRaw := "w_400,f_jpeg,cb_v1"
	o, err := ops.Parse(opsRaw, nil)
	if err != nil {
		t.Fatal(err)
	}
	source := h.origin.URL + "/photo.jpg"
	const exp = int64(4102444800)
	sig, err := h.signer.Sign(o.SigningCanonical(), source, exp)
	if err != nil {
		t.Fatal(err)
	}
	get := func(segment string) int {
		resp, err := http.Get(h.srv.URL + "/" + sig + "/" + segment + "/" + source + "?exp=" + itoa(exp))
		if err != nil {
			t.Fatal(err)
		}
		_, _ = resp.Body.Read(make([]byte, 1<<20))
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	if code := get(opsRaw); code != http.StatusOK {
		t.Fatalf("signed cb_ request: %d, want 200", code)
	}
	if code := get("w_400,f_jpeg,cb_v2"); code != http.StatusUnauthorized {
		t.Fatalf("swapped cb_ accepted: %d, want 401", code)
	}
}

func itoa(v int64) string {
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	return string(b[i:])
}
