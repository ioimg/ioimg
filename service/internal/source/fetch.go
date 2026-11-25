// Package source fetches upstream images with SSRF protection.
package source

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Errors that callers map to the public error model.
var (
	ErrNotAllowed    = errors.New("source_not_allowed")
	ErrPrivateAddr   = errors.New("private_address_blocked")
	ErrTooLarge      = errors.New("payload_too_large")
	ErrNotFound      = errors.New("source_not_found")
	ErrOrigin        = errors.New("origin_error")
	ErrTimeout       = errors.New("origin_timeout")
	ErrInvalidSource = errors.New("invalid_source_url")
)

// Result is a fetched upstream object.
type Result struct {
	Body        []byte
	ContentType string
	Status      int
}

// Fetcher is safe for concurrent use.
type Fetcher struct {
	client       *http.Client
	timeout      time.Duration
	maxBytes     int64
	allowHost    func(string) bool
	allowPrivate bool
	userAgent    string
}

// Options configures a Fetcher.
type Options struct {
	Timeout      time.Duration
	MaxBytes     int64
	AllowHost    func(string) bool
	AllowPrivate bool
	UserAgent    string
}

// New builds a fetcher whose dialer re-checks every resolved IP, closing the
// DNS-rebinding hole that a plain allowlist check leaves open.
func New(o Options) *Fetcher {
	if o.Timeout <= 0 {
		o.Timeout = 15 * time.Second
	}
	if o.MaxBytes <= 0 {
		o.MaxBytes = 25 << 20
	}
	if o.AllowHost == nil {
		o.AllowHost = func(string) bool { return true }
	}
	dialer := &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           guardDialer(dialer, o.AllowPrivate),
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          256,
		MaxIdleConnsPerHost:   64,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ExpectContinueTimeout: time.Second,
		ResponseHeaderTimeout: o.Timeout,
	}
	f := &Fetcher{
		timeout:      o.Timeout,
		maxBytes:     o.MaxBytes,
		allowHost:    o.AllowHost,
		allowPrivate: o.AllowPrivate,
		userAgent:    o.UserAgent,
	}
	f.client = &http.Client{
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("too many redirects")
			}
			return f.checkURL(req.URL)
		},
	}
	return f
}

// Fetch retrieves an absolute http(s) URL.
func (f *Fetcher) Fetch(ctx context.Context, raw string) (*Result, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, ErrInvalidSource
	}
	if err := f.checkURL(u); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, f.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, ErrInvalidSource
	}
	req.Header.Set("User-Agent", f.userAgent)
	req.Header.Set("Accept", "image/*")
	resp, err := f.client.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || osTimeout(err) {
			return nil, ErrTimeout
		}
		return nil, fmt.Errorf("%w: %v", ErrOrigin, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone {
		return nil, ErrNotFound
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%w: upstream %d", ErrOrigin, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, f.maxBytes+1))
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || osTimeout(err) {
			return nil, ErrTimeout
		}
		return nil, fmt.Errorf("%w: %v", ErrOrigin, err)
	}
	if int64(len(body)) > f.maxBytes {
		return nil, ErrTooLarge
	}
	if len(body) == 0 {
		return nil, ErrOrigin
	}
	return &Result{Body: body, ContentType: resp.Header.Get("Content-Type"), Status: resp.StatusCode}, nil
}

func (f *Fetcher) checkURL(u *url.URL) error {
	if u.Scheme != "http" && u.Scheme != "https" {
		return ErrInvalidSource
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return ErrInvalidSource
	}
	if !f.allowHost(host) {
		return ErrNotAllowed
	}
	// IP literals are checked here; hostnames are checked again after DNS
	// resolution in the dialer.
	if ip := net.ParseIP(host); ip != nil && !f.allowPrivate && isPrivate(ip) {
		return ErrPrivateAddr
	}
	return nil
}

func guardDialer(d *net.Dialer, allowPrivate bool) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, err
		}
		if len(ips) == 0 {
			return nil, fmt.Errorf("no addresses for %s", host)
		}
		var lastErr error
		for _, ip := range ips {
			if !allowPrivate && isPrivate(ip.IP) {
				return nil, ErrPrivateAddr
			}
			conn, err := d.DialContext(ctx, network, net.JoinHostPort(ip.IP.String(), port))
			if err == nil {
				return conn, nil
			}
			lastErr = err
		}
		return nil, lastErr
	}
}

// isPrivate covers loopback, private, link-local (including cloud metadata),
// CGNAT, multicast, and unspecified addresses.
func isPrivate(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() || ip.IsInterfaceLocalMulticast() {
		return true
	}
	if v4 := ip.To4(); v4 != nil {
		// 100.64.0.0/10 (CGNAT)
		if v4[0] == 100 && v4[1]&0xc0 == 64 {
			return true
		}
		// 169.254.0.0/16 (metadata, already link-local but explicit)
		if v4[0] == 169 && v4[1] == 254 {
			return true
		}
		// 192.0.0.0/24
		if v4[0] == 192 && v4[1] == 0 && v4[2] == 0 {
			return true
		}
		// 198.18.0.0/15 benchmarking
		if v4[0] == 198 && (v4[1] == 18 || v4[1] == 19) {
			return true
		}
	}
	// IPv6 unique local fc00::/7 is covered by IsPrivate in modern Go, but be
	// explicit for older toolchains.
	if len(ip) == net.IPv6len && ip[0]&0xfe == 0xfc {
		return true
	}
	return false
}

func osTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}
