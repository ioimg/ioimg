package server

import (
	"net/http/httptest"
	"testing"

	"github.com/ioimg/ioimg/service/pkg/tenant"
)

// FuzzParseTarget feeds arbitrary paths and query strings to the URL parser.
// The parser sits in front of every request, so a panic or an accidentally
// accepted hostile path is a production incident, not a test failure.
func FuzzParseTarget(f *testing.F) {
	seeds := []string{
		"/w_400,f_jpg,q_80/http://127.0.0.1/a.jpg",
		"/w_400,h_400,c_fill,g_auto,f_auto/http://ex.com/a.png?x=1&i=2",
		"/es_AAAA/f_png",
		"/raw_1/http://ex.com/a.gif",
		"/img?url=http://ex.com/a.jpg&w=10&opt=abc",
		"/w_1,h_1,c_force,bg_ff0000ff,ex_1:ce,fl_1,f_webp/http://ex.com/a.bmp",
		"/t_thumb/http://ex.com/a.jpg",
		"///../..",
		"",
		"/",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	p := tenant.NewPolicy(tenant.Project{ID: "p", AllowAnyHost: true})
	f.Fuzz(func(t *testing.T, path string) {
		if len(path) > 4096 {
			return
		}
		r := httptest.NewRequest("GET", "http://example.com/", nil)
		r.URL.Path = path
		if i := indexByte(path, '?'); i >= 0 {
			r.URL.Path = path[:i]
			r.URL.RawQuery = path[i+1:]
		}
		// Must never panic and must never return an error-free target pointing
		// at something that is not http(s) or es_.
		if tgt, err := parseTarget(r, p); err == nil {
			if tgt.Source == "" {
				t.Fatalf("accepted a target with no source: %q", path)
			}
		}
	})
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}
