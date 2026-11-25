package sign

import (
	"testing"
	"time"
)

func TestSignVerify(t *testing.T) {
	keys, err := ParseKeys("k1:secret,k2:old")
	if err != nil {
		t.Fatal(err)
	}
	s := &Signer{Keys: keys, IncludeExp: true}
	opsCanonical := "c=fit,f=jpeg,w=400"
	source, err := CanonicalSource("HTTPS://CDN.Example.com:443//a//b.jpg#frag")
	if err != nil {
		t.Fatal(err)
	}
	if source != "https://cdn.example.com/a/b.jpg" {
		t.Fatalf("unexpected canonical source: %s", source)
	}
	exp := time.Now().Add(time.Hour).Unix()
	sig, err := s.Sign(opsCanonical, source, exp)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Verify(sig, opsCanonical, source, exp); err != nil {
		t.Fatalf("verify failed: %v", err)
	}
	if err := s.Verify(sig, opsCanonical, source, exp+1); err == nil {
		t.Fatal("signature with a different exp must fail")
	}
	if err := s.Verify("not-a-signature", opsCanonical, source, exp); err == nil {
		t.Fatal("garbage signature must fail")
	}
}

func TestExpiry(t *testing.T) {
	keys, _ := ParseKeys("k1:secret")
	s := &Signer{Keys: keys, IncludeExp: true, Now: func() time.Time { return time.Unix(1000, 0) }}
	exp := int64(900)
	sig, _ := s.Sign("w=10", "https://example.com/a.jpg", exp)
	if err := s.Verify(sig, "w=10", "https://example.com/a.jpg", exp); err != ErrExpired {
		t.Fatalf("want ErrExpired, got %v", err)
	}
}

func TestExpiryRequired(t *testing.T) {
	keys, _ := ParseKeys("k1:secret")
	s := &Signer{Keys: keys, IncludeExp: true, ExpiryRequired: true}
	sig, _ := s.Sign("w=10", "https://example.com/a.jpg", 0)
	if err := s.Verify(sig, "w=10", "https://example.com/a.jpg", 0); err != ErrExpiryMissing {
		t.Fatalf("want ErrExpiryMissing, got %v", err)
	}
}
