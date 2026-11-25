package purge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// Cloudflare purges by file URL or cache tag.
type Cloudflare struct {
	Token  string
	ZoneID string
	Options
}

func (c *Cloudflare) Name() string { return "cloudflare" }

func (c *Cloudflare) Purge(ctx context.Context, urls, tags []string) (Result, error) {
	res := Result{Provider: c.Name(), URLs: len(urls), Tags: len(tags)}
	if c.Token == "" || c.ZoneID == "" {
		res.Result = statusSkipped
		res.Error = "not configured"
		return res, nil
	}
	base := baseOr(c.BaseURL, "https://api.cloudflare.com/client/v4")
	endpoint := base + "/zones/" + c.ZoneID + "/purge_cache"

	send := func(payload map[string]any) error {
		body, _ := json.Marshal(payload)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+c.Token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := clientOr(c.Client).Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
		if resp.StatusCode/100 != 2 {
			return fmt.Errorf("cloudflare %d: %s", resp.StatusCode, truncate(string(raw), 200))
		}
		var parsed struct {
			Success bool `json:"success"`
		}
		if err := json.Unmarshal(raw, &parsed); err == nil && !parsed.Success {
			return fmt.Errorf("cloudflare rejected purge")
		}
		return nil
	}

	if len(urls) > 0 {
		if err := send(map[string]any{"files": urls}); err != nil {
			res.Result = statusFailed
			res.Error = err.Error()
			return res, err
		}
	}
	if len(tags) > 0 {
		if len(tags) > 30 {
			tags = tags[:30]
		}
		if err := send(map[string]any{"tags": tags}); err != nil {
			res.Result = statusFailed
			res.Error = err.Error()
			return res, err
		}
	}
	res.Result = statusOK
	return res, nil
}

// Bunny purges by URL (its API has no tag purge).
type Bunny struct {
	Key string
	Options
}

func (b *Bunny) Name() string { return "bunny" }

func (b *Bunny) Purge(ctx context.Context, urls, tags []string) (Result, error) {
	res := Result{Provider: b.Name(), URLs: len(urls), Tags: len(tags)}
	if b.Key == "" {
		res.Result = statusSkipped
		res.Error = "not configured"
		return res, nil
	}
	if len(urls) == 0 {
		res.Result = statusSkipped
		res.Error = "bunny only supports URL purges"
		return res, nil
	}
	base := baseOr(b.BaseURL, "https://api.bunny.net")
	for _, u := range urls {
		endpoint := base + "/purge?url=" + url.QueryEscape(u) + "&async=false"
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
		if err != nil {
			res.Result, res.Error = statusFailed, err.Error()
			return res, err
		}
		req.Header.Set("AccessKey", b.Key)
		resp, err := clientOr(b.Client).Do(req)
		if err != nil {
			res.Result, res.Error = statusFailed, err.Error()
			return res, err
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			res.Result = statusFailed
			res.Error = fmt.Sprintf("bunny %d: %s", resp.StatusCode, truncate(string(raw), 200))
			return res, fmt.Errorf("%s", res.Error)
		}
	}
	res.Result = statusOK
	return res, nil
}

// Fastly purges by URL or Surrogate-Key.
type Fastly struct {
	Token     string
	ServiceID string
	Options
}

func (f *Fastly) Name() string { return "fastly" }

func (f *Fastly) Purge(ctx context.Context, urls, tags []string) (Result, error) {
	res := Result{Provider: f.Name(), URLs: len(urls), Tags: len(tags)}
	if f.Token == "" || f.ServiceID == "" {
		res.Result = statusSkipped
		res.Error = "not configured"
		return res, nil
	}
	base := baseOr(f.BaseURL, "https://api.fastly.com")
	do := func(endpoint, keyHeader string) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Fastly-Key", f.Token)
		if keyHeader != "" {
			req.Header.Set("Surrogate-Key", keyHeader)
		}
		resp, err := clientOr(f.Client).Do(req)
		if err != nil {
			return err
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			return fmt.Errorf("fastly %d: %s", resp.StatusCode, truncate(string(raw), 200))
		}
		return nil
	}
	for _, u := range urls {
		if err := do(base+"/service/"+f.ServiceID+"/purge/"+url.PathEscape(u), ""); err != nil {
			res.Result, res.Error = statusFailed, err.Error()
			return res, err
		}
	}
	if len(tags) > 0 {
		if err := do(base+"/service/"+f.ServiceID+"/purge", join(tags)); err != nil {
			res.Result, res.Error = statusFailed, err.Error()
			return res, err
		}
	}
	res.Result = statusOK
	return res, nil
}

func join(v []string) string {
	out := ""
	for i, s := range v {
		if i > 0 {
			out += " "
		}
		out += s
	}
	return out
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
