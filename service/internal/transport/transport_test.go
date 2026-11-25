package transport

import (
	"testing"

	"github.com/ioimg/ioimg/service/internal/cache"
)

func TestPreforkEnv(t *testing.T) {
	cases := []struct {
		value   string
		want    bool
		enabled bool
	}{
		{"", false, true},
		{"1", true, true},
		{"true", true, true},
		{"yes", true, true},
		{"on", true, true},
		{"0", false, false},
		{"false", false, false},
		{"no", false, false},
		{"off", false, false},
		{"bogus", false, true},
	}
	for _, tc := range cases {
		t.Setenv("IOIMG_PREFORK", tc.value)
		if got := preforkRequested(); got != tc.want {
			t.Errorf("preforkRequested(%q) = %v, want %v", tc.value, got, tc.want)
		}
		if got := preforkEnabled(); got != tc.enabled {
			t.Errorf("preforkEnabled(%q) = %v, want %v", tc.value, got, tc.enabled)
		}
	}
}

// The cache read mode is chosen per transport: blocking reads for the pinned
// celeris loops, io_uring for fibre, one fd for net/http.
func TestReadModeSelection(t *testing.T) {
	uring := cache.UringAvailable()
	d, err := cache.NewDisk(t.TempDir(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []cache.ReadMode{cache.ReadOpenStat, cache.ReadUring, cache.ReadOS} {
		d.SetReadMode(mode)
		want := string(mode)
		if mode == cache.ReadAuto {
			want = string(cache.ReadOpenStat)
			if uring {
				want = string(cache.ReadUring)
			}
		}
		if got := d.ReadMode(); got != want {
			t.Errorf("SetReadMode(%q) resolved to %q, want %q", mode, got, want)
		}
	}
}
