package source

import (
	"context"
	"net/http"
	"net/http/httptest"
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
