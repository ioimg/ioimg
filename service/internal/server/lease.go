package server

import (
	"context"
	"sync"
	"time"

	"github.com/ioimg/ioimg/service/pkg/tenant"
)

// leaseWait bounds how long a request waits for a lease it just ran out of.
const leaseWait = 3 * time.Second

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
	// refreshingSince bounds a refresh attempt. A lease call that never clears
	// the flag (crashed goroutine, wedged connection) must not brick the project
	// for the rest of the process lifetime: past leaseRefreshStale the flag is
	// treated as abandoned and a new attempt is allowed.
	refreshingSince time.Time
	// done is closed when the in-flight refresh finishes. Requests that arrive
	// while a lease call is already running wait on it instead of being refused:
	// a 429 costs microseconds, so without the wait a single 35 ms control-plane
	// round trip turns into hundreds of rejections.
	done chan struct{}
}

// leaseRefreshStale is how long a refresh may stay in flight before another
// attempt may take it over. Twice the wait bounds leaves room for a slow but
// healthy control plane to answer.
const leaseRefreshStale = 2 * leaseWait

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
	needLease   bool
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

// reserve claims one unit from the replica's lease. It never spends unreserved
// quota: the snapshot remainder lags the control plane by a usage flush, so
// granting from it before the first lease lands is exactly how replicas
// overshoot the plan. A replica with no lease therefore asks for one (one round
// trip, bounded by leaseWait) instead of serving on credit.
func (l *leaseBudget) reserve(projectID string) *quotaReservation {
	l.mu.Lock()
	defer l.mu.Unlock()

	st := l.stateLocked(projectID)
	unspent := int64(0)
	lastAmount := int64(0)
	if st != nil {
		unspent = st.grant.unspent
		lastAmount = st.grant.amount
	}
	remaining := int64(0)
	if st != nil {
		remaining = unspent
	}
	res := &quotaReservation{
		budget:     l,
		projectID:  projectID,
		lastAmount: lastAmount,
		remaining:  remaining,
	}
	if remaining <= 0 {
		res.needLease = true
		res.needRefresh = st == nil
		return res
	}
	res.allowed = true
	if unspent > 0 {
		st.grant.unspent--
		unspent--
		res.leaseID = st.grant.id
		res.held = true
	}
	if st != nil && unspent == 0 {
		// The next request must wait for quota instead of inventing it.
		res.needLease = true
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
	if st.refreshing && l.now().Sub(st.refreshingSince) < leaseRefreshStale {
		return false
	}
	st.refreshing = true
	st.refreshingSince = l.now()
	st.done = make(chan struct{})
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
	st.refreshingSince = time.Time{}
	if st.done != nil {
		close(st.done)
		st.done = nil
	}
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
	return s.leases.reserve(p.ID)
}

// leaseWant is how much a single lease asks for: a tenth of the plan, capped so
// one lease can never dominate a large plan.
func leaseWant(p *tenant.Policy) int64 {
	want := p.MonthlyLimit / 10
	if want > 1000 {
		want = 1000
	}
	if want < 1 {
		want = 1
	}
	return want
}

// ensureLease turns "out of lease units" into a short wait instead of a 429. A
// burst makes one control-plane call because beginRefresh is single-flight; the
// retry re-reserves against the new grant, and a refusal still ends in 429
// (fail-closed) rather than overshooting the plan.
func (s *Server) ensureLease(p *tenant.Policy, r *quotaReservation) *quotaReservation {
	if s.control == nil || s.leases == nil || p.MonthlyLimit <= 0 ||
		r == nil || r.allowed || !r.needLease {
		return r
	}
	if !s.leases.beginRefresh(p.ID) {
		// Someone else is already asking the control plane. Waiting one round
		// trip keeps the request in the "slow" bucket instead of turning the
		// whole burst into 429s.
		s.leases.waitRefresh(p.ID, leaseWait)
		retry := s.leases.reserve(p.ID)
		retry.needLease = false
		return retry
	}
	ctx, cancel := context.WithTimeout(context.Background(), leaseWait)
	defer cancel()
	lease, err := s.control.LeaseQuota(ctx, p.ID, s.replicaID, leaseWant(p))
	if err != nil {
		s.metrics.LeaseDenied.Add(1)
		s.leases.endRefresh(p.ID, nil)
		s.log.Warn("quota lease refresh failed", "project", p.ID, "error", err)
		return r
	}
	if lease.Amount <= 0 {
		s.metrics.LeaseDenied.Add(1)
	} else {
		s.metrics.LeaseGrants.Add(1)
	}
	s.leases.endRefresh(p.ID, lease)
	retry := s.leases.reserve(p.ID)
	retry.needLease = false
	return retry
}

// waitRefresh blocks until the in-flight refresh finishes or d elapses. It
// returns immediately when no refresh is running.
func (l *leaseBudget) waitRefresh(projectID string, d time.Duration) {
	l.mu.Lock()
	st := l.byProject[projectID]
	var ch chan struct{}
	if st != nil && st.refreshing {
		ch = st.done
	}
	l.mu.Unlock()
	if ch == nil {
		return
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ch:
	case <-t.C:
	}
}

// debugState is a diagnostic view of a replica's lease, used when a request is
// refused so the log explains which half of the budget ran out.
func (l *leaseBudget) debugState(projectID string) (id string, amount, unspent int64, expires time.Time, refreshing bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	st := l.stateLocked(projectID)
	if st == nil {
		return "", 0, 0, time.Time{}, false
	}
	return st.grant.id, st.grant.amount, st.grant.unspent, st.grant.expires, st.refreshing
}

// refreshAge reports how long the current refresh has been in flight (0 when
// no refresh is running), for diagnostics.
func (l *leaseBudget) refreshAge(projectID string) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	st := l.byProject[projectID]
	if st == nil || !st.refreshing {
		return "0s"
	}
	return l.now().Sub(st.refreshingSince).String()
}

func (s *Server) maybeRefreshLease(p *tenant.Policy, r *quotaReservation) {
	if s.control == nil || s.leases == nil || p.MonthlyLimit <= 0 ||
		r == nil || !r.allowed || !r.needRefresh {
		return
	}
	if !s.leases.beginRefresh(p.ID) {
		return
	}
	want := leaseWant(p)
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
