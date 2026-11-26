package source

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestFetcherAllowlistAndSize(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/big":
			w.Write(make([]byte, 2048))
		case "/redirect":
			http.Redirect(w, r, "/ok", http.StatusFound)
		default:
			w.Write([]byte("image-bytes"))
		}
	}))
	defer origin.Close()

	f := New(Options{
		Timeout:      time.Second,
		MaxBytes:     1024,
		AllowPrivate: true,
		AllowHost: func(h string) bool {
			return strings.HasPrefix(h, "127.0.0.1") || h == "localhost"
		},
	})
	res, err := f.Fetch(context.Background(), origin.URL+"/ok")
	if err != nil {
		t.Fatal(err)
	}
	if string(res.Body) != "image-bytes" {
		t.Fatalf("unexpected body: %q", res.Body)
	}
	if _, err := f.Fetch(context.Background(), origin.URL+"/big"); err != ErrTooLarge {
		t.Fatalf("want ErrTooLarge, got %v", err)
	}
	if _, err := f.Fetch(context.Background(), origin.URL+"/redirect"); err != nil {
		t.Fatalf("redirect should be followed: %v", err)
	}

	deny := New(Options{AllowPrivate: true, AllowHost: func(string) bool { return false }})
	if _, err := deny.Fetch(context.Background(), origin.URL+"/ok"); err != ErrNotAllowed {
		t.Fatalf("want ErrNotAllowed, got %v", err)
	}
}

func TestFetcherBlocksPrivate(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("x"))
	}))
	defer origin.Close()
	f := New(Options{AllowPrivate: false, AllowHost: func(string) bool { return true }})
	if _, err := f.Fetch(context.Background(), origin.URL); err != ErrPrivateAddr {
		t.Fatalf("want ErrPrivateAddr, got %v", err)
	}
}

func TestSourceAuthExactHostAndRedirectStripping(t *testing.T) {
	var gotA, gotB http.Header
	originB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotB = r.Header.Clone()
		w.Write([]byte("b"))
	}))
	defer originB.Close()
	originA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotA = r.Header.Clone()
		u, _ := url.Parse(originB.URL)
		u.Host = "localhost:" + u.Port()
		http.Redirect(w, r, u.String(), http.StatusFound)
	}))
	defer originA.Close()

	f := New(Options{
		Timeout:      time.Second,
		MaxBytes:     1024,
		AllowPrivate: true,
		SourceAuth: map[string]Auth{
			"127.0.0.1": {Name: "X-Api-Key", Value: "top-secret"},
		},
		PassthroughHeaders: []string{"X-Trace"},
	})
	if _, err := f.FetchWithHeaders(context.Background(), originA.URL+"/ok", http.Header{"X-Trace": {"trace-1"}}); err != nil {
		t.Fatal(err)
	}
	if gotA.Get("X-Api-Key") != "top-secret" {
		t.Fatalf("exact-host auth missing at A: %v", gotA)
	}
	if gotA.Get("X-Trace") != "trace-1" {
		t.Fatalf("passthrough missing at A: %v", gotA)
	}
	if gotB.Get("X-Api-Key") != "" || gotB.Get("X-Trace") != "" || gotB.Get("Authorization") != "" {
		t.Fatalf("credentials leaked across redirect: %v", gotB)
	}

	// localhost is a different exact host, so the auth for 127.0.0.1 must not
	// be applied even without a redirect.
	u, _ := url.Parse(originB.URL)
	u.Host = "localhost:" + u.Port()
	if _, err := f.Fetch(context.Background(), u.String()); err != nil {
		t.Fatal(err)
	}
	if gotB.Get("X-Api-Key") != "" {
		t.Fatalf("auth applied to non-matching host: %v", gotB)
	}
}

func TestPassthroughRejectsMalformedValues(t *testing.T) {
	var got http.Header
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.Write([]byte("ok"))
	}))
	defer origin.Close()
	f := New(Options{
		Timeout:            time.Second,
		AllowPrivate:       true,
		PassthroughHeaders: []string{"X-Trace"},
	})
	in := http.Header{
		"X-Trace": {"ok-value"},
	}
	if _, err := f.FetchWithHeaders(context.Background(), origin.URL, in); err != nil {
		t.Fatal(err)
	}
	if got.Get("X-Trace") != "ok-value" {
		t.Fatalf("well-formed value not forwarded: %v", got)
	}

	for name, value := range map[string]string{
		"crlf":     "good\r\nInjected: yes",
		"newline":  "good\nInjected: yes",
		"oversize": strings.Repeat("a", 1025),
	} {
		got = nil
		in.Set("X-Trace", value)
		if _, err := f.FetchWithHeaders(context.Background(), origin.URL, in); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got.Get("X-Trace") != "" {
			t.Fatalf("%s value was forwarded: %v", name, got)
		}
	}
}
