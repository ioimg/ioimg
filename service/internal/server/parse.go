package server

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"ioimg/internal/config"
	"ioimg/internal/ops"
	"ioimg/internal/sign"
)

// Target is a fully parsed and normalised image request.
type Target struct {
	Ops       ops.Ops
	Canonical string // canonical ops
	Source    string // canonical source URL
	Signature string
	Exp       int64
	Kid       string
}

// parseTarget accepts both path-based and query-based requests.
func parseTarget(r *http.Request, c config.Config) (Target, error) {
	if r.URL.Path == "/img" {
		return parseQueryTarget(r, c)
	}
	return parsePathTarget(r, c)
}

func parsePathTarget(r *http.Request, c config.Config) (Target, error) {
	p := strings.TrimPrefix(r.URL.Path, "/")
	idx := strings.Index(p, "https://")
	if i := strings.Index(p, "http://"); i >= 0 && (idx < 0 || i < idx) {
		idx = i
	}
	if idx < 0 {
		return Target{}, errInvalidSource()
	}
	prefix := p[:idx]
	source := p[idx:]
	if prefix == "" || !strings.HasSuffix(prefix, "/") {
		return Target{}, errInvalidSource()
	}
	prefix = strings.TrimSuffix(prefix, "/")
	parts := strings.Split(prefix, "/")

	var sig, opsRaw string
	switch len(parts) {
	case 1:
		opsRaw = parts[0]
	case 2:
		sig, opsRaw = parts[0], parts[1]
	default:
		return Target{}, errInvalidSource()
	}
	if opsRaw == "" {
		return Target{}, errInvalidOps("ops segment is empty")
	}

	// Path mode treats the request query string as part of the source URL,
	// except for the reserved exp/kid/sig parameters.
	q := r.URL.Query()
	exp, _ := strconv.ParseInt(q.Get("exp"), 10, 64)
	if v := q.Get("sig"); v != "" && sig == "" {
		sig = v
	}
	kid := q.Get("kid")
	q.Del("exp")
	q.Del("kid")
	q.Del("sig")
	if enc := q.Encode(); enc != "" {
		if strings.Contains(source, "?") {
			source += "&" + enc
		} else {
			source += "?" + enc
		}
	}
	return finishTarget(opsRaw, source, sig, exp, kid, c)
}

func parseQueryTarget(r *http.Request, c config.Config) (Target, error) {
	q := r.URL.Query()
	rawSource := q.Get("src")
	if rawSource == "" {
		return Target{}, errInvalidSource()
	}
	var parts []string
	order := []string{"w", "h", "c", "g", "f", "q", "dpr", "bg", "blur", "sharp", "rot", "flip", "trim", "strip", "enlarge", "palette", "t"}
	for _, k := range order {
		if v := q.Get(k); v != "" {
			parts = append(parts, k+"_"+v)
		}
	}
	if len(parts) == 0 {
		return Target{}, errInvalidOps("at least one transformation is required")
	}
	exp, _ := strconv.ParseInt(q.Get("exp"), 10, 64)
	return finishTarget(strings.Join(parts, ","), rawSource, q.Get("sig"), exp, q.Get("kid"), c)
}

func finishTarget(opsRaw, rawSource, sig string, exp int64, kid string, c config.Config) (Target, error) {
	o, err := ops.Parse(opsRaw, c.Presets)
	if err != nil {
		return Target{}, errInvalidOps(err.Error())
	}
	if err := validatePresets(o, c.Presets); err != nil {
		return Target{}, errInvalidOps(err.Error())
	}
	source, err := sign.CanonicalSource(rawSource)
	if err != nil {
		return Target{}, errInvalidSource()
	}
	if u, err := url.Parse(source); err == nil {
		if !c.AllowedHost(u.Hostname()) {
			return Target{}, errNotAllowed(u.Hostname())
		}
	}

	if c.SignedOnly && sig == "" {
		return Target{}, errInvalidSignature()
	}
	if sig != "" {
		if c.Signing == nil {
			return Target{}, errInvalidSignature()
		}
		if err := c.Signing.Verify(sig, o.Canonical(), source, exp); err != nil {
			switch err {
			case sign.ErrExpired:
				return Target{}, errSignatureExpired()
			case sign.ErrExpiryMissing:
				return Target{}, &HTTPError{Status: http.StatusUnauthorized, Code: "invalid_signature", Message: "exp is required for signed URLs"}
			default:
				return Target{}, errInvalidSignature()
			}
		}
	}
	return Target{Ops: o, Canonical: o.Canonical(), Source: source, Signature: sig, Exp: exp, Kid: kid}, nil
}

func validatePresets(o ops.Ops, presets map[string]string) error {
	if o.Preset == "" {
		return nil
	}
	if _, ok := presets[o.Preset]; !ok {
		return errInvalidOps("unknown preset " + o.Preset)
	}
	return nil
}
