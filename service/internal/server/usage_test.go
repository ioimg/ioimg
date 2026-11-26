package server

import (
	"testing"

	"github.com/ioimg/ioimg/service/pkg/tenant"
)

// A successful flush must not refill the quota: the snapshot the poller holds is
// only as fresh as its last revision, so "pending is empty" cannot mean "no
// usage". This is the bug a purely pending-based check hides.
func TestQuotaSurvivesFlush(t *testing.T) {
	s := &Server{usage: newUsage()}
	p := &tenant.Policy{ID: "p", MonthlyLimit: 10}

	for i := 0; i < 4; i++ {
		s.countUsage("p", 100)
	}
	if got := s.quotaRemaining(p); got != 6 {
		t.Fatalf("want 6 left, got %d", got)
	}
	// The reporter takes the batch; the control plane acknowledges it, but the
	// next snapshot still shows 0 transforms (304, stale). Quota must not reset.
	if reports := s.usage.take(); len(reports) != 1 {
		t.Fatalf("want one report, got %d", len(reports))
	}
	if got := s.quotaRemaining(p); got != 6 {
		t.Fatalf("quota reset after a flush: want 6, got %d", got)
	}
}

// The control plane's aggregate covers other replicas, so it wins when it is
// larger than this process' own count.
func TestQuotaUsesSnapshotWhenLarger(t *testing.T) {
	s := &Server{usage: newUsage()}
	s.countUsage("p", 1)
	p := &tenant.Policy{ID: "p", MonthlyLimit: 100, Usage: tenant.Usage{Transforms: 90}}
	if got := s.quotaRemaining(p); got != 10 {
		t.Fatalf("want 10 left, got %d", got)
	}
	// And a local count larger than the snapshot wins in the other direction.
	for i := 0; i < 95; i++ {
		s.countUsage("p", 1)
	}
	if got := s.quotaRemaining(p); got != 4 {
		t.Fatalf("want 4 left, got %d", got)
	}
}

func TestQuotaUnlimitedAndExhausted(t *testing.T) {
	s := &Server{usage: newUsage()}
	if got := s.quotaRemaining(&tenant.Policy{ID: "p"}); got != -1 {
		t.Fatalf("want unlimited (-1), got %d", got)
	}
	for i := 0; i < 5; i++ {
		s.countUsage("p", 1)
	}
	if got := s.quotaRemaining(&tenant.Policy{ID: "p", MonthlyLimit: 3}); got != 0 {
		t.Fatalf("want 0 left, got %d", got)
	}
	// Usage for an empty project id is never counted.
	s.countUsage("", 1)
	if got := s.usage.totalFor(""); got != 0 {
		t.Fatalf("empty project must not be counted, got %d", got)
	}
}
