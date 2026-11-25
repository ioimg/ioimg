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

// UsageReport is one project's counters since the last report.
type UsageReport struct {
	ProjectID  string `json:"projectId"`
	Transforms int64  `json:"transforms"`
	BytesIn    int64  `json:"bytesIn"`
	BytesOut   int64  `json:"bytesOut"`
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
