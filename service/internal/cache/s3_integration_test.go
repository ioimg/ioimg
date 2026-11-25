package cache

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/johannesboyne/gofakes3"
	"github.com/johannesboyne/gofakes3/backend/s3mem"
)

func TestS3L2WithFakeS3(t *testing.T) {
	fake := httptest.NewServer(gofakes3.New(s3mem.New()).Server())
	defer fake.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	s, err := NewS3(ctx, S3Options{
		Endpoint:  fake.URL,
		Bucket:    "ioimg-test",
		AccessKey: "key",
		SecretKey: "secret",
		Prefix:    "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Put(ctx, "abc123", []byte("payload")); err != nil {
		t.Fatal(err)
	}
	data, ok, err := s.Get(ctx, "abc123")
	if err != nil || !ok || string(data) != "payload" {
		t.Fatalf("get: ok=%v err=%v data=%q", ok, err, data)
	}
	if _, ok, err := s.Get(ctx, "missing"); err != nil || ok {
		t.Fatalf("missing key: ok=%v err=%v", ok, err)
	}
	if err := s.Delete(ctx, "abc123"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.Get(ctx, "abc123"); ok {
		t.Fatal("key still present after delete")
	}
	for _, k := range []string{"p1", "p2", "p3"} {
		if err := s.Put(ctx, k, []byte(k)); err != nil {
			t.Fatal(err)
		}
	}
	n, err := s.DeletePrefix(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if n < 3 {
		t.Fatalf("expected at least 3 deleted objects, got %d", n)
	}
}
