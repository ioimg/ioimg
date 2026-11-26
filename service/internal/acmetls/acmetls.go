// Package acmetls provides optional automatic HTTPS with ACME (RFC 8555).
//
// It is used by the data plane only when IOIMG_TLS=auto. The default deployment
// terminates TLS at the CDN edge, so this package is for self-hosted or
// direct-origin setups where no external proxy holds the certificates.
//
// The host policy is deliberately strict: a certificate is requested only for
// an FQDN that is either a custom domain in the tenant snapshot currently
// loaded from the control plane or listed in the static IOIMG_TLS_HOSTS
// allowlist. Arbitrary SNI names are rejected before any ACME request, which
// keeps a hostile client from burning the CA's issuance rate limit.
package acmetls

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/acme"
	"golang.org/x/crypto/acme/autocert"
)

// Options configures the ACME manager.
type Options struct {
	// CacheDir stores the ACME account key and issued certificates. It is
	// created with 0700 permissions when missing.
	CacheDir string
	// Email is the optional ACME account contact.
	Email string
	// CA is the ACME directory URL. Empty means Let's Encrypt production, or
	// staging when Staging is true. An explicit CA always wins over Staging.
	CA string
	// Staging selects the Let's Encrypt staging directory for tests.
	Staging bool
	// Hosts is the static hostname allowlist (IOIMG_TLS_HOSTS). Entries may be
	// exact names or *.suffix wildcards. A wildcard only authorises individual
	// subdomains; it does not request a wildcard certificate.
	Hosts []string
	// Allowed is an optional dynamic allowlist consulted after Hosts. The data
	// plane wires it to the tenant store so a custom domain that appears in a
	// fresh control-plane snapshot becomes eligible without a restart.
	Allowed func(host string) bool
}

// StagingURL is the Let's Encrypt staging directory. It exists here because the
// vendored acme package does not export a constant for it. Staging certificates
// are not trusted by browsers; use them only for tests.
const StagingURL = "https://acme-staging-v02.api.letsencrypt.org/directory"

// Manager wraps autocert.Manager with the ioimg host policy and a sanitised
// directory cache.
type Manager struct {
	manager *autocert.Manager
	static  []string
	allowed func(host string) bool
}

// New builds a Manager. It does not make any network request; certificates are
// only requested when a client first presents SNI for an allowed host.
func New(o Options) (*Manager, error) {
	if strings.TrimSpace(o.CacheDir) == "" {
		return nil, errors.New("acmetls: cache dir is required")
	}
	if err := os.MkdirAll(o.CacheDir, 0o700); err != nil {
		return nil, fmt.Errorf("acmetls: create cache dir: %w", err)
	}
	if info, err := os.Stat(o.CacheDir); err != nil {
		return nil, fmt.Errorf("acmetls: stat cache dir: %w", err)
	} else if !info.IsDir() {
		return nil, fmt.Errorf("acmetls: cache path %q is not a directory", o.CacheDir)
	}

	static := make([]string, 0, len(o.Hosts))
	for _, entry := range o.Hosts {
		entry = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(entry), "."))
		if entry == "" {
			continue
		}
		static = append(static, entry)
	}

	ca := strings.TrimSpace(o.CA)
	if ca == "" {
		ca = acme.LetsEncryptURL
		if o.Staging {
			ca = StagingURL
		}
	}

	m := &Manager{static: static, allowed: o.Allowed}
	m.manager = &autocert.Manager{
		Prompt:     autocert.AcceptTOS,
		Cache:      NewCache(o.CacheDir),
		HostPolicy: m.hostPolicy,
		Email:      strings.TrimSpace(o.Email),
		Client:     &acme.Client{DirectoryURL: ca},
	}
	return m, nil
}

// TLSConfig returns the tls.Config for the HTTPS listener. It enables HTTP/2
// and the tls-alpn-01 challenge.
func (m *Manager) TLSConfig() *tls.Config { return m.manager.TLSConfig() }

// HTTPHandler returns the HTTP-01 challenge handler. Requests that are not an
// ACME challenge are passed to fallback.
func (m *Manager) HTTPHandler(fallback http.Handler) http.Handler {
	return m.manager.HTTPHandler(fallback)
}

// AllowsHost reports whether host may be issued a certificate. It applies the
// same policy as the TLS handshake and never performs network I/O, so it is
// safe to call from a request path or a test.
func (m *Manager) AllowsHost(host string) bool {
	return m.hostPolicy(context.Background(), host) == nil
}

func (m *Manager) hostPolicy(_ context.Context, host string) error {
	name, ok := NormalizeHost(host)
	if !ok {
		return fmt.Errorf("acmetls: host %q is not a valid hostname", host)
	}
	for _, entry := range m.static {
		if matchHost(entry, name) {
			return nil
		}
	}
	if m.allowed != nil && m.allowed(name) {
		return nil
	}
	return fmt.Errorf("acmetls: host %q is not a configured custom domain", name)
}

// NormalizeHost lowercases an SNI/Host value, strips a trailing dot and an
// optional port, and validates the hostname labels. IP addresses are rejected:
// public CAs do not issue certificates for them.
func NormalizeHost(host string) (string, bool) {
	host = strings.ToLower(strings.TrimSpace(host))
	host = strings.TrimSuffix(host, ".")
	if host == "" || strings.Contains(host, "://") {
		return "", false
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = strings.TrimSuffix(h, ".")
	} else if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		return "", false
	}
	if host == "" || len(host) > 253 {
		return "", false
	}
	if net.ParseIP(host) != nil {
		return "", false
	}
	if strings.ContainsAny(host, "* /\\:@\x00\r\n\t") {
		return "", false
	}
	for _, label := range strings.Split(host, ".") {
		if !validLabel(label) {
			return "", false
		}
	}
	return host, true
}

func validLabel(label string) bool {
	if label == "" || len(label) > 63 {
		return false
	}
	if label[0] == '-' || label[len(label)-1] == '-' {
		return false
	}
	for i := 0; i < len(label); i++ {
		c := label[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-':
		default:
			return false
		}
	}
	return true
}

// matchHost matches an exact hostname or a *.suffix wildcard entry.
func matchHost(entry, host string) bool {
	entry = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(entry), "."))
	if entry == host {
		return true
	}
	if strings.HasPrefix(entry, "*.") {
		suffix := entry[1:]
		return strings.HasSuffix(host, suffix) && len(host) > len(suffix)
	}
	return false
}

// SanitizeCacheName maps an autocert cache key to a single safe file name. Any
// character outside [A-Za-z0-9._+@-] is replaced with '_', leading dots are
// removed (so a key can never become "." or ".."), and the result is truncated
// to a filesystem-safe length. The returned name never contains a path
// separator, so an SNI-derived key cannot escape the cache directory.
func SanitizeCacheName(name string) string {
	var b strings.Builder
	b.Grow(len(name))
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '.', r == '-', r == '_', r == '+', r == '@':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := strings.TrimLeft(b.String(), ".")
	if out == "" || out == "." || out == ".." {
		out = "_"
	}
	if len(out) > 200 {
		out = out[:200]
	}
	return out
}

// Cache is an autocert.Cache backed by a directory with sanitised file names.
type Cache struct {
	dir string
}

// NewCache returns a cache rooted at dir.
func NewCache(dir string) *Cache {
	return &Cache{dir: filepath.Clean(strings.TrimSpace(dir))}
}

func (c *Cache) path(key string) (string, error) {
	if c == nil || c.dir == "" || c.dir == "." {
		return "", errors.New("acmetls: cache directory is not configured")
	}
	name := SanitizeCacheName(key)
	p := filepath.Join(c.dir, name)
	rel, err := filepath.Rel(c.dir, p)
	if err != nil || rel == ".." || rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("acmetls: unsafe cache key %q", key)
	}
	return p, nil
}

// Get implements autocert.Cache.
func (c *Cache) Get(ctx context.Context, key string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p, err := c.path(key)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		return nil, autocert.ErrCacheMiss
	}
	if err != nil {
		return nil, err
	}
	return data, nil
}

// Put implements autocert.Cache. The write is atomic: a temp file in the cache
// directory is renamed into place after the data is on disk.
func (c *Cache) Put(ctx context.Context, key string, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p, err := c.path(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(c.dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(c.dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() {
		_ = os.Remove(tmp)
	}()
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// Delete implements autocert.Cache.
func (c *Cache) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p, err := c.path(key)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Compile-time checks that the cache satisfies autocert.Cache.
var _ autocert.Cache = (*Cache)(nil)
