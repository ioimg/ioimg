package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/ioimg/ioimg/service/internal/cache"
	"github.com/ioimg/ioimg/service/internal/config"
	"github.com/ioimg/ioimg/service/internal/purge"
	"github.com/ioimg/ioimg/service/pkg/ops"
	"github.com/ioimg/ioimg/service/pkg/sign"
	"github.com/ioimg/ioimg/service/pkg/tenant"
)

// requireAdmin enforces the purge/warm admin bearer token.
func (s *Server) requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	admin := strings.TrimSpace(os.Getenv("IOIMG_ADMIN_TOKEN"))
	if admin == "" || !constantTimeEqual(bearerToken(r), admin) {
		s.writeError(w, r, &HTTPError{Status: http.StatusUnauthorized, Code: "unauthorized", Message: "admin token required"})
		return false
	}
	return true
}

// handlePurge implements POST /v1/purge: local L1/L2 invalidation plus
// best-effort CDN fan-out.
func (s *Server) handlePurge(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	var body struct {
		URLs   []string `json:"urls"`
		Tags   []string `json:"tags"`
		Prefix string   `json:"prefix"`
		All    bool     `json:"all"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		s.writeError(w, r, errInvalidOps("invalid JSON body"))
		return
	}
	deleted := 0
	if body.All {
		if err := s.cache.Clear(); err != nil {
			s.writeError(w, r, errInternal(err))
			return
		}
		if s.l2 != nil {
			ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
			n, err := s.l2.DeletePrefix(ctx, "")
			cancel()
			if err != nil {
				s.log.Warn("l2 purge all failed", "error", err)
			}
			deleted += n
		}
		deleted = -1
	}
	if body.Prefix != "" {
		deleted += s.cache.DeletePrefix(body.Prefix)
		if s.l2 != nil {
			ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
			n, err := s.l2.DeletePrefix(ctx, body.Prefix)
			cancel()
			if err != nil {
				s.log.Warn("l2 prefix purge failed", "error", err)
			}
			deleted += n
		}
	}
	project := ""
	if s.allowProjectHeader {
		project = r.Header.Get(s.projectHeader)
	}
	for _, raw := range body.URLs {
		u, err := url.Parse(raw)
		if err != nil {
			continue
		}
		policy, ok := s.tenants.Resolve(u.Hostname(), project)
		if !ok {
			continue
		}
		fake := &http.Request{URL: &url.URL{Path: u.Path, RawQuery: u.RawQuery}, Header: http.Header{}}
		t, err := parsePathTarget(fake, policy)
		if err != nil {
			continue
		}
		for _, key := range keysForAllFormats(s.version, policy.ID, t) {
			s.cache.Delete(key)
			if s.l2 != nil {
				ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
				if err := s.l2.Delete(ctx, key); err != nil {
					s.log.Warn("l2 delete failed", "error", err)
				}
				cancel()
			}
			deleted++
		}
	}

	results := s.purgeCDN(r.Context(), body.URLs, body.Tags)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"local": map[string]any{"keys": deleted},
		"cdn":   results,
	})
}

func (s *Server) purgeCDN(ctx context.Context, urls, tags []string) []purge.Result {
	results := make([]purge.Result, 0, len(s.purgers))
	for _, p := range s.purgers {
		ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		res, err := p.Purge(ctx, urls, tags)
		cancel()
		if err != nil && res.Error == "" {
			res.Result, res.Error = "failed", err.Error()
		}
		if res.Provider == "" {
			res.Provider = p.Name()
		}
		results = append(results, res)
	}
	return results
}

// handleWarm implements POST /v1/warm: queue up to 1000 targets and fill the
// caches in the background.
func (s *Server) handleWarm(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	var body struct {
		Targets []struct {
			Source string `json:"source"`
			Ops    string `json:"ops"`
		} `json:"targets"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&body); err != nil {
		s.writeError(w, r, errInvalidOps("invalid JSON body"))
		return
	}
	if len(body.Targets) == 0 || len(body.Targets) > 1000 {
		s.writeError(w, r, errInvalidOps("targets must contain 1..1000 entries"))
		return
	}
	policy, perr := s.policyFor(r)
	if perr != nil {
		s.writeError(w, r, perr)
		return
	}
	plans := make([]warmPlan, 0, len(body.Targets))
	for _, t := range body.Targets {
		plan, err := s.warmPlan(policy, t.Source, t.Ops)
		if err != nil {
			s.writeError(w, r, asHTTPError(err))
			return
		}
		plans = append(plans, plan)
	}
	id := "job_" + newRequestID()
	job := s.jobs.create(id, len(plans))
	go s.runWarm(job.ID, plans)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]any{"jobId": job.ID, "total": job.Total})
}

// handleJob implements GET /v1/jobs/{id}.
func (s *Server) handleJob(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/v1/jobs/")
	job, ok := s.jobs.get(id)
	if !ok {
		s.writeError(w, r, &HTTPError{Status: http.StatusNotFound, Code: "not_found", Message: "job not found"})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(job)
}

type warmPlan struct {
	projectID string
	target    Target
	format    ops.Format
	key       string
}

func (s *Server) warmPlan(policy *tenant.Policy, source, rawOps string) (warmPlan, error) {
	o, err := ops.Parse(rawOps, policy.Presets)
	if err != nil {
		return warmPlan{}, errInvalidOps(err.Error())
	}
	canonical, err := sign.CanonicalSource(source)
	if err != nil {
		return warmPlan{}, errInvalidSource()
	}
	if u, err := url.Parse(canonical); err == nil {
		if !policy.AllowedHost(u.Hostname()) {
			return warmPlan{}, errNotAllowed(u.Hostname())
		}
	}
	format := o.Format
	if format == ops.FormatAuto {
		format = ops.Negotiate("", s.formats, false)
	}
	t := Target{Ops: o, Canonical: o.Canonical(), Source: canonical}
	return warmPlan{projectID: policy.ID, target: t, format: format, key: cache.Key(config.CacheKeyVersion+"|"+s.version+"|"+policy.ID, t.Canonical, t.Source, string(format))}, nil
}

func (s *Server) runWarm(id string, plans []warmPlan) {
	s.jobs.update(id, func(j *Job) { j.Status = "running" })
	for _, p := range plans {
		res := WarmResult{Source: p.target.Source, Ops: p.target.Canonical}
		if _, _, ok := s.cache.Get(p.key); ok {
			res.Status = "skipped"
			s.recordWarm(id, res)
			continue
		}
		select {
		case s.sem <- struct{}{}:
		case <-time.After(30 * time.Second):
			res.Status, res.Error = "failed", "worker pool busy"
			s.recordWarm(id, res)
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		data, _, err := s.render(ctx, p.target, p.format)
		cancel()
		<-s.sem
		if err != nil {
			res.Status, res.Error = "failed", asHTTPError(err).Code
			s.recordWarm(id, res)
			continue
		}
		if err := s.cache.Put(p.key, data); err != nil {
			s.log.Warn("warm l1 write failed", "error", err)
		}
		if s.l2 != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			// Re-warming a page must not re-upload entries that are already there:
			// object stores have no batch PUT, and each write is a billable Class A
			// operation (R2: 1M/month). The cache key pins the pipeline version, so
			// an existing key with the same size means identical bytes.
			if st, ok := s.l2.(interface {
				Stat(context.Context, string) (int64, bool, error)
			}); ok {
				if size, exists, err := st.Stat(ctx, p.key); err == nil && exists && size == int64(len(data)) {
					s.metrics.L2Skipped.Add(1)
					cancel()
					s.countUsage(policyIDForPlan(p), int64(len(data)))
					res.Status = "ok"
					s.recordWarm(id, res)
					continue
				}
			}
			if err := s.l2.Put(ctx, p.key, data); err != nil {
				s.log.Warn("warm l2 write failed", "error", err)
			}
			cancel()
		}
		s.countUsage(policyIDForPlan(p), int64(len(data)))
		res.Status = "ok"
		s.recordWarm(id, res)
	}
	s.jobs.update(id, func(j *Job) {
		j.Status = "completed"
		if j.Failed == j.Total {
			j.Status = "failed"
		}
	})
}

func policyIDForPlan(p warmPlan) string { return p.projectID }

func (s *Server) recordWarm(id string, res WarmResult) {
	s.jobs.update(id, func(j *Job) {
		j.Done++
		if res.Status == "failed" {
			j.Failed++
		}
		j.Results = append(j.Results, res)
	})
}

func keysForAllFormats(version, projectID string, t Target) []string {
	formats := []ops.Format{ops.FormatAVIF, ops.FormatWebP, ops.FormatJPEG, ops.FormatPNG, ops.FormatGIF, ops.FormatJSON}
	if t.Ops.Format != ops.FormatAuto {
		formats = []ops.Format{t.Ops.Format}
	}
	keys := make([]string, 0, len(formats))
	for _, f := range formats {
		keys = append(keys, cache.Key(config.CacheKeyVersion+"|"+version+"|"+projectID, t.Canonical, t.Source, string(f)))
	}
	return keys
}
