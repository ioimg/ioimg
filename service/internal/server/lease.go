package server

import (
	"context"
	"sync"
	"time"

	"github.com/ioimg/ioimg/service/pkg/tenant"
)

// leaseBudget is the in-memory view of the reservations this replica holds.
// It is deliberately process-local: losing it only means falling back to the
// snapshot/local counter, never spending quota that was not reserved.
type leaseBudget struct {
	mu        sync.Mutex
	byProject map[string]*leaseState
	now       func() time.Time
}

type leaseState struct {
	grant      leaseGrant
	refreshing bool
}

type leaseGrant struct {
	id      string
	amount  int64
	unspent int64
	expires time.Time
}

func newLeaseBudget() *leaseBudget {
	return &leaseBudget{byProject: map[string]*leaseState{}, now: time.Now}
}

// quotaReservation is one request's claim on the budget. A lease unit is held
// at admission and only committed when the request actually counts a transform;
// cache hits and early failures release it again.
type quotaReservation struct {
	budget      *leaseBudget
	projectID   string
	leaseID     string
	lastAmount  int64
	held        bool
	spent       bool
	allowed     bool
	unlimited   bool
	needRefresh bool
	remaining   int64
}

func (r *quotaReservation) spend() string {
	if r == nil || r.unlimited {
		return ""
	}
	r.spent = true
	return r.leaseID
}

func (r *quotaReservation) release() {
	if r == nil || !r.held || r.spent {
		return
	}
	r.budget.release(r.projectID, r.leaseID)
}

func (l *leaseBudget) stateLocked(projectID string) *leaseState {
	st := l.byProject[projectID]
	if st == nil {
		return nil
	}
	if !st.grant.expires.IsZero() && !l.now().Before(st.grant.expires) {
		// Expired reservations stop counting immediately, exactly like the
		// control plane's expires_at > now() filter.
		delete(l.byProject, projectID)
		return nil
	}
	return st
}

func (l *leaseBudget) reserve(projectID string, base int64) *quotaReservation {
	l.mu.Lock()
	defer l.mu.Unlock()

	st := l.stateLocked(projectID)
	unspent := int64(0)
	lastAmount := int64(0)
	if st != nil {
		unspent = st.grant.unspent
		lastAmount = st.grant.amount
	}
	res := &quotaReservation{
		budget:     l,
		projectID:  projectID,
		lastAmount: lastAmount,
		remaining:  base + unspent,
	}
	if base+unspent <= 0 {
		return res
	}
	res.allowed = true
	if unspent > 0 {
		st.grant.unspent--
		unspent--
		res.leaseID = st.grant.id
		res.held = true
	}
	// Refresh at or below 10% of the last grant, including the unit this
	// request just took: waiting for exactly zero would leave a request with no
	// way to bootstrap the next lease.
	res.needRefresh = st == nil || lastAmount == 0 || unspent*10 <= lastAmount
	return res
}

func (l *leaseBudget) release(projectID, leaseID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	st := l.stateLocked(projectID)
	if st == nil || st.grant.id != leaseID {
		return
	}
	st.grant.unspent++
}

func (l *leaseBudget) beginRefresh(projectID string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	st := l.stateLocked(projectID)
	if st == nil {
		st = &leaseState{}
		l.byProject[projectID] = st
	}
	if st.refreshing {
		return false
	}
	st.refreshing = true
	return true
}

func (l *leaseBudget) endRefresh(projectID string, lease *tenant.QuotaLease) {
	l.mu.Lock()
	defer l.mu.Unlock()
	st := l.byProject[projectID]
	if st == nil {
		st = &leaseState{}
		l.byProject[projectID] = st
	}
	st.refreshing = false
	if lease == nil || lease.Amount <= 0 {
		// Keep whatever is left of the old lease: a denial is not a reason to
		// throw away quota this replica already reserved.
		return
	}
	st.grant = leaseGrant{
		id:      lease.LeaseID,
		amount:  lease.Amount,
		unspent: lease.Amount,
		expires: lease.ExpiresAt,
	}
}

func (s *Server) acquireQuota(p *tenant.Policy) *quotaReservation {
	if p.MonthlyLimit <= 0 {
		return &quotaReservation{unlimited: true, allowed: true, remaining: -1}
	}
	if s.leases == nil {
		remaining := s.quotaRemaining(p)
		return &quotaReservation{allowed: remaining != 0, remaining: remaining}
	}
	return s.leases.reserve(p.ID, s.quotaRemaining(p))
}

func (s *Server) maybeRefreshLease(p *tenant.Policy, r *quotaReservation) {
	if s.control == nil || s.leases == nil || p.MonthlyLimit <= 0 ||
		r == nil || !r.allowed || !r.needRefresh {
		return
	}
	if !s.leases.beginRefresh(p.ID) {
		return
	}
	want := p.MonthlyLimit / 10
	if want > 1000 {
		want = 1000
	}
	if want < 1 {
		want = 1
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		lease, err := s.control.LeaseQuota(ctx, p.ID, s.replicaID, want)
		if err != nil {
			s.metrics.LeaseDenied.Add(1)
			s.leases.endRefresh(p.ID, nil)
			s.log.Warn("quota lease refresh failed", "project", p.ID, "error", err)
			return
		}
		if lease.Amount <= 0 {
			s.metrics.LeaseDenied.Add(1)
		} else {
			s.metrics.LeaseGrants.Add(1)
		}
		s.leases.endRefresh(p.ID, lease)
	}()
}
