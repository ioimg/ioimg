package server

import "sync"

// projectGate enforces a per-project transform concurrency limit. It is a
// counter, not a semaphore channel, because the limit can change on every
// control-plane snapshot: a channel would have to be rebuilt while requests are
// in flight, which either leaks slots or hands out too many.
//
// Only the miss path passes through here (cache hits never touch a worker slot),
// so the mutex is not on the hot path of a warm cache.
type projectGate struct {
	mu       sync.Mutex
	inflight map[string]int
	rejected uint64
}

func newProjectGate() *projectGate {
	return &projectGate{inflight: map[string]int{}}
}

// acquire reserves a slot for the project. A limit of zero or less means
// unlimited and costs one map read.
func (g *projectGate) acquire(projectID string, limit int) bool {
	if limit <= 0 || projectID == "" {
		return true
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	current := g.inflight[projectID]
	if current >= limit {
		g.rejected++
		return false
	}
	g.inflight[projectID] = current + 1
	return true
}

func (g *projectGate) release(projectID string) {
	if projectID == "" {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if current := g.inflight[projectID] - 1; current > 0 {
		g.inflight[projectID] = current
		return
	}
	delete(g.inflight, projectID)
}

// stats reports the current counters for metrics.
func (g *projectGate) stats() (inflight map[string]int, rejected uint64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	inflight = make(map[string]int, len(g.inflight))
	for id, n := range g.inflight {
		inflight[id] = n
	}
	return inflight, g.rejected
}
