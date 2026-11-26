package server

import (
	"context"
	"sync"
	"time"

	"github.com/ioimg/ioimg/service/pkg/tenant"
)

// usage accumulates per-project counters and reports them to the control
// plane in batches. Reporting is best-effort: failed batches are merged back.
type usage struct {
	mu sync.Mutex
	// pending is what has not been acknowledged by the control plane yet. It is
	// keyed per project and lease so a report can carry the reservation it spent
	// from without mixing leases together.
	pending map[usageKey]*tenant.UsageReport
	// total is everything this process has counted since it started. It never
	// resets, so a successful flush cannot refill a project's quota: the
	// control-plane snapshot is only as fresh as the last revision the poller
	// saw, and a 304 must not look like "no usage".
	total map[string]*tenant.UsageReport
}

type usageKey struct {
	projectID string
	leaseID   string
}

func newUsage() *usage {
	return &usage{
		pending: map[usageKey]*tenant.UsageReport{},
		total:   map[string]*tenant.UsageReport{},
	}
}

func (u *usage) add(projectID, leaseID string, transforms, bytesIn, bytesOut int64) {
	if projectID == "" {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	key := usageKey{projectID: projectID, leaseID: leaseID}
	r := u.pending[key]
	if r == nil {
		r = &tenant.UsageReport{ProjectID: projectID, LeaseID: leaseID}
		u.pending[key] = r
	}
	r.Transforms += transforms
	r.BytesIn += bytesIn
	r.BytesOut += bytesOut

	t := u.total[projectID]
	if t == nil {
		t = &tenant.UsageReport{ProjectID: projectID}
		u.total[projectID] = t
	}
	t.Transforms += transforms
	t.BytesIn += bytesIn
	t.BytesOut += bytesOut
}

// totalFor is the authoritative local counter for a project.
func (u *usage) totalFor(projectID string) int64 {
	u.mu.Lock()
	defer u.mu.Unlock()
	if r := u.total[projectID]; r != nil {
		return r.Transforms
	}
	return 0
}

func (u *usage) take() []tenant.UsageReport {
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.pending) == 0 {
		return nil
	}
	out := make([]tenant.UsageReport, 0, len(u.pending))
	for _, r := range u.pending {
		out = append(out, *r)
	}
	u.pending = map[usageKey]*tenant.UsageReport{}
	return out
}

func (u *usage) mergeBack(reports []tenant.UsageReport) {
	u.mu.Lock()
	defer u.mu.Unlock()
	for _, r := range reports {
		key := usageKey{projectID: r.ProjectID, leaseID: r.LeaseID}
		p := u.pending[key]
		if p == nil {
			p = &tenant.UsageReport{ProjectID: r.ProjectID, LeaseID: r.LeaseID}
			u.pending[key] = p
		}
		p.Transforms += r.Transforms
		p.BytesIn += r.BytesIn
		p.BytesOut += r.BytesOut
	}
}

// quotaRemaining returns the monthly transform budget left. It uses whichever is
// larger: the control plane's aggregate (which covers every replica, but is only
// as fresh as the last snapshot revision) or this process' own lifetime count
// (which is fresher but blind to its peers). Taking the max means a flush, a 304
// poll, or a control-plane hiccup can never hand a project a second budget.
func (s *Server) quotaRemaining(p *tenant.Policy) int64 {
	if p.MonthlyLimit <= 0 {
		return -1 // unlimited
	}
	used := p.Usage.Transforms
	if local := s.usage.totalFor(p.ID); local > used {
		used = local
	}
	remaining := p.MonthlyLimit - used
	if remaining < 0 {
		return 0
	}
	return remaining
}

func (s *Server) countUsage(projectID string, bytesOut int64) {
	s.countUsageLease(projectID, "", bytesOut)
}

func (s *Server) countUsageLease(projectID, leaseID string, bytesOut int64) {
	s.usage.add(projectID, leaseID, 1, 0, bytesOut)
	if leaseID != "" {
		s.metrics.LeaseSpent.Add(1)
	}
}

func (s *Server) usageLoop(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			reports := s.usage.take()
			if len(reports) == 0 {
				continue
			}
			rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			err := s.control.ReportUsage(rctx, reports)
			cancel()
			if err != nil {
				s.log.Warn("usage report failed; will retry", "error", err, "reports", len(reports))
				s.usage.mergeBack(reports)
			}
		}
	}
}
