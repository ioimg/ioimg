package tenant

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

// Client talks to the ioimg control plane.
type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

// NewClient builds a control-plane client.
func NewClient(baseURL, token string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return &Client{
		BaseURL: baseURL,
		Token:   token,
		HTTP:    &http.Client{Timeout: timeout},
	}
}

// FetchSnapshot returns the current snapshot. notModified is true when the
// control plane replies 304 for the given revision.
func (c *Client) FetchSnapshot(ctx context.Context, since int64) (snap *Snapshot, notModified bool, err error) {
	u := c.BaseURL + "/v1/config/snapshot"
	if since > 0 {
		u += "?since=" + strconv.FormatInt(since, 10)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotModified {
		return nil, true, nil
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, false, fmt.Errorf("control plane %d: %s", resp.StatusCode, string(body))
	}
	var out Snapshot
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&out); err != nil {
		return nil, false, fmt.Errorf("decode snapshot: %w", err)
	}
	return &out, false, nil
}

// UsageReport is one project's counters since the last report. LeaseID is the
// reservation the transforms were spent from; empty keeps old payloads valid.
type UsageReport struct {
	// ID makes a report idempotent: the control plane records it and ignores a
	// replay. It must be unique per batch and reused when a batch is retried.
	ID         string `json:"id,omitempty"`
	ProjectID  string `json:"projectId"`
	Transforms int64  `json:"transforms"`
	BytesIn    int64  `json:"bytesIn"`
	BytesOut   int64  `json:"bytesOut"`
	LeaseID    string `json:"leaseId,omitempty"`
}

// QuotaLease is a reservation of monthly transform quota for this replica.
type QuotaLease struct {
	LeaseID   string    `json:"leaseId"`
	Amount    int64     `json:"amount"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// leaseTimeout bounds the refresh call. A slow control plane must never hold a
// request; the caller runs this off the request path and falls back to the
// current lease/snapshot budget when it fails.
const leaseTimeout = 2 * time.Second

// LeaseQuota asks the control plane to reserve want transforms for a replica.
// There is no retry here: the existing periodic usage/report policy handles
// failures without turning a quota blip into a request storm.
func (c *Client) LeaseQuota(
	ctx context.Context,
	projectID, replicaID string,
	want int64,
) (*QuotaLease, error) {
	body, _ := json.Marshal(map[string]any{
		"projectId": projectID,
		"replicaId": replicaID,
		"want":      want,
	})
	ctx, cancel := context.WithTimeout(ctx, leaseTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.BaseURL+"/v1/quota/lease", bytesReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("quota lease %d: %s", resp.StatusCode, string(raw))
	}
	var out QuotaLease
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode quota lease: %w", err)
	}
	return &out, nil
}

// ReportUsage sends a batch of counters.
func (c *Client) ReportUsage(ctx context.Context, reports []UsageReport) error {
	if len(reports) == 0 {
		return nil
	}
	body, _ := json.Marshal(map[string]any{"reports": reports})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/v1/usage", bytesReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("usage report %d: %s", resp.StatusCode, string(raw))
	}
	return nil
}
