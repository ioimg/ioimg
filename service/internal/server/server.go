// Package server wires the HTTP data plane: URL parsing, caching, fetching,
// transforming, and the public error/header contract.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sync/singleflight"

	"ioimg/internal/cache"
	"ioimg/internal/config"
	"ioimg/internal/imaging"
	"ioimg/internal/ops"
	"ioimg/internal/purge"
	"ioimg/internal/source"
	"ioimg/internal/tenant"
)

// Server is the ioimg data plane.
type Server struct {
	cfg     config.Config
	fetcher *source.Fetcher
	cache   *cache.Disk
	metrics *Metrics
	log     *slog.Logger
	sf      singleflight.Group
	sem     chan struct{}
	limit   *limiter
	version string
	started time.Time

	formats map[ops.Format]bool
	proc    imaging.Processor

	l2      cache.L2
	purgers []purge.Purger
	jobs    *jobStore

	tenants            *tenant.Store
	defaultPolicy      *tenant.Policy
	control            *tenant.Client
	projectHeader      string
	allowProjectHeader bool
	usage              *usage
}

// Options are the optional Server dependencies (mainly for tests).
type Options struct {
	Logger  *slog.Logger
	Fetcher *source.Fetcher
	Cache   *cache.Disk
	Metrics *Metrics
	L2      cache.L2
	Purgers []purge.Purger
	Version string
}

// New builds a Server.
func New(cfg config.Config, o Options) (*Server, error) {
	logger := o.Logger
	if logger == nil {
		logger = slog.Default()
	}
	disk := o.Cache
	if disk == nil {
		d, err := cache.NewDisk(cfg.CacheDir, cfg.CacheMaxBytes)
		if err != nil {
			return nil, fmt.Errorf("open cache: %w", err)
		}
		disk = d
	}
	fetcher := o.Fetcher
	if fetcher == nil {
		fetcher = source.New(source.Options{
			Timeout:      cfg.SourceTimeout,
			MaxBytes:     cfg.SourceMaxBytes,
			AllowHost:    func(string) bool { return true },
			AllowPrivate: cfg.AllowPrivate,
			UserAgent:    cfg.SourceUserAgent,
		})
	}
	m := o.Metrics
	if m == nil {
		m = NewMetrics()
	}
	conc := cfg.Concurrency
	if conc == 0 {
		conc = runtime.NumCPU() * 4
	}
	version := o.Version
	if version == "" {
		version = "0.1.0-dev"
	}
	s := &Server{
		cfg:     cfg,
		fetcher: fetcher,
		cache:   disk,
		metrics: m,
		log:     logger,
		sem:     make(chan struct{}, conc),
		version: version,
		started: time.Now(),
		formats: imaging.AvailableFormats(),
		proc:    imaging.DefaultProcessor(),
	}
	s.l2 = o.L2
	if s.l2 == nil && cfg.L2Enabled {
		ctx, cancel := context.WithTimeout(context.Background(), cfg.L2.Timeout)
		l2, err := cache.NewS3(ctx, cache.S3Options{
			Endpoint:  cfg.L2.Endpoint,
			Bucket:    cfg.L2.Bucket,
			AccessKey: cfg.L2.AccessKey,
			SecretKey: cfg.L2.SecretKey,
			Region:    cfg.L2.Region,
			Prefix:    cfg.L2.Prefix,
			UseSSL:    cfg.L2.UseSSL,
			SecureSet: cfg.L2.UseSSLSet,
		})
		cancel()
		if err != nil {
			return nil, err
		}
		s.l2 = l2
	}
	s.purgers = o.Purgers
	if s.purgers == nil {
		s.purgers = buildPurgers(cfg.CDN)
	}
	s.jobs = newJobStore(100)
	s.usage = newUsage()
	s.projectHeader = cfg.ProjectHeader
	s.allowProjectHeader = cfg.AllowProjectHeader
	s.defaultPolicy = buildDefaultPolicy(cfg)
	s.tenants = tenant.NewStore(s.defaultPolicy)
	if cfg.ControlURL != "" {
		s.tenants = tenant.NewStore(nil)
		s.control = tenant.NewClient(cfg.ControlURL, cfg.ControlToken, cfg.ControlTimeout)
		ctx, cancel := context.WithTimeout(context.Background(), cfg.ControlTimeout)
		err := s.tenants.LoadOnce(ctx, s.control)
		cancel()
		if err != nil {
			logger.Warn("initial control-plane snapshot failed; waiting for sync", "error", err)
		}
		go s.tenants.Run(context.Background(), s.control, cfg.ControlSync, logger)
		go s.usageLoop(context.Background())
	}

	if cfg.DisableAVIF {
		delete(s.formats, ops.FormatAVIF)
	}
	if cfg.DisableWebP {
		delete(s.formats, ops.FormatWebP)
	}
	if rps := envFloat("IOIMG_RATE_RPS", 0); rps > 0 {
		s.limit = newLimiter(rps, envInt("IOIMG_RATE_BURST", 0))
	}
	return s, nil
}

// Handler returns the HTTP handler.
//
// Routing is intentionally not built on http.ServeMux: ServeMux cleans paths
// and redirects "//" to "/", which corrupts the source URL embedded in
// /{sig}/{ops}/https://host/path. The switch below keeps the raw path intact.
func (s *Server) Handler() http.Handler {
	return s.withRecovery(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = w.Write([]byte("ok"))
		case "/readyz":
			s.handleReady(w, r)
		case "/metrics":
			s.handleMetrics(w, r)
		case "/v1/stats":
			s.handleStats(w, r)
		case "/v1/purge":
			if r.Method != http.MethodPost {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			s.handlePurge(w, r)
		case "/v1/warm":
			if r.Method != http.MethodPost {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			s.handleWarm(w, r)
		default:
			if strings.HasPrefix(r.URL.Path, "/v1/jobs/") {
				s.handleJob(w, r)
				return
			}
			s.handleImage(w, r)
		}
	}))
}

func (s *Server) withRecovery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(base http.ResponseWriter, r *http.Request) {
		w := &responseWriter{ResponseWriter: base, status: 200}
		start := time.Now()
		defer func() {
			if rec := recover(); rec != nil {
				s.log.Error("panic serving request", "path", r.URL.Path, "panic", rec)
				if !headerWritten(w) {
					s.writeError(w, r, errInternal(fmt.Errorf("panic")))
				}
			}
			if s.cfg.AccessLog && !strings.HasPrefix(r.URL.Path, "/healthz") {
				s.log.Info("access", "method", r.Method, "path", r.URL.Path,
					"status", statusOf(w), "duration_ms", float64(time.Since(start).Microseconds())/1000)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleImage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		s.writeError(w, r, &HTTPError{Status: http.StatusMethodNotAllowed, Code: "method_not_allowed", Message: "only GET and HEAD are supported"})
		return
	}
	rid := requestID(r)
	w.Header().Set("X-Request-Id", rid)
	s.metrics.Requests.Add(1)

	if !s.limit.allow(clientIP(r)) {
		s.writeError(w, r, errRateLimited())
		return
	}

	policy, perr := s.policyFor(r)
	if perr != nil {
		s.writeError(w, r, perr)
		return
	}
	target, err := parseTarget(r, policy)
	if err != nil {
		s.writeError(w, r, asHTTPError(err))
		return
	}
	if remaining := s.quotaRemaining(policy); remaining >= 0 {
		w.Header().Set("X-IOIMG-Quota-Remaining", strconv.FormatInt(remaining, 10))
		if remaining == 0 {
			s.writeError(w, r, errQuotaExceeded())
			return
		}
	}

	format := target.Ops.Format
	if (format == ops.FormatAVIF && s.cfg.DisableAVIF) || (format == ops.FormatWebP && s.cfg.DisableWebP) {
		s.writeError(w, r, errUnsupportedFormat(string(format)))
		return
	}
	auto := format == ops.FormatAuto
	if auto {
		format = ops.Negotiate(r.Header.Get("Accept"), s.formats, false)
	}
	key := cache.Key(config.CacheKeyVersion+"|"+s.version+"|"+policy.ID, target.Canonical, target.Source, string(format))
	etag := `"` + key + `"`

	h := w.Header()
	h.Set("ETag", etag)
	h.Set("X-IOIMG-Format", string(format))
	if auto {
		h.Set("Vary", "Accept")
	}
	h.Set("Cache-Tag", "project:"+policy.ID+","+cacheTag(target.Source))
	if format == ops.FormatJSON {
		h.Set("Cache-Control", "public, max-age=60")
	} else {
		h.Set("Cache-Control", "public, max-age=31536000, immutable")
	}
	h.Set("X-IOIMG-Backend", "go")
	if target.Ops.Clamped() {
		h.Set("X-IOIMG-Clamped", "1")
	}

	if inm := r.Header.Get("If-None-Match"); inm != "" && etagMatches(inm, etag) {
		h.Set("X-IOIMG-Cache", "HIT-L1")
		w.WriteHeader(http.StatusNotModified)
		return
	}

	// Cache hit: no fetch, no transform, no worker slot.
	if data, mod, ok := s.cache.Get(key); ok {
		s.metrics.Hits.Add(1)
		h.Set("X-IOIMG-Cache", "HIT-L1")
		h.Set("Last-Modified", mod.UTC().Format(http.TimeFormat))
		s.writeBody(w, r, format, data)
		return
	}
	s.metrics.Misses.Add(1)

	select {
	case s.sem <- struct{}{}:
		defer func() { <-s.sem }()
	default:
		s.writeError(w, r, errOverloaded())
		return
	}

	type result struct {
		data    []byte
		timings map[string]float64
		hit     bool
		l2      bool
		mod     time.Time
	}
	v, err, _ := s.sf.Do(key, func() (any, error) {
		if data, mod, ok := s.cache.Get(key); ok {
			return result{data: data, hit: true, mod: mod}, nil
		}
		if s.l2 != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			data, ok, l2err := s.l2.Get(ctx, key)
			cancel()
			if l2err != nil {
				s.log.Warn("l2 read failed", "error", l2err)
			} else if ok {
				_ = s.cache.Put(key, data)
				return result{data: data, hit: true, l2: true, mod: time.Now()}, nil
			}
		}
		data, timings, err := s.render(r.Context(), target, format)
		if err != nil {
			return nil, err
		}
		if err := s.cache.Put(key, data); err != nil {
			s.log.Warn("cache write failed", "error", err)
		}
		if s.l2 != nil {
			copy := append([]byte(nil), data...)
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				if err := s.l2.Put(ctx, key, copy); err != nil {
					s.log.Warn("l2 write failed", "error", err)
				}
			}()
		}
		return result{data: data, timings: timings}, nil
	})
	if err != nil {
		s.writeError(w, r, asHTTPError(err))
		return
	}
	res := v.(result)
	if !res.hit {
		s.countUsage(policy.ID, int64(len(res.data)))
	}
	state := "MISS"
	if res.hit {
		if res.l2 {
			state = "HIT-L2"
		} else {
			state = "HIT-L1"
		}
		s.metrics.Hits.Add(1)
		if res.l2 {
			s.metrics.L2Hits.Add(1)
		}
	} else {
		s.metrics.BytesOut.Add(uint64(len(res.data)))
		if res.mod.IsZero() {
			res.mod = time.Now()
		}
		h.Set("Last-Modified", res.mod.UTC().Format(http.TimeFormat))
	}
	h.Set("X-IOIMG-Cache", state)
	if res.timings != nil {
		h.Set("Server-Timing", serverTiming(res.timings))
		h.Set("X-IOIMG-Transform-Ms", strconv.FormatFloat(res.timings["total"], 'f', 2, 64))
	}
	s.writeBody(w, r, format, res.data)
}

// render fetches, decodes, transforms, and encodes one image.
func (s *Server) render(ctx context.Context, t Target, format ops.Format) ([]byte, map[string]float64, error) {
	timings := map[string]float64{}
	total := time.Now()

	fetchStart := time.Now()
	src, err := s.fetcher.Fetch(ctx, t.Source)
	if err != nil {
		return nil, timings, mapSourceError(err)
	}
	s.metrics.FetchNs.Add(uint64(time.Since(fetchStart).Nanoseconds()))
	s.metrics.BytesIn.Add(uint64(len(src.Body)))
	timings["fetch"] = ms(fetchStart)

	procStart := time.Now()
	res, err := s.proc.Process(src.Body, t.Ops, imaging.Options{OutFormat: format, Quality: t.Ops.Quality})
	if err != nil {
		if errors.Is(err, imaging.ErrDimension) {
			return nil, timings, errDimension()
		}
		if errors.Is(err, imaging.ErrDecode) {
			return nil, timings, errDecode()
		}
		return nil, timings, errInternal(err)
	}
	s.metrics.TransformNs.Add(uint64(time.Since(procStart).Nanoseconds()))
	for k, v := range res.Timings {
		timings[k] = v
	}

	if format == ops.FormatJSON {
		payload, err := json.Marshal(map[string]any{
			"source":        t.Source,
			"width":         res.Info.Width,
			"height":        res.Info.Height,
			"format":        res.Info.Format,
			"bytes":         res.Info.Bytes,
			"hasAlpha":      res.Info.HasAlpha,
			"isAnimated":    res.Info.Animated,
			"dominantColor": res.DominantColor,
			"exif":          map[string]any{"orientation": 1},
		})
		if err != nil {
			return nil, timings, errInternal(err)
		}
		timings["total"] = ms(total)
		return payload, timings, nil
	}
	timings["total"] = ms(total)
	return res.Data, timings, nil
}

func (s *Server) writeBody(w http.ResponseWriter, r *http.Request, format ops.Format, data []byte) {
	w.Header().Set("Content-Type", imaging.ContentType(format))
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func (s *Server) writeError(w http.ResponseWriter, r *http.Request, he *HTTPError) {
	s.metrics.RecordError(he.Code)
	s.log.Warn("request failed", "code", he.Code, "status", he.Status, "path", r.URL.Path, "request_id", r.Header.Get("X-Request-Id"))

	// Optional browser fallback: redirect to the original source.
	if he.Status >= 400 && strings.HasPrefix(r.Header.Get("Accept"), "image/") {
		policy, _ := s.policyFor(r)
		if policy != nil && policy.FallbackRedirect {
			target, err := parseTarget(r, policy)
			if err == nil {
				w.Header().Set("Cache-Control", "no-store")
				http.Redirect(w, r, target.Source, http.StatusFound)
				return
			}
		}
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if he.Retryable {
		w.Header().Set("Retry-After", "1")
	}
	w.WriteHeader(he.Status)
	body := map[string]any{
		"error": map[string]any{
			"code":       he.Code,
			"message":    he.Message,
			"request_id": requestID(r),
			"docs":       "https://ioimg.dev/docs/errors#" + he.Code,
		},
	}
	_ = json.NewEncoder(w).Encode(body)
}

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	// The L1 cache must be writable for readiness.
	probe := filepath.Join(s.cfg.CacheDir, ".ready")
	if err := os.WriteFile(probe, []byte("ok"), 0o644); err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("cache not writable: " + err.Error()))
		return
	}
	_ = os.Remove(probe)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ready"))
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if s.cfg.MetricsToken != "" && !constantTimeEqual(bearerToken(r), s.cfg.MetricsToken) {
		s.writeError(w, r, &HTTPError{Status: http.StatusUnauthorized, Code: "unauthorized", Message: "metrics token required"})
		return
	}
	entries, bytes := s.cache.Stats()
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	_, _ = w.Write([]byte(s.metrics.Render(entries, bytes)))
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	if s.cfg.MetricsToken != "" && !constantTimeEqual(bearerToken(r), s.cfg.MetricsToken) {
		s.writeError(w, r, &HTTPError{Status: http.StatusUnauthorized, Code: "unauthorized", Message: "metrics token required"})
		return
	}
	entries, bytes := s.cache.Stats()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"version":       s.version,
		"uptimeSeconds": int(time.Since(s.started).Seconds()),
		"requests":      s.metrics.Requests.Load(),
		"hits":          s.metrics.Hits.Load(),
		"misses":        s.metrics.Misses.Load(),
		"errors":        s.metrics.Errors.Load(),
		"bytesIn":       s.metrics.BytesIn.Load(),
		"bytesOut":      s.metrics.BytesOut.Load(),
		"cacheEntries":  entries,
		"cacheBytes":    bytes,
		"goroutines":    runtime.NumGoroutine(),
	})
}

func cacheTag(sourceURL string) string {
	h := cache.Key("tag", "", sourceURL, "")
	return "src:" + h[:12]
}

func serverTiming(t map[string]float64) string {
	var parts []string
	for _, k := range []string{"fetch", "decode", "transform", "encode"} {
		if v, ok := t[k]; ok {
			parts = append(parts, fmt.Sprintf("%s;dur=%.1f", k, v))
		}
	}
	return strings.Join(parts, ", ")
}

func etagMatches(header, etag string) bool {
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		part = strings.TrimPrefix(part, "W/")
		if part == etag || part == "*" {
			return true
		}
	}
	return false
}

func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" && trustProxy() {
		if i := strings.Index(xff, ","); i > 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func trustProxy() bool {
	return strings.EqualFold(os.Getenv("IOIMG_TRUST_PROXY"), "true")
}

func mapSourceError(err error) error {
	switch {
	case errors.Is(err, source.ErrNotAllowed):
		return errNotAllowed("")
	case errors.Is(err, source.ErrPrivateAddr):
		return errPrivate()
	case errors.Is(err, source.ErrNotFound):
		return errNotFound()
	case errors.Is(err, source.ErrTooLarge):
		return errTooLarge()
	case errors.Is(err, source.ErrTimeout):
		return errTimeout()
	case errors.Is(err, source.ErrInvalidSource):
		return errInvalidSource()
	default:
		return errOrigin(err.Error())
	}
}

func ms(start time.Time) float64 {
	return float64(time.Since(start).Microseconds()) / 1000
}

func statusOf(w http.ResponseWriter) int {
	type statuser interface{ Status() int }
	if s, ok := w.(statuser); ok {
		return s.Status()
	}
	return 0
}

func headerWritten(w http.ResponseWriter) bool {
	type written interface{ Written() bool }
	if x, ok := w.(written); ok {
		return x.Written()
	}
	return false
}

func envFloat(k string, d float64) float64 {
	if v := os.Getenv(k); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return d
}

func envInt(k string, d int) int {
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return d
}

// policyFor resolves the tenant for a request. In env mode the default
// policy is always returned; in control-plane mode an unknown host/project
// is rejected instead of falling back to another tenant.
func (s *Server) policyFor(r *http.Request) (*tenant.Policy, *HTTPError) {
	project := ""
	if s.allowProjectHeader {
		project = r.Header.Get(s.projectHeader)
	}
	p, ok := s.tenants.Resolve(r.Host, project)
	if !ok {
		return nil, errProjectNotFound()
	}
	return p, nil
}

func buildDefaultPolicy(cfg config.Config) *tenant.Policy {
	p := tenant.Project{
		ID:               "default",
		Name:             "default",
		Domains:          nil,
		Allowlist:        cfg.Allowlist,
		AllowAnyHost:     cfg.AllowAnyHost,
		Presets:          cfg.Presets,
		SignedOnly:       cfg.SignedOnly,
		FallbackRedirect: cfg.FallbackRedirect,
	}
	if cfg.Signing != nil {
		p.ExpiryRequired = cfg.Signing.ExpiryRequired
		for _, k := range cfg.Signing.Keys {
			p.SigningKeys = append(p.SigningKeys, tenant.SigningKey{Kid: k.ID, Secret: string(k.Secret)})
		}
	}
	return tenant.NewPolicy(p)
}
