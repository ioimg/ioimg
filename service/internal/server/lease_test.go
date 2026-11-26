package server

import (
	"bytes"
	"net/http"
	"testing"
	"time"

	"github.com/ioimg/ioimg/service/pkg/tenant"
)

func setProjectUsage(t *testing.T, f *fakeControl, projectID string, used int64) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.projects[projectID]
	if !ok {
		t.Fatalf("project %q missing", projectID)
	}
	p.Usage = tenant.Usage{Period: "2026-12", Transforms: used}
	f.projects[projectID] = p
	f.revision++
}

func waitForLeaseCalls(t *testing.T, f *fakeControl, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		f.mu.Lock()
		calls := f.leaseCalls
		f.mu.Unlock()
		if calls >= want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("lease calls = %d, want at least %d", calls, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func leaseCalls(f *fakeControl) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.leaseCalls
}

// TestQuotaLeaseStopsAtBudgetAndReportsLease exercises the reservation path
// end-to-end against a static control-plane stub: a replica gets a lease, spends
// it, reports the lease id, and eventually answers 429 instead of running to the
// snapshot's much larger remaining number.
func TestQuotaLeaseStopsAtBudgetAndReportsLease(t *testing.T) {
	f := newFakeControl(t)
	f.createProject(t, map[string]any{
		"id": "leased", "name": "Leased", "allowAnyHost": true,
		"monthlyTransforms": 6,
	})
	setProjectUsage(t, f, "leased", 3)
	f.leaseAmount = 2
	f.leaseTotal = 2
	plane := newControlDataPlane(t, f.srv.URL)
	path := "/w_40,f_jpeg/" + f.origin.URL + "/photo.jpg"

	resp := f.request(t, plane, "leased", path+"?n=0")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("first request: %d", resp.StatusCode)
	}
	resp.Body.Close()
	waitForLeaseCalls(t, f, 1)

	got429 := false
	for i := 1; i < 32; i++ {
		resp = f.request(t, plane, "leased", path+"?n="+itoa(int64(i)))
		code := resp.StatusCode
		resp.Body.Close()
		if code == http.StatusTooManyRequests {
			got429 = true
			break
		}
		if code != http.StatusOK {
			t.Fatalf("request %d: %d", i, code)
		}
	}
	if !got429 {
		t.Fatal("replica never hit quota_exceeded")
	}

	reports := plane.usage.take()
	foundLease := false
	for _, r := range reports {
		if r.LeaseID != "" {
			foundLease = true
			break
		}
	}
	if !foundLease {
		t.Fatalf("usage reports did not carry a lease id: %+v", reports)
	}
	if grants := plane.metrics.LeaseGrants.Load(); grants < 1 {
		t.Fatalf("lease grants = %d, want at least 1", grants)
	}
	if spent := plane.metrics.LeaseSpent.Load(); spent == 0 {
		t.Fatal("no transforms were accounted to a lease")
	}
	metrics := plane.metrics.Render(0, 0)
	for _, want := range []string{
		"ioimg_quota_lease_grants_total ",
		"ioimg_quota_lease_denied_total ",
		"ioimg_quota_lease_spent_total ",
	} {
		if !bytes.Contains([]byte(metrics), []byte(want)) {
			t.Fatalf("missing %q in metrics:\n%s", want, metrics)
		}
	}
}

// A replica refreshes its lease when the remaining reservation gets low, not
// only after every last unit is gone.
func TestQuotaLeaseRefreshesWhenLow(t *testing.T) {
	f := newFakeControl(t)
	f.createProject(t, map[string]any{
		"id": "refresh", "name": "Refresh", "allowAnyHost": true,
		"monthlyTransforms": 100,
	})
	f.leaseAmount = 100 // capped by the request to 10% of the plan = 10
	plane := newControlDataPlane(t, f.srv.URL)
	path := "/w_40,f_jpeg/" + f.origin.URL + "/photo.jpg"

	for i := 0; i < 30; i++ {
		resp := f.request(t, plane, "refresh", path+"?n="+itoa(int64(i)))
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("request %d: %d", i, resp.StatusCode)
		}
		resp.Body.Close()
	}
	waitForLeaseCalls(t, f, 2)
}

// When the control plane is unreachable the replica may spend only the budget
// it already holds, then it must fail closed with quota_exceeded.
func TestQuotaLeaseFailsClosedWhenControlPlaneDown(t *testing.T) {
	f := newFakeControl(t)
	f.createProject(t, map[string]any{
		"id": "down", "name": "Down", "allowAnyHost": true,
		"monthlyTransforms": 20,
	})
	f.leaseAmount = 5 // capped by want = 10% of 20 = 2
	plane := newControlDataPlane(t, f.srv.URL)
	path := "/w_40,f_jpeg/" + f.origin.URL + "/photo.jpg"

	resp := f.request(t, plane, "down", path+"?n=boot")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("bootstrap request: %d", resp.StatusCode)
	}
	resp.Body.Close()
	waitForLeaseCalls(t, f, 1)
	before := leaseCalls(f)

	f.srv.Close()

	allowed := 0
	got429 := false
	for i := 0; i < 64; i++ {
		resp = f.request(t, plane, "down", path+"?n="+itoa(int64(i)))
		code := resp.StatusCode
		resp.Body.Close()
		if code == http.StatusTooManyRequests {
			got429 = true
			break
		}
		if code != http.StatusOK {
			t.Fatalf("request %d: %d", i, code)
		}
		allowed++
	}
	if !got429 {
		t.Fatal("replica kept serving after its budget ran out with the control plane down")
	}
	if allowed > 22 {
		t.Fatalf("spent %d requests after the control plane went down, beyond the held budget", allowed)
	}
	if after := leaseCalls(f); after != before {
		t.Fatalf("replica called the closed control plane: %d -> %d", before, after)
	}
}
