package server

import (
	"testing"
	"time"
)

func TestProjectLimiterUnlimitedDoesNotAllocate(t *testing.T) {
	l := newProjectLimiter()
	for i := 0; i < 100; i++ {
		if d := l.allow("free", 0, 0); !d.Allowed {
			t.Fatal("unlimited project must always be allowed")
		}
		if d := l.allow("", 10, 1); !d.Allowed {
			t.Fatal("empty project must not be limited")
		}
	}
	if len(l.buckets) != 0 {
		t.Fatalf("unlimited projects must not allocate buckets, got %d", len(l.buckets))
	}
}

func TestProjectLimiterRejectsAndReports(t *testing.T) {
	l := newProjectLimiter()
	if d := l.allow("acme", 1, 1); !d.Allowed || d.Remaining != 0 || d.Limit != 1 {
		t.Fatalf("first token: %+v", d)
	}
	d := l.allow("acme", 1, 1)
	if d.Allowed || d.Remaining != 0 || d.Limit != 1 {
		t.Fatalf("second request must be rejected: %+v", d)
	}
	if d.RetryAfter <= 0 || d.RetryAfter > 1 {
		t.Fatalf("want sub-second retry after, got %v", d.RetryAfter)
	}
	if ceilSeconds(d.RetryAfter) != 1 || ceilSeconds(d.Reset) != 1 {
		t.Fatalf("headers must ceil to one second: retry=%v reset=%v", d.RetryAfter, d.Reset)
	}
	if !l.allow("other", 1, 1).Allowed {
		t.Fatal("another project must have its own bucket")
	}
}

func TestProjectLimiterEvictsIdleBuckets(t *testing.T) {
	l := newProjectLimiter()
	now := time.Unix(1000, 0)
	l.now = func() time.Time { return now }

	// rps=1, burst=1 means a one-second refill window, so 2x = 2 seconds idle.
	if d := l.allow("idle", 1, 1); !d.Allowed {
		t.Fatal("first request must be allowed")
	}
	if _, ok := l.buckets["idle"]; !ok {
		t.Fatal("limited project must have a bucket")
	}

	now = now.Add(1999 * time.Millisecond)
	l.allow("idle", 1, 1) // still inside the eviction window; refills and uses it
	if _, ok := l.buckets["idle"]; !ok {
		t.Fatal("bucket evicted before 2x its refill window")
	}

	now = now.Add(2 * time.Second)
	l.allow("other", 1, 1)
	if _, ok := l.buckets["idle"]; ok {
		t.Fatal("idle bucket was not evicted after 2x its refill window")
	}
	if _, ok := l.buckets["other"]; !ok {
		t.Fatal("active bucket must survive the sweep")
	}
}

func TestProjectLimiterZeroBurstIsUnlimited(t *testing.T) {
	l := newProjectLimiter()
	// The control plane rejects this combination, but an absent burst must not
	// make the data plane invent a limit the operator did not configure.
	for i := 0; i < 10; i++ {
		if d := l.allow("acme", 2.5, 0); !d.Allowed {
			t.Fatalf("zero burst must be unlimited, got %+v", d)
		}
		if d := l.allow("negative", 2.5, -1); !d.Allowed {
			t.Fatalf("negative burst must be unlimited, got %+v", d)
		}
	}
	if len(l.buckets) != 0 {
		t.Fatalf("unlimited projects must not allocate buckets, got %d", len(l.buckets))
	}
}
