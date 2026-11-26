package server

import (
	"sync"
	"time"
)

// bucket is a token bucket keyed by client identity.
type bucket struct {
	tokens float64
	last   time.Time
}

// limiter is a small per-key token bucket. It is deliberately per-process;
// distributed limits belong at the edge or in Valkey.
type limiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	rps     float64
	burst   float64
}

func newLimiter(rps float64, burst int) *limiter {
	if rps <= 0 {
		return nil
	}
	if burst <= 0 {
		burst = int(rps)
		if burst < 1 {
			burst = 1
		}
	}
	return &limiter{buckets: map[string]*bucket{}, rps: rps, burst: float64(burst)}
}

func (l *limiter) allow(key string) bool {
	if l == nil {
		return true
	}
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.buckets[key]
	if !ok {
		if len(l.buckets) > 100_000 {
			// Bound memory under a spoofed-IP flood.
			l.buckets = make(map[string]*bucket)
		}
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	}
	elapsed := now.Sub(b.last).Seconds()
	b.tokens = minF(l.burst, b.tokens+elapsed*l.rps)
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

func minF(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

// projectBucket is a project's token bucket. rps and burst are stored per bucket
// because a control-plane snapshot can change them while requests are in flight.
type projectBucket struct {
	tokens float64
	last   time.Time
	rps    float64
	burst  float64
}

// projectLimiter applies a per-project token bucket in addition to the per-IP
// edge limiter above. It is per-process, like limiter, because a shared limit
// belongs at the edge or in Valkey.
//
// A project with no limit never gets a bucket, so the unlimited default costs
// one branch and no allocation. Buckets are dropped after twice their refill
// window of inactivity: an attacker cannot grow the map by cycling project ids,
// and a project that goes quiet for long enough comes back with a full burst.
type projectLimiter struct {
	mu      sync.Mutex
	buckets map[string]*projectBucket
	now     func() time.Time
}

func newProjectLimiter() *projectLimiter {
	return &projectLimiter{buckets: map[string]*projectBucket{}, now: time.Now}
}

// projectLimitDecision describes one admission decision. RetryAfter and Reset
// are in seconds so the HTTP layer can ceil them into headers without losing
// sub-second precision here.
type projectLimitDecision struct {
	Allowed    bool
	Limit      int
	Remaining  int
	RetryAfter float64
	Reset      float64
}

func (l *projectLimiter) allow(projectID string, rps float64, burst int) projectLimitDecision {
	// Zero or negative on either field means unlimited, and an unlimited
	// project never gets a bucket.
	if l == nil || projectID == "" || rps <= 0 || burst <= 0 {
		return projectLimitDecision{Allowed: true, Remaining: -1}
	}
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweepLocked(now)

	b, ok := l.buckets[projectID]
	if !ok {
		b = &projectBucket{tokens: float64(burst), last: now, rps: rps, burst: float64(burst)}
		l.buckets[projectID] = b
	} else {
		elapsed := now.Sub(b.last).Seconds()
		if elapsed < 0 {
			elapsed = 0
		}
		b.tokens = minF(b.burst, b.tokens+elapsed*b.rps)
		b.last = now
		b.rps = rps
		b.burst = float64(burst)
		if b.tokens > b.burst {
			b.tokens = b.burst
		}
	}

	if b.tokens < 1 {
		wait := (1 - b.tokens) / b.rps
		return projectLimitDecision{
			Allowed: false, Limit: burst, Remaining: 0,
			RetryAfter: wait, Reset: wait,
		}
	}
	b.tokens--
	return projectLimitDecision{
		Allowed: true, Limit: burst, Remaining: int(b.tokens),
		Reset: (b.burst - b.tokens) / b.rps,
	}
}

// sweepLocked evicts buckets idle for at least twice their refill window. The
// full-refill window is burst/rps: the time an empty bucket needs to return to
// its burst size.
func (l *projectLimiter) sweepLocked(now time.Time) {
	for id, b := range l.buckets {
		window := b.burst / b.rps
		if now.Sub(b.last).Seconds() >= 2*window {
			delete(l.buckets, id)
		}
	}
}

// ceilSeconds rounds a duration in seconds up for Retry-After/Reset headers.
func ceilSeconds(seconds float64) int {
	if seconds <= 0 {
		return 0
	}
	n := int(seconds)
	if float64(n) < seconds {
		n++
	}
	return n
}
