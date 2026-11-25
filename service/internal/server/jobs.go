package server

import (
	"sync"
	"time"
)

// WarmResult is one target outcome inside a warm job.
type WarmResult struct {
	Source string `json:"source"`
	Ops    string `json:"ops"`
	Status string `json:"status"` // ok|skipped|failed
	Error  string `json:"error,omitempty"`
}

// Job is an asynchronous warm/batch job.
type Job struct {
	ID      string       `json:"jobId"`
	Status  string       `json:"status"` // queued|running|completed|failed
	Total   int          `json:"total"`
	Done    int          `json:"done"`
	Failed  int          `json:"failed"`
	Created time.Time    `json:"createdAt"`
	Updated time.Time    `json:"updatedAt"`
	Results []WarmResult `json:"results,omitempty"`
}

type jobStore struct {
	mu    sync.Mutex
	jobs  map[string]*Job
	order []string
	max   int
}

func newJobStore(max int) *jobStore {
	if max <= 0 {
		max = 100
	}
	return &jobStore{jobs: map[string]*Job{}, max: max}
}

func (s *jobStore) create(id string, total int) *Job {
	j := &Job{ID: id, Status: "queued", Total: total, Created: time.Now(), Updated: time.Now()}
	s.mu.Lock()
	s.jobs[id] = j
	s.order = append(s.order, id)
	for len(s.order) > s.max {
		old := s.order[0]
		s.order = s.order[1:]
		delete(s.jobs, old)
	}
	s.mu.Unlock()
	return j
}

func (s *jobStore) get(id string) (Job, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jobs[id]
	if !ok {
		return Job{}, false
	}
	cp := *j
	cp.Results = append([]WarmResult(nil), j.Results...)
	return cp, true
}

func (s *jobStore) update(id string, fn func(*Job)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if j, ok := s.jobs[id]; ok {
		fn(j)
		j.Updated = time.Now()
	}
}
