package purge

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCloudflare(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("bad auth header: %q", r.Header.Get("Authorization"))
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer srv.Close()
	c := &Cloudflare{Token: "tok", ZoneID: "zone", Options: Options{BaseURL: srv.URL}}
	res, err := c.Purge(context.Background(), []string{"https://img.example/a.jpg"}, []string{"src:1"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Result != statusOK || res.URLs != 1 || res.Tags != 1 {
		t.Fatalf("unexpected result: %+v", res)
	}
	if _, ok := got["files"]; !ok {
		t.Fatalf("missing files payload: %+v", got)
	}
}

func TestBunny(t *testing.T) {
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("AccessKey") != "key" {
			t.Errorf("bad access key: %q", r.Header.Get("AccessKey"))
		}
		queries = append(queries, r.URL.RawQuery)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	b := &Bunny{Key: "key", Options: Options{BaseURL: srv.URL}}
	res, err := b.Purge(context.Background(), []string{"https://img.example/a.jpg", "https://img.example/b.jpg"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Result != statusOK || len(queries) != 2 {
		t.Fatalf("unexpected result: %+v queries=%d", res, len(queries))
	}
	if !strings.Contains(queries[0], "url=https%3A%2F%2Fimg.example%2Fa.jpg") {
		t.Fatalf("unexpected query: %s", queries[0])
	}
}

func TestFastly(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Fastly-Key") != "tok" {
			t.Errorf("bad fastly key: %q", r.Header.Get("Fastly-Key"))
		}
		paths = append(paths, r.URL.Path+"|"+r.Header.Get("Surrogate-Key"))
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	f := &Fastly{Token: "tok", ServiceID: "svc", Options: Options{BaseURL: srv.URL}}
	if _, err := f.Purge(context.Background(), []string{"https://img.example/a.jpg"}, []string{"src:1", "src:2"}); err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 {
		t.Fatalf("want 2 calls, got %v", paths)
	}
	if !strings.Contains(paths[1], "src:1 src:2") {
		t.Fatalf("missing surrogate key: %v", paths)
	}
}
