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
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/ioimg/ioimg/service/internal/cache"
	"github.com/ioimg/ioimg/service/internal/config"
	"github.com/ioimg/ioimg/service/internal/imaging"
	"github.com/ioimg/ioimg/service/internal/purge"
	"github.com/ioimg/ioimg/service/internal/source"
	"github.com/ioimg/ioimg/service/pkg/ops"
	"github.com/ioimg/ioimg/service/pkg/tenant"
)

// Server is the ioimg data plane.
type Server struct {
	cfg          config.Config
	fetcher      *source.Fetcher
	cache        *cache.Disk
	metrics      *Metrics
	log          *slog.Logger
	sf           singleflight.Group
	sem          chan struct{}
	gate         *projectGate
	limit        *limiter
	projectLimit *projectLimiter
	version      string
	started      time.Time

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
	replicaID          string
	usage              *usage
	leases             *leaseBudget
	watermarks         *watermarkCache
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
	if imaging.Backend() != "vips" {
		logger.Warn("pure-Go imaging backend active: transforms are several times slower; build with -tags vips for production")
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
			Timeout:            cfg.SourceTimeout,
			MaxBytes:           cfg.SourceMaxBytes,
			RawMaxBytes:        cfg.SourceRawMaxBytes,
			AllowHost:          func(string) bool { return true },
			AllowPrivate:       cfg.AllowPrivate,
			UserAgent:          cfg.SourceUserAgent,
			SourceAuth:         cfg.SourceAuth,
			PassthroughHeaders: cfg.PassthroughHeaders,
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
		cfg:          cfg,
		fetcher:      fetcher,
		cache:        disk,
		metrics:      m,
		log:          logger,
		sem:          make(chan struct{}, conc),
		gate:         newProjectGate(),
		projectLimit: newProjectLimiter(),
		version:      version,
		started:      time.Now(),
		formats:      imaging.AvailableFormats(),
		proc:         imaging.DefaultProcessor(),
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
	s.projectHeader = cfg.ProjectHeader
	s.replicaID = cfg.ReplicaID
	if s.replicaID == "" {
		s.replicaID = defaultReplicaID()
	}
	s.usage = newUsage(s.replicaID)
	s.leases = newLeaseBudget()
	s.watermarks = &watermarkCache{}
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
	return s.withRecovery(http.HandlerFunc(s.dispatch))
}

// dispatch routes one request. Every transport (net/http, celeris, fiber) calls
// it, so the operational endpoints behave the same on all of them.
// dispatch is the single entry point every transport adapter calls. Panic
// recovery lives here rather than in each adapter: celeris and fiber call
// dispatch directly, so a panic anywhere below would otherwise take the whole
// process down instead of failing one request. Errors stay loud: the panic is
// logged with its stack and counted, and the client gets a structured 500 when
// nothing has been written yet.
func (s *Server) dispatch(w http.ResponseWriter, r *http.Request) {
	ow := &onceWriter{inner: w}
	defer func() {
		rec := recover()
		if rec == nil {
			return
		}
		s.log.Error("panic serving request",
			"method", r.Method, "path", r.URL.Path, "panic", rec, "stack", string(debug.Stack()))
		if headerWritten(ow) {
			// Headers or body already went out; the only honest option left is a
			// logged, counted failure with a truncated response.
			s.metrics.RecordError("panic_after_write")
			return
		}
		s.writeError(ow, r, errInternal(fmt.Errorf("panic")))
	}()
	s.route(ow, r)
}

// onceWriter reports whether anything was written, so recovery can decide
// between a structured error and a truncation.
type onceWriter struct {
	inner http.ResponseWriter
	wrote bool
}

func (o *onceWriter) Header() http.Header         { return o.inner.Header() }
func (o *onceWriter) WriteHeader(code int)        { o.wrote = true; o.inner.WriteHeader(code) }
func (o *onceWriter) Write(b []byte) (int, error) { o.wrote = true; return o.inner.Write(b) }
func (o *onceWriter) Written() bool               { return o.wrote }

func (s *Server) route(w http.ResponseWriter, r *http.Request) {
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
	// The project rate limit is admission control, not work: it must run before
	// the concurrency gate, any cache probe, and any origin fetch. Unlimited
	// projects take the first branch and never allocate a bucket.
	if decision := s.projectLimit.allow(policy.ID, policy.RateRPS, policy.RateBurst); !decision.Allowed {
		s.metrics.RecordProjectRateLimited(policy.ID)
		h := w.Header()
		h.Set("Retry-After", strconv.Itoa(ceilSeconds(decision.RetryAfter)))
		h.Set("X-IOIMG-RateLimit-Limit", strconv.Itoa(decision.Limit))
		h.Set("X-IOIMG-RateLimit-Remaining", strconv.Itoa(decision.Remaining))
		h.Set("X-IOIMG-RateLimit-Reset", strconv.Itoa(ceilSeconds(decision.Reset)))
		s.writeError(w, r, errRateLimited())
		return
	} else if decision.Limit > 0 {
		h := w.Header()
		h.Set("X-IOIMG-RateLimit-Limit", strconv.Itoa(decision.Limit))
		h.Set("X-IOIMG-RateLimit-Remaining", strconv.Itoa(decision.Remaining))
		h.Set("X-IOIMG-RateLimit-Reset", strconv.Itoa(ceilSeconds(decision.Reset)))
	}
	target, err := parseTarget(r, policy)
	if err != nil {
		s.writeError(w, r, asHTTPError(err))
		return
	}
	reservation := s.acquireQuota(policy)
	if !reservation.allowed && reservation.needLease {
		// Out of lease units: wait for one control-plane grant instead of
		// failing the request that happens to arrive first.
		reservation = s.ensureLease(policy, reservation)
	}
	if !reservation.allowed {
		if s.leases != nil {
			id, amount, unspent, expires, refreshing := s.leases.debugState(policy.ID)
			s.log.Warn("quota refused", "project", policy.ID, "limit", policy.MonthlyLimit,
				"snapshot_used", policy.Usage.Transforms, "local_used", s.usage.totalFor(policy.ID),
				"lease_id", id, "lease_amount", amount, "lease_unspent", unspent,
				"lease_expires_in", time.Until(expires).String(), "lease_refreshing", refreshing, "lease_refresh_age", s.leases.refreshAge(policy.ID),
				"need_lease", reservation.needLease, "need_refresh", reservation.needRefresh)
		}
		if reservation.remaining >= 0 {
			w.Header().Set("X-IOIMG-Quota-Remaining", "0")
		}
		s.writeError(w, r, errQuotaExceeded())
		return
	}
	if reservation.remaining >= 0 {
		w.Header().Set("X-IOIMG-Quota-Remaining", strconv.FormatInt(reservation.remaining, 10))
	}
	defer reservation.release()
	s.maybeRefreshLease(policy, reservation)

	format := target.Ops.Format
	if (format == ops.FormatAVIF && s.cfg.DisableAVIF) || (format == ops.FormatWebP && s.cfg.DisableWebP) {
		s.writeError(w, r, errUnsupportedFormat(string(format)))
		return
	}
	auto := format == ops.FormatAuto
	if auto {
		format = ops.Negotiate(r.Header.Get("Accept"), s.formats, false)
	}
	best := format == ops.FormatBest
	key := cache.Key(config.CacheKeyVersion+"|"+s.version+"|"+policy.ID, target.Canonical, target.Source, string(format))

	h := w.Header()
	s.applyResponseHeaders(h)
	if target.SourceKeyID != "" && s.cfg.LogLevel == slog.LevelDebug {
		// Key ids are rotation metadata, not secrets, but only reveal which one
		// decrypted the source when debug logging was explicitly enabled.
		h.Set("X-IOIMG-Source-Key", target.SourceKeyID)
		s.log.Debug("encrypted source decrypted", "key_id", target.SourceKeyID)
	}
	if !best {
		h.Set("ETag", etagFor(key, format, false))
	}
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

	if inm := r.Header.Get("If-None-Match"); inm != "" && !best && etagMatches(inm, etagFor(key, format, false)) {
		h.Set("X-IOIMG-Cache", "HIT-L1")
		w.WriteHeader(http.StatusNotModified)
		return
	}

	// Cache hit: no fetch, no transform, no worker slot.
	if data, mod, ok := s.cache.Get(key); ok {
		s.metrics.Hits.Add(1)
		chosen := concreteFormat(format, data)
		if best {
			h.Set("X-IOIMG-Format", string(chosen))
			h.Set("ETag", etagFor(key, chosen, true))
			if inm := r.Header.Get("If-None-Match"); inm != "" && etagMatches(inm, etagFor(key, chosen, true)) {
				h.Set("X-IOIMG-Cache", "HIT-L1")
				w.WriteHeader(http.StatusNotModified)
				return
			}
		}
		h.Set("X-IOIMG-Cache", "HIT-L1")
		h.Set("Last-Modified", mod.UTC().Format(http.TimeFormat))
		s.writeBody(w, r, chosen, target.Ops.Filename, data, target.Ops)
		return
	}
	// Per-project admission first: a noisy neighbour must not consume the shared
	// worker pool (or fail because someone else's project filled it).
	if !s.gate.acquire(policy.ID, policy.Concurrency) {
		s.metrics.Concurrency.Add(1)
		w.Header().Set("Retry-After", "1")
		s.writeError(w, r, errConcurrencyExceeded())
		return
	}
	defer s.gate.release(policy.ID)

	select {
	case s.sem <- struct{}{}:
		defer func() { <-s.sem }()
	default:
		s.writeError(w, r, errOverloaded())
		return
	}

	type result struct {
		data    []byte
		format  ops.Format
		timings map[string]float64
		hit     bool
		l2      bool
		mod     time.Time
	}
	v, err, shared := s.sf.Do(key, func() (any, error) {
		if data, mod, ok := s.cache.Get(key); ok {
			return result{data: data, format: concreteFormat(format, data), hit: true, mod: mod}, nil
		}
		if s.l2 != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			data, ok, l2err := s.l2.Get(ctx, key)
			cancel()
			if l2err != nil {
				s.log.Warn("l2 read failed", "error", l2err)
			} else if ok {
				_ = s.cache.Put(key, data)
				return result{data: data, format: concreteFormat(format, data), hit: true, l2: true, mod: time.Now()}, nil
			}
		}
		data, timings, chosen, err := s.render(r.Context(), target, format, r.Header)
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
		return result{data: data, format: chosen, timings: timings}, nil
	})
	if err != nil {
		s.writeError(w, r, asHTTPError(err))
		return
	}
	res := v.(result)
	// Metrics are recorded where the work actually happened: a request that
	// joined an in-flight render is a hit, not a second miss.
	if shared {
		s.metrics.Coalesced.Add(1)
	}
	if !res.hit {
		s.countUsageLease(policy.ID, reservation.spend(), int64(len(res.data)))
		s.metrics.Misses.Add(1)
	}
	s.metrics.BytesOut.Add(uint64(len(res.data)))
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
		if res.mod.IsZero() {
			res.mod = time.Now()
		}
		h.Set("Last-Modified", res.mod.UTC().Format(http.TimeFormat))
	}
	h.Set("X-IOIMG-Cache", state)
	chosen := res.format
	if chosen == "" || chosen == ops.FormatBest {
		chosen = concreteFormat(format, res.data)
	}
	if best {
		h.Set("X-IOIMG-Format", string(chosen))
		h.Set("ETag", etagFor(key, chosen, true))
		if inm := r.Header.Get("If-None-Match"); inm != "" && etagMatches(inm, etagFor(key, chosen, true)) {
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}
	if res.timings != nil {
		if _, ok := res.timings["fallback"]; ok {
			h.Set("X-IOIMG-Fallback", "1")
		}
		if _, ok := res.timings["avif_subsample_fallback"]; ok {
			h.Set("X-IOIMG-AVIF-Subsample-Fallback", "1")
		}
		h.Set("Server-Timing", serverTiming(res.timings))
		h.Set("X-IOIMG-Transform-Ms", strconv.FormatFloat(res.timings["total"], 'f', 2, 64))
	}
	s.writeBody(w, r, chosen, target.Ops.Filename, res.data, target.Ops)
}

// concreteFormat resolves f_best to the winner. For a cache hit only the
// encoded bytes are available, which is enough to derive the media type.
func concreteFormat(requested ops.Format, data []byte) ops.Format {
	if requested != ops.FormatBest {
		return requested
	}
	if f := imaging.FormatFromBytes(data); f != "" {
		return f
	}
	return ops.FormatJPEG
}

// etagFor keeps the cache key as the identity and, for f_best, appends the
// winning format so a changed winner cannot be revalidated as the old one.
func etagFor(key string, format ops.Format, best bool) string {
	if best {
		return `"` + key + "-" + string(format) + `"`
	}
	return `"` + key + `"`
}

// skipApplies reports whether skp_ matches both the probed source format and
// the requested output format. jpg/jpeg are treated as aliases.
func skipApplies(list []string, sourceFormat string, out ops.Format) bool {
	want := normalizeSkipExtension(string(out))
	if normalizeSkipExtension(sourceFormat) != want {
		return false
	}
	for _, ext := range list {
		if normalizeSkipExtension(ext) == want {
			return true
		}
	}
	return false
}

func normalizeSkipExtension(ext string) string {
	ext = strings.ToLower(ext)
	if ext == "jpg" {
		return "jpeg"
	}
	return ext
}

// render fetches, decodes, transforms, and encodes one image.
func (s *Server) render(ctx context.Context, t Target, format ops.Format, reqHeaders http.Header) ([]byte, map[string]float64, ops.Format, error) {
	timings := map[string]float64{}
	total := time.Now()

	fetchStart := time.Now()
	// raw_ is allowed past the normal source size guard (imgproxy treats raw
	// sources as unchecked); it is still bounded by the raw ceiling, because the
	// body is buffered here. skp_ keeps the normal limit: it only skips the
	// transform, not the source checks.
	fetch := func(ctx context.Context, raw string) (*source.Result, error) {
		return s.fetcher.FetchWithHeaders(ctx, raw, reqHeaders)
	}
	if t.Ops.Raw {
		fetch = func(ctx context.Context, raw string) (*source.Result, error) {
			return s.fetcher.FetchRawWithHeaders(ctx, raw, reqHeaders)
		}
	}
	src, err := fetch(ctx, t.Source)
	usedFallback := false
	if err != nil {
		// fiu_: a per-request fallback image is served when the source cannot be
		// fetched. It is fetched through the same SSRF-guarded fetcher, so a
		// fallback URL cannot reach anything the source could not.
		if t.Ops.FallbackURL == "" {
			return nil, timings, format, mapSourceError(err)
		}
		s.metrics.Fallback.Add(1)
		fallback, ferr := fetch(ctx, t.Ops.FallbackURL)
		if ferr != nil {
			s.log.Warn("fallback fetch failed", "error", ferr, "source_error", err)
			return nil, timings, format, mapSourceError(err)
		}
		src, usedFallback = fallback, true
	}
	s.metrics.FetchNs.Add(uint64(time.Since(fetchStart).Nanoseconds()))
	s.metrics.BytesIn.Add(uint64(len(src.Body)))
	timings["fetch"] = ms(fetchStart)
	if usedFallback {
		timings["fallback"] = 1
	}

	// raw_ streams the fetched bytes untouched. It deliberately runs no hashsum,
	// decode, resolution, or size check; signing, allowlist/SSRF, rate limiting,
	// and quota accounting have already happened in handleImage.
	if t.Ops.Raw {
		res, err := imaging.PassThrough(src.Body)
		if err != nil {
			return nil, timings, format, errDecode()
		}
		for k, v := range res.Timings {
			timings[k] = v
		}
		timings["total"] = ms(total)
		return res.Data, timings, concreteFormat(format, res.Data), nil
	}

	// hs_: verify the digest before spending CPU on a decode. A fallback image is
	// not the source the hashsum was computed for, so it is not verified.
	if !usedFallback && !t.Ops.Hashsum.IsZero() {
		if !hashsumMatches(t.Ops.Hashsum, src.Body) {
			return nil, timings, format, errHashsumMismatch()
		}
	}

	// skp_: skip processing when the source is one of the listed formats and the
	// requested output format is the same. The header probe is enough to decide;
	// no pixel decode or dimension guard runs on the pass-through path.
	if len(t.Ops.SkipProcessing) > 0 {
		res, err := imaging.PassThrough(src.Body)
		if err != nil {
			return nil, timings, format, errDecode()
		}
		if res.Info.Format != "" && skipApplies(t.Ops.SkipProcessing, res.Info.Format, format) {
			for k, v := range res.Timings {
				timings[k] = v
			}
			timings["total"] = ms(total)
			return res.Data, timings, format, nil
		}
	}

	opts := imaging.Options{OutFormat: format, Quality: t.Ops.Quality}
	if t.Ops.Watermark != nil {
		wmURL := ""
		if t.Ops.WatermarkText == "" {
			wmURL = t.Ops.WatermarkURL
			if wmURL == "" {
				wmURL = s.cfg.WatermarkURL
			}
			if wmURL == "" {
				return nil, timings, format, errWatermarkMissing()
			}
		}
		wm, wmErr := s.watermarkFor(ctx, t.Ops.WatermarkText, wmURL)
		if wmErr != nil {
			s.log.Warn("watermark unavailable", "error", wmErr)
			return nil, timings, format, errWatermarkMissing()
		}
		opts.Watermark = wm
	}

	procStart := time.Now()
	res, err := runPipeline(s.proc, src.Body, t.Ops, opts)
	if err != nil {
		if usedFallback {
			return nil, timings, format, errDecode()
		}
		// A corrupt/undecodable source falls back to the fallback image too: the
		// commercial products treat "no usable image" the same way.
		if t.Ops.FallbackURL != "" && errors.Is(err, imaging.ErrDecode) {
			src, ferr := fetch(ctx, t.Ops.FallbackURL)
			if ferr == nil {
				s.metrics.Fallback.Add(1)
				usedFallback = true
				timings["fallback"] = 1
				res, err = runPipeline(s.proc, src.Body, t.Ops, opts)
			}
		}
		if err != nil {
			if errors.Is(err, imaging.ErrDimension) {
				return nil, timings, format, errDimension()
			}
			if errors.Is(err, imaging.ErrDecode) {
				return nil, timings, format, errDecode()
			}
			return nil, timings, format, errInternal(err)
		}
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
			return nil, timings, format, errInternal(err)
		}
		timings["total"] = ms(total)
		return payload, timings, format, nil
	}
	timings["total"] = ms(total)
	chosen := res.Format
	if chosen == "" || chosen == ops.FormatBest {
		chosen = concreteFormat(format, res.Data)
	}
	return res.Data, timings, chosen, nil
}

// runPipeline runs the image pipeline, by default on its own goroutine.
//
// celeris pins every event-loop worker to a single CPU core (sched_setaffinity
// plus runtime.LockOSThread), and threads created from a pinned thread inherit
// that one-core mask. libvips overlaps decode with encode in two threads, so a
// transform running on a pinned worker loses that overlap: a 512px PNG from a
// 2040x1356 source took 69 ms inline versus 51 ms on an unpinned goroutine, and
// throughput at two connections went from 22 to 29 images/s. IOIMG_OFFLOAD=0
// restores the inline call.
func runPipeline(proc imaging.Processor, src []byte, o ops.Ops, opts imaging.Options) (imaging.Result, error) {
	if offloadDisabled() {
		return proc.Process(src, o, opts)
	}
	type outcome struct {
		res imaging.Result
		err error
	}
	ch := make(chan outcome, 1)
	go func() {
		res, err := proc.Process(src, o, opts)
		ch <- outcome{res, err}
	}()
	out := <-ch
	return out.res, out.err
}

// offloadDisabled reports whether the transform must run inline on the request
// goroutine. Kept as a function so tests can pin the behaviour.
func offloadDisabled() bool {
	off, err := strconv.ParseBool(os.Getenv("IOIMG_OFFLOAD"))
	return err == nil && !off
}

// applyResponseHeaders adds the operator-configured headers. Built-in response
// headers set afterwards win on a name collision, so configuration cannot break
// Content-Type/Content-Length/ETag framing by accident.
func (s *Server) applyResponseHeaders(h http.Header) {
	for _, x := range s.cfg.ResponseHeaders {
		h.Add(x.Name, x.Value)
	}
}

func (s *Server) writeBody(w http.ResponseWriter, r *http.Request, format ops.Format, filename string, data []byte, o ops.Ops) {
	ctype := imaging.ContentType(format)
	// raw_ and skp_ responses may be the source bytes verbatim, so the actual
	// media type comes from the bytes themselves rather than the requested
	// output format. Unknown payloads fall back to application/octet-stream.
	if o.Raw || (len(o.SkipProcessing) > 0 && format != ops.FormatJSON) {
		ctype = imaging.ContentTypeFromBytes(data)
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	if filename != "" {
		// fn_ is sanitised at parse time (letters, digits, dot, dash, underscore
		// only), so the quoted form cannot break out of the header value.
		w.Header().Set("Content-Disposition", `inline; filename="`+filename+`"`)
	}
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
	if he.Status >= 400 && he.Code != "rate_limited" &&
		strings.HasPrefix(r.Header.Get("Accept"), "image/") {
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
	if he.Retryable && w.Header().Get("Retry-After") == "" {
		w.Header().Set("Retry-After", "1")
	}
	w.WriteHeader(he.Status)
	body := map[string]any{
		"error": map[string]any{
			"code":       he.Code,
			"message":    he.Message,
			"request_id": requestID(r),
			"docs":       "https://ioimg.net/docs/errors#" + he.Code,
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

// defaultReplicaID is stable for the lifetime of the process and distinct
// between processes on the same host. Set IOIMG_REPLICA_ID to pin a value.
func defaultReplicaID() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "unknown"
	}
	return fmt.Sprintf("%s-%d", host, os.Getpid())
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

// SetCacheReadMode switches the L1 read path at runtime. The transport selector
// uses it because the best read mode depends on the engine: pinned event loops
// (celeris) want blocking one-fd reads, goroutine-per-connection transports
// (fiber) do better with io_uring.
func (s *Server) SetCacheReadMode(m cache.ReadMode) { s.cache.SetReadMode(m) }

// AllowedTLSHost reports whether an SNI hostname may receive an ACME
// certificate. Only custom domains in the currently loaded tenant snapshot
// qualify; the request allowlist and AllowAnyHost deliberately do not, because
// they answer a different question and would let arbitrary SNI burn the CA's
// issuance rate limit. Static deployments add hosts via IOIMG_TLS_HOSTS.
func (s *Server) AllowedTLSHost(host string) bool {
	return s.tenants.HasDomain(host)
}

// CacheReadMode reports the resolved L1 read mode.
func (s *Server) CacheReadMode() string { return s.cache.ReadMode() }
