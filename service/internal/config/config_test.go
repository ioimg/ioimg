package config

import "testing"

func TestAllowedHost(t *testing.T) {
	c := Config{Allowlist: []string{"cdn.example.com", "*.acme.com"}}
	cases := map[string]bool{
		"cdn.example.com":      true,
		"CDN.EXAMPLE.COM.":     true,
		"img.acme.com":         true,
		"a.b.acme.com":         true,
		"acme.com":             false,
		"evil.com":             false,
		"cdn.example.com.evil": false,
	}
	for host, want := range cases {
		if got := c.AllowedHost(host); got != want {
			t.Errorf("AllowedHost(%q) = %v, want %v", host, got, want)
		}
	}
}

func TestAllowedHostOpenMode(t *testing.T) {
	c := Config{AllowAnyHost: true}
	if !c.AllowedHost("anything.example") {
		t.Fatal("open mode must allow any host")
	}
}
