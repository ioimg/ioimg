package config

import (
	"strings"
	"testing"
)

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

func TestParseSourceAuth(t *testing.T) {
	auth, err := parseSourceAuth(" CDN.Example.com. = Authorization:Bearer abc:def ,api.example.com=X-Api-Key: value with spaces ")
	if err != nil {
		t.Fatal(err)
	}
	if got := auth["cdn.example.com"]; got.Name != "Authorization" || got.Value != "Bearer abc:def" {
		t.Fatalf("unexpected auth: %+v", got)
	}
	if got := auth["api.example.com"]; got.Name != "X-Api-Key" || got.Value != "value with spaces" {
		t.Fatalf("unexpected auth: %+v", got)
	}
	for _, raw := range []string{
		"*.example.com=X-Key:secret-value",
		"https://example.com=X-Key:secret-value",
		"example.com=Bad Header:secret-value",
		"example.com=Authorization:secret\r\nInjected: yes",
		"example.com=X-Key:",
	} {
		if _, err := parseSourceAuth(raw); err == nil {
			t.Fatalf("want error for %q", raw)
		} else if strings.Contains(err.Error(), "secret") {
			t.Fatalf("error leaked the credential value: %v", err)
		}
	}
}

func TestParsePassthroughHeaders(t *testing.T) {
	got, err := parsePassthroughHeaders("x-trace, X-Request-Id, authorization, cookie")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"X-Trace", "X-Request-Id", "Authorization", "Cookie"}
	if len(got) != len(want) {
		t.Fatalf("unexpected headers: %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("unexpected headers: %v", got)
		}
	}
	for _, raw := range []string{"Host", "Content-Length", "Bad Header", "bad\nname"} {
		if _, err := parsePassthroughHeaders(raw); err == nil {
			t.Fatalf("want error for %q", raw)
		}
	}
}

func TestParseResponseHeadersRejectsInjection(t *testing.T) {
	got, err := parseResponseHeaders("X-Robots-Tag:noindex, Cross-Origin-Resource-Policy: cross-origin")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Name != "X-Robots-Tag" || got[0].Value != "noindex" || got[1].Value != "cross-origin" {
		t.Fatalf("unexpected headers: %+v", got)
	}
	for _, raw := range []string{
		"Bad Header:value",
		"X-Test:ok\r\nInjected: yes",
		"X-Test:ok\nInjected: yes",
		"X-Test",
		":value",
	} {
		if _, err := parseResponseHeaders(raw); err == nil {
			t.Fatalf("want error for %q", raw)
		}
	}
}

func TestSecurityFeaturesDefaultOff(t *testing.T) {
	auth, err := parseSourceAuth("")
	if err != nil || len(auth) != 0 {
		t.Fatalf("SOURCE_AUTH must default to off: %v %v", auth, err)
	}
	passthrough, err := parsePassthroughHeaders("")
	if err != nil || len(passthrough) != 0 {
		t.Fatalf("PASSTHROUGH_HEADERS must default to off: %v %v", passthrough, err)
	}
	headers, err := parseResponseHeaders("")
	if err != nil || len(headers) != 0 {
		t.Fatalf("RESPONSE_HEADERS must default to off: %v %v", headers, err)
	}
}
