// Package sign implements ioimg signed URLs (docs/API.md §4).
package sign

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Key is one HMAC signing key. IDs allow rotation with a grace period.
type Key struct {
	ID     string
	Secret []byte
}

// Signer verifies and produces signatures. Keys[0] is used for signing; all
// keys are accepted for verification.
type Signer struct {
	Keys           []Key
	IncludeExp     bool
	ExpiryRequired bool
	Now            func() time.Time
}

// ParseKeys parses "kid:secret,kid2:secret2". A single secret without a colon
// becomes kid "default".
func ParseKeys(raw string) ([]Key, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var keys []Key
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, secret, ok := strings.Cut(part, ":")
		if !ok {
			id, secret = "default", part
		}
		if id == "" || secret == "" {
			return nil, fmt.Errorf("invalid signing key entry")
		}
		keys = append(keys, Key{ID: id, Secret: []byte(secret)})
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("no signing keys")
	}
	return keys, nil
}

// Canonical builds the string that is signed and verified. It always ends with
// exp when IncludeExp is true.
func (s *Signer) Canonical(opsCanonical, source string, exp int64) string {
	body := opsCanonical + "/" + source
	if s.IncludeExp && exp > 0 {
		body += "?exp=" + strconv.FormatInt(exp, 10)
	}
	return body
}

// Sign returns the base64url signature without padding.
func (s *Signer) Sign(opsCanonical, source string, exp int64) (string, error) {
	if len(s.Keys) == 0 {
		return "", fmt.Errorf("no signing keys configured")
	}
	return s.signWith(s.Keys[0], opsCanonical, source, exp), nil
}

func (s *Signer) signWith(k Key, opsCanonical, source string, exp int64) string {
	mac := hmac.New(sha256.New, k.Secret)
	mac.Write([]byte(s.Canonical(opsCanonical, source, exp)))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// Verify checks a signature and optional expiry. Returned errors are safe to
// map to 401/410 without leaking key details.
func (s *Signer) Verify(sig, opsCanonical, source string, exp int64) error {
	if len(s.Keys) == 0 {
		return fmt.Errorf("no signing keys configured")
	}
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	if exp > 0 && now().Unix() > exp {
		return ErrExpired
	}
	if s.ExpiryRequired && exp == 0 {
		return ErrExpiryMissing
	}
	if sig == "" {
		return ErrInvalid
	}
	got, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil {
		return ErrInvalid
	}
	for _, k := range s.Keys {
		want := hmac.New(sha256.New, k.Secret)
		want.Write([]byte(s.Canonical(opsCanonical, source, exp)))
		if hmac.Equal(got, want.Sum(nil)) {
			return nil
		}
	}
	return ErrInvalid
}

// Errors returned by Verify.
var (
	ErrInvalid       = fmt.Errorf("invalid signature")
	ErrExpired       = fmt.Errorf("signature expired")
	ErrExpiryMissing = fmt.Errorf("expiry required")
)

// CanonicalSource normalises a source URL before it is signed, per
// docs/API.md §1.3: lowercase scheme/host, drop default port and fragment,
// collapse duplicate slashes in the path, and keep the query untouched.
func CanonicalSource(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid source url")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("source must be absolute http(s)")
	}
	if u.Host == "" {
		return "", fmt.Errorf("source host is required")
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	host := u.Hostname()
	port := u.Port()
	if port == "80" && u.Scheme == "http" || port == "443" && u.Scheme == "https" {
		u.Host = host
		if strings.Contains(host, ":") {
			u.Host = "[" + host + "]"
		}
	}
	u.Fragment = ""
	u.RawFragment = ""
	u.Path = collapseSlashes(u.Path)
	u.RawPath = ""
	// Keep RawQuery exactly as supplied: reordering can change origin semantics.
	return u.String(), nil
}

func collapseSlashes(p string) string {
	if p == "" {
		return ""
	}
	var b strings.Builder
	prevSlash := false
	for _, r := range p {
		if r == '/' {
			if prevSlash {
				continue
			}
			prevSlash = true
		} else {
			prevSlash = false
		}
		b.WriteRune(r)
	}
	return b.String()
}
