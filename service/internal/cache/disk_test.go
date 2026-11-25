package cache

import (
	"bytes"
	"os"
	"testing"
)

func TestDiskPutGetDeleteAndEvict(t *testing.T) {
	dir := t.TempDir()
	d, err := NewDisk(dir, 100)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Put("a", bytes.Repeat([]byte("x"), 40)); err != nil {
		t.Fatal(err)
	}
	if data, _, ok := d.Get("a"); !ok || len(data) != 40 {
		t.Fatalf("get a failed: %v %d", ok, len(data))
	}
	if err := d.Put("b", bytes.Repeat([]byte("y"), 40)); err != nil {
		t.Fatal(err)
	}
	if err := d.Put("c", bytes.Repeat([]byte("z"), 40)); err != nil {
		t.Fatal(err)
	}
	// Budget is 100 bytes; at least one entry must have been evicted.
	n, size := d.Stats()
	if size > 100 || n >= 3 {
		t.Fatalf("eviction failed: n=%d size=%d", n, size)
	}
	d.Delete("b")
	if _, _, ok := d.Get("b"); ok {
		t.Fatal("b should be deleted")
	}
	if err := d.Clear(); err != nil {
		t.Fatal(err)
	}
	des, _ := os.ReadDir(dir)
	if len(des) != 0 {
		t.Fatalf("clear left %d files", len(des))
	}
}

func TestKeyStable(t *testing.T) {
	a := Key("v1", "w=10", "https://x/a.jpg", "webp")
	b := Key("v1", "w=10", "https://x/a.jpg", "webp")
	c := Key("v1", "w=10", "https://x/a.jpg", "jpeg")
	if a != b || a == c {
		t.Fatalf("key function unstable: %s %s %s", a, b, c)
	}
}
