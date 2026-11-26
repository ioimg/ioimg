package server

import "testing"

func TestProjectGateLimitsPerProject(t *testing.T) {
	g := newProjectGate()

	if !g.acquire("a", 1) {
		t.Fatal("first acquire must succeed")
	}
	if g.acquire("a", 1) {
		t.Fatal("second acquire over the limit must fail")
	}
	// Another project is unaffected: the limit is per project, not global.
	if !g.acquire("b", 1) {
		t.Fatal("another project must not be blocked")
	}

	g.release("a")
	if !g.acquire("a", 1) {
		t.Fatal("released slot must be reusable")
	}
}

func TestProjectGateUnlimitedAndEmpty(t *testing.T) {
	g := newProjectGate()
	// Zero or negative means unlimited and must never fail.
	for i := 0; i < 100; i++ {
		if !g.acquire("a", 0) || !g.acquire("a", -3) {
			t.Fatal("unlimited limit must always allow")
		}
	}
	// An empty project id (default policy without tenancy) is never gated.
	if !g.acquire("", 1) {
		t.Fatal("empty project must not be gated")
	}
	// Acquires without a limit are not tracked, so release must be a no-op.
	g.release("")
	inflight, _ := g.stats()
	if len(inflight) != 0 {
		t.Fatalf("unlimited acquires must not be tracked, got %v", inflight)
	}
}

func TestProjectGateReleaseWithoutAcquire(t *testing.T) {
	g := newProjectGate()
	// A release without a matching acquire must not go negative.
	g.release("a")
	g.release("missing")
	inflight, rejected := g.stats()
	if len(inflight) != 0 || rejected != 0 {
		t.Fatalf("want empty stats, got %v %d", inflight, rejected)
	}
}

func TestProjectGateStats(t *testing.T) {
	g := newProjectGate()
	g.acquire("a", 1)
	g.acquire("a", 1) // rejected
	g.acquire("b", 4)
	inflight, rejected := g.stats()
	if rejected != 1 {
		t.Fatalf("want 1 rejection, got %d", rejected)
	}
	if inflight["a"] != 1 || inflight["b"] != 1 {
		t.Fatalf("want one slot each, got %v", inflight)
	}
	// stats returns a copy: mutating it must not affect the gate.
	inflight["a"] = 99
	if again, _ := g.stats(); again["a"] != 1 {
		t.Fatalf("stats must return a copy, got %v", again)
	}
}

func TestProjectGateLimitShrinkAppliesToNewRequests(t *testing.T) {
	g := newProjectGate()
	if !g.acquire("a", 4) || !g.acquire("a", 4) || !g.acquire("a", 4) {
		t.Fatal("acquires under the old limit must succeed")
	}
	// The snapshot lowers the limit while three requests are in flight: the
	// next request is refused, in-flight work is never aborted.
	if g.acquire("a", 2) {
		t.Fatal("a shrunken limit must refuse new work")
	}
	g.release("a")
	g.release("a")
	if !g.acquire("a", 2) {
		t.Fatal("released slot must be reusable at the new limit")
	}
}
