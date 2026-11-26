package tenant

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"
)

func bytesReader(b []byte) io.Reader { return bytes.NewReader(b) }

// Store holds the current immutable snapshot and resolves tenants by host.
type Store struct {
	mu       sync.RWMutex
	revision int64
	byID     map[string]*Policy
	byHost   map[string]*Policy
	wildcard []*Policy
	fallback *Policy // env mode only: single tenant, no control plane
}

// NewStore builds a store. fallback may be nil when a control plane is used.
func NewStore(fallback *Policy) *Store {
	s := &Store{byID: map[string]*Policy{}, byHost: map[string]*Policy{}, fallback: fallback}
	if fallback != nil {
		s.byID[fallback.ID] = fallback
		for _, d := range fallback.Domains {
			s.byHost[strings.ToLower(d)] = fallback
		}
	}
	return s
}

// Apply replaces the snapshot atomically.
func (s *Store) Apply(snap Snapshot) {
	byID := make(map[string]*Policy, len(snap.Projects))
	byHost := make(map[string]*Policy)
	var wildcard []*Policy
	for _, p := range snap.Projects {
		pol := NewPolicy(p)
		byID[pol.ID] = pol
		for _, d := range pol.Domains {
			d = strings.ToLower(strings.TrimSuffix(d, "."))
			if strings.HasPrefix(d, "*.") {
				wildcard = append(wildcard, pol)
			} else {
				byHost[d] = pol
			}
		}
	}
	s.mu.Lock()
	s.revision = snap.Revision
	s.byID = byID
	s.byHost = byHost
	s.wildcard = wildcard
	s.mu.Unlock()
}

// Revision returns the applied snapshot revision.
func (s *Store) Revision() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.revision
}

// Resolve picks a tenant by explicit project header (highest priority) or Host.
func (s *Store) Resolve(host, project string) (*Policy, bool) {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	// Strip a port if present.
	if i := strings.LastIndex(host, ":"); i > 0 && !strings.Contains(host[i:], "]") {
		host = host[:i]
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if project != "" {
		if p, ok := s.byID[project]; ok {
			return p, true
		}
		return nil, false
	}
	if p, ok := s.byHost[host]; ok {
		return p, true
	}
	for _, p := range s.wildcard {
		if p.MatchesDomain(host) {
			return p, true
		}
	}
	if s.fallback != nil {
		return s.fallback, true
	}
	return nil, false
}

// HasDomain reports whether host is a custom domain in the applied snapshot.
// It is used by the optional ACME host policy: only a domain the control plane
// currently lists may be issued a certificate. The env-mode fallback policy is
// included when it was built with explicit domains, but its allowlist is not:
// AllowedHost answers a different question (who may request images).
func (s *Store) HasDomain(host string) bool {
	host = normalizeHost(host)
	if host == "" {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.byHost[host]; ok {
		return true
	}
	for _, p := range s.wildcard {
		if p.MatchesDomain(host) {
			return true
		}
	}
	if s.fallback != nil && s.fallback.MatchesDomain(host) {
		return true
	}
	return false
}

// normalizeHost lowercases a host and removes a trailing dot and port.
func normalizeHost(host string) string {
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	if i := strings.LastIndex(host, ":"); i > 0 && !strings.Contains(host[i:], "]") {
		host = host[:i]
	}
	return strings.TrimSuffix(host, ".")
}

// Default returns the env-mode fallback policy, if any.
func (s *Store) Default() *Policy {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.fallback
}

// LoadOnce fetches and applies one snapshot.
func (s *Store) LoadOnce(ctx context.Context, client *Client) error {
	snap, notModified, err := client.FetchSnapshot(ctx, s.Revision())
	if err != nil {
		return err
	}
	if notModified {
		return nil
	}
	s.Apply(*snap)
	return nil
}

// Run polls the control plane until ctx is cancelled. It keeps the last good
// snapshot when the control plane is unavailable. Call LoadOnce first to make
// sure the service starts with a snapshot.
func (s *Store) Run(ctx context.Context, client *Client, interval time.Duration, logger *slog.Logger) {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	logger = logger.With("component", "tenant-sync")
	load := func() {
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		snap, notModified, err := client.FetchSnapshot(ctx, s.Revision())
		if err != nil {
			logger.Warn("snapshot fetch failed; keeping last known config", "error", err, "revision", s.Revision())
			return
		}
		if notModified {
			return
		}
		s.Apply(*snap)
		logger.Info("snapshot applied", "revision", snap.Revision, "projects", len(snap.Projects))
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			load()
		}
	}
}
