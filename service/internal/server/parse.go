package server

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/ioimg/ioimg/service/pkg/ops"
	"github.com/ioimg/ioimg/service/pkg/sign"
	"github.com/ioimg/ioimg/service/pkg/tenant"
)

// Target is a fully parsed and normalised image request.
type Target struct {
	Ops       ops.Ops
	Canonical string // canonical ops
	Source    string // canonical source URL
	Signature string
	Exp       int64
	Kid       string
	// SourceKeyID is the signing-key id that decrypted an es_ source. It is
	// only exposed in a debug response header, never in the cache key.
	SourceKeyID string
}

// parseTarget accepts both path-based and query-based requests.
func parseTarget(r *http.Request, p *tenant.Policy) (Target, error) {
	if r.URL.Path == "/img" {
		return parseQueryTarget(r, p)
	}
	return parsePathTarget(r, p)
}

func parsePathTarget(r *http.Request, p *tenant.Policy) (Target, error) {
	path := strings.TrimPrefix(r.URL.Path, "/")
	idx := strings.Index(path, "https://")
	if i := strings.Index(path, "http://"); i >= 0 && (idx < 0 || i < idx) {
		idx = i
	}
	if idx < 0 {
		// es_ sources carry no scheme, so locate the encrypted segment instead.
		// A plain HTTP(S) source always wins above, which keeps existing URLs
		// (including ones whose path contains "/es_") byte-for-byte compatible.
		if strings.HasPrefix(path, "es_") {
			idx = 0
		} else if i := strings.Index(path, "/es_"); i >= 0 {
			idx = i + 1
		}
	}
	if idx < 0 {
		return Target{}, errInvalidSource()
	}
	prefix := path[:idx]
	source := path[idx:]
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
	// An es_ token is the whole source; its plaintext query string was sealed
	// by the token creator. Append only for plain sources so exp/sig handled
	// above cannot corrupt the ciphertext.
	if !strings.HasPrefix(source, "es_") {
		if enc := q.Encode(); enc != "" {
			if strings.Contains(source, "?") {
				source += "&" + enc
			} else {
				source += "?" + enc
			}
		}
	}
	return finishTarget(opsRaw, source, sig, exp, kid, p)
}

func parseQueryTarget(r *http.Request, p *tenant.Policy) (Target, error) {
	q := r.URL.Query()
	rawSource := q.Get("src")
	if rawSource == "" {
		return Target{}, errInvalidSource()
	}
	var parts []string
	order := []string{
		"w", "h", "c", "g", "f", "q", "dpr", "bg", "blur", "sharp", "rot", "flip",
		"trim", "strip", "enlarge", "palette", "skp", "skip_processing", "raw", "t",
		"wm", "watermark", "wmu", "watermark_url",
		"wmt", "watermark_text", "wms", "watermark_size",
		"wmr", "wm_rot", "watermark_rotate", "wmsh", "watermark_shadow",
	}
	for _, k := range order {
		if v := q.Get(k); v != "" {
			parts = append(parts, k+"_"+v)
		}
	}
	if len(parts) == 0 {
		return Target{}, errInvalidOps("at least one transformation is required")
	}
	exp, _ := strconv.ParseInt(q.Get("exp"), 10, 64)
	return finishTarget(strings.Join(parts, ","), rawSource, q.Get("sig"), exp, q.Get("kid"), p)
}

func finishTarget(opsRaw, rawSource, sig string, exp int64, kid string, p *tenant.Policy) (Target, error) {
	o, err := ops.Parse(opsRaw, p.Presets)
	if err != nil {
		return Target{}, errInvalidOps(err.Error())
	}
	sourceKeyID := ""
	if strings.HasPrefix(rawSource, "es_") {
		if p.SourceCipher == nil {
			return Target{}, errEncryptedSource("encrypted source requires IOIMG_SIGNING_KEYS")
		}
		plain, usedKey, derr := p.SourceCipher.Decrypt(rawSource)
		if derr != nil {
			switch {
			case errors.Is(derr, sign.ErrUnknownSourceKey):
				return Target{}, errEncryptedSource(derr.Error())
			case errors.Is(derr, sign.ErrNoSourceKeys):
				return Target{}, errEncryptedSource("encrypted source requires IOIMG_SIGNING_KEYS")
			default:
				return Target{}, errEncryptedSource("encrypted source is invalid or has been tampered with")
			}
		}
		// The decrypted URL is deliberately not trusted: it goes through the
		// same canonicalisation and allowlist checks as a plain source below.
		rawSource, sourceKeyID = plain, usedKey
	}
	source, err := sign.CanonicalSource(rawSource)
	if err != nil {
		return Target{}, errInvalidSource()
	}
	if u, err := url.Parse(source); err == nil {
		if !p.AllowedHost(u.Hostname()) {
			return Target{}, errNotAllowed(u.Hostname())
		}
	}

	if p.SignedOnly && sig == "" {
		return Target{}, errInvalidSignature()
	}
	if sig != "" {
		if p.Signing == nil {
			return Target{}, errInvalidSignature()
		}
		if err := p.Signing.Verify(sig, o.SigningCanonical(), source, exp); err != nil {
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
	return Target{Ops: o, Canonical: o.Canonical(), Source: source, Signature: sig, Exp: exp, Kid: kid, SourceKeyID: sourceKeyID}, nil
}
