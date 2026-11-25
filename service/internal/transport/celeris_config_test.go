package transport

import (
	"testing"
	"time"

	"github.com/goceleris/celeris"

	"github.com/ioimg/ioimg/service/internal/config"
)

// TestCelerisConfigHasTimeouts pins the slowloris guard on the default
// transport. Without these, a client can hold a connection open forever while
// dribbling bytes; fiber and net/http both set equivalents.
func TestCelerisConfigHasTimeouts(t *testing.T) {
	c := celerisConfig(config.Config{Addr: ":8080"})

	if c.Addr != ":8080" {
		t.Fatalf("Addr = %q, want :8080", c.Addr)
	}
	if c.Engine != celeris.IOUring {
		t.Fatalf("Engine = %v, want io_uring", c.Engine)
	}
	for _, tc := range []struct {
		name string
		got  time.Duration
		want time.Duration
	}{
		{"ReadTimeout", c.ReadTimeout, readTimeout},
		{"WriteTimeout", c.WriteTimeout, writeTimeout},
		{"IdleTimeout", c.IdleTimeout, idleTimeout},
	} {
		if tc.got <= 0 {
			t.Fatalf("%s = %v, want a positive timeout", tc.name, tc.got)
		}
		if tc.got != tc.want {
			t.Fatalf("%s = %v, want %v", tc.name, tc.got, tc.want)
		}
	}
	if c.MaxRequestBodySize <= 0 {
		t.Fatalf("MaxRequestBodySize = %d, want positive", c.MaxRequestBodySize)
	}
}
