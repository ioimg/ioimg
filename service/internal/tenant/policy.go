// Package tenant holds per-project configuration for the data plane.
package tenant

import (
	"strings"

	"ioimg/internal/ops"
	"ioimg/internal/sign"
)

// Limits are the soft quotas a project may enforce.
type Limits struct {
	MonthlyTransforms int64 `json:"monthlyTransforms"`
	Concurrency       int   `json:"concurrency"`
}

// Usage is the control plane's monthly counter snapshot.
type Usage struct {
	Period     string `json:"period"`
	Transforms int64  `json:"transforms"`
	BytesIn    int64  `json:"bytesIn"`
	BytesOut   int64  `json:"bytesOut"`
}

// SigningKey is one active HMAC key for a project.
type SigningKey struct {
	Kid    string `json:"kid"`
	Secret string `json:"secret"`
}

// Project is the control-plane representation of a tenant.
type Project struct {
	ID               string            `json:"id"`
	Name             string            `json:"name"`
	Domains          []string          `json:"domains"`
	Allowlist        []string          `json:"allowlist"`
	AllowAnyHost     bool              `json:"allowAnyHost"`
	Presets          map[string]string `json:"presets"`
	SignedOnly       bool              `json:"signedOnly"`
	ExpiryRequired   bool              `json:"expiryRequired"`
	FallbackRedirect bool              `json:"fallbackRedirect"`
	SigningKeys      []SigningKey      `json:"signingKeys"`
	Limits           Limits            `json:"limits"`
	Usage            Usage             `json:"usage"`
}

// Snapshot is the full configuration pulled by the data plane.
type Snapshot struct {
	Revision int64     `json:"revision"`
	Projects []Project `json:"projects"`
}

// Policy is the immutable per-request view of a project.
type Policy struct {
	ID               string
	Name             string
	Domains          []string
	Allowlist        []string
	AllowAnyHost     bool
	SignedOnly       bool
	ExpiryRequired   bool
	FallbackRedirect bool
	Presets          map[string]string
	Signing          *sign.Signer
	MonthlyLimit     int64
	Usage            Usage
}

// NewPolicy converts a project into a policy.
func NewPolicy(p Project) *Policy {
	pol := &Policy{
		ID:               p.ID,
		Name:             p.Name,
		Domains:          append([]string(nil), p.Domains...),
		Allowlist:        append([]string(nil), p.Allowlist...),
		AllowAnyHost:     p.AllowAnyHost,
		SignedOnly:       p.SignedOnly,
		ExpiryRequired:   p.ExpiryRequired,
		FallbackRedirect: p.FallbackRedirect,
		Presets:          p.Presets,
		MonthlyLimit:     p.Limits.MonthlyTransforms,
		Usage:            p.Usage,
	}
	if pol.Presets == nil {
		pol.Presets = map[string]string{}
	}
	if len(p.SigningKeys) > 0 {
		keys := make([]sign.Key, 0, len(p.SigningKeys))
		for _, k := range p.SigningKeys {
			keys = append(keys, sign.Key{ID: k.Kid, Secret: []byte(k.Secret)})
		}
		pol.Signing = &sign.Signer{
			Keys:           keys,
			IncludeExp:     true,
			ExpiryRequired: p.ExpiryRequired,
		}
	}
	return pol
}

// AllowedHost matches exact hosts and *.suffix wildcards.
func (p *Policy) AllowedHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if p.AllowAnyHost {
		return true
	}
	if len(p.Allowlist) == 0 {
		return false
	}
	for _, entry := range p.Allowlist {
		entry = strings.ToLower(entry)
		if entry == host {
			return true
		}
		if strings.HasPrefix(entry, "*.") {
			suffix := entry[1:]
			if strings.HasSuffix(host, suffix) && len(host) > len(suffix) {
				return true
			}
		}
	}
	return false
}

// MatchesDomain reports whether a request host belongs to this project.
func (p *Policy) MatchesDomain(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, d := range p.Domains {
		d = strings.ToLower(strings.TrimSuffix(d, "."))
		if d == host {
			return true
		}
		if strings.HasPrefix(d, "*.") {
			suffix := d[1:]
			if strings.HasSuffix(host, suffix) && len(host) > len(suffix) {
				return true
			}
		}
	}
	return false
}

// ParsePresets validates a preset map (used by the control plane before save).
func ParsePresets(raw map[string]string) (map[string]string, error) {
	if raw == nil {
		return map[string]string{}, nil
	}
	for name, expansion := range raw {
		if name == "" {
			return nil, errInvalidPreset("preset name cannot be empty")
		}
		if _, err := ops.Parse(expansion, nil); err != nil {
			return nil, errInvalidPreset("preset " + name + ": " + err.Error())
		}
	}
	out := make(map[string]string, len(raw))
	for k, v := range raw {
		out[k] = v
	}
	return out, nil
}

type presetError string

func (e presetError) Error() string { return string(e) }

func errInvalidPreset(msg string) error { return presetError(msg) }
