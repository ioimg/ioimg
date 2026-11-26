package tenant

import "testing"

func TestHasDomainFromSnapshot(t *testing.T) {
	s := NewStore(nil)
	s.Apply(Snapshot{Revision: 1, Projects: []Project{
		{ID: "p1", Domains: []string{"cdn.shop.example", "*.img.shop.example"}},
		{ID: "p2", Domains: []string{"static.example"}},
	}})
	for _, host := range []string{"cdn.shop.example", "CDN.SHOP.EXAMPLE.", "a.img.shop.example", "static.example"} {
		if !s.HasDomain(host) {
			t.Errorf("HasDomain(%q) = false, want true", host)
		}
	}
	for _, host := range []string{"", "shop.example", "unknown.example", "cdn.shop.example.evil"} {
		if s.HasDomain(host) {
			t.Errorf("HasDomain(%q) = true, want false", host)
		}
	}
}

func TestHasDomainIgnoresFallbackAllowlist(t *testing.T) {
	// The request allowlist answers "who may request images", not "which custom
	// domains may receive a certificate". AllowAnyHost must never widen TLS.
	fallback := NewPolicy(Project{ID: "default", Allowlist: []string{"img.example.com"}, AllowAnyHost: true})
	s := NewStore(fallback)
	if s.HasDomain("img.example.com") {
		t.Fatal("allowlist entry must not be treated as a custom domain")
	}
	if s.HasDomain("anything.example") {
		t.Fatal("AllowAnyHost must not widen the TLS host policy")
	}
}
