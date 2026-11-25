// Package purge implements best-effort CDN invalidation adapters.
package purge

import (
	"context"
	"net/http"
	"strings"
	"time"
)

// Result is one provider's outcome, serialised in the purge response.
type Result struct {
	Provider string `json:"provider"`
	Result   string `json:"result"`
	Error    string `json:"error,omitempty"`
	URLs     int    `json:"urls"`
	Tags     int    `json:"tags,omitempty"`
}

// Purger invalidates URLs and/or cache tags at one provider.
type Purger interface {
	Name() string
	Purge(ctx context.Context, urls, tags []string) (Result, error)
}

// Options are shared provider settings.
type Options struct {
	BaseURL string // override for tests; empty uses the public API
	Client  *http.Client
}

func baseOr(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return strings.TrimRight(v, "/")
}

func clientOr(c *http.Client) *http.Client {
	if c != nil {
		return c
	}
	return &http.Client{Timeout: 15 * time.Second}
}

// Safe results recorded on error paths (never leak credentials).
const (
	statusOK      = "ok"
	statusFailed  = "failed"
	statusSkipped = "skipped"
)
