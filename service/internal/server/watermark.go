package server

import (
	"context"
	"fmt"
	"image"
	"sync"

	"github.com/ioimg/ioimg/service/internal/imaging"
)

// watermarkCache keeps decoded watermark images in memory: a watermark is
// fetched once per URL, not once per request, and is immutable afterwards.
type watermarkCache struct {
	mu    sync.Mutex
	items map[string]*watermarkEntry
}

type watermarkEntry struct {
	img image.Image
	err error
}

const watermarkCacheMax = 32

func (c *watermarkCache) get(ctx context.Context, url string, load func(context.Context, string) (image.Image, error)) (image.Image, error) {
	c.mu.Lock()
	if c.items == nil {
		c.items = make(map[string]*watermarkEntry)
	}
	if e, ok := c.items[url]; ok {
		c.mu.Unlock()
		return e.img, e.err
	}
	c.mu.Unlock()

	img, err := load(ctx, url)

	c.mu.Lock()
	if len(c.items) >= watermarkCacheMax {
		// The cache is tiny and watermark URLs are few in practice; dropping
		// everything is cheaper than tracking recency here.
		c.items = make(map[string]*watermarkEntry)
	}
	c.items[url] = &watermarkEntry{img: img, err: err}
	c.mu.Unlock()
	return img, err
}

// watermarkFor resolves the watermark image for a request. Text watermarks are
// rendered locally and never touch the fetcher; image watermarks (wmu_, then the
// configured default) go through the SSRF-guarded fetcher.
func (s *Server) watermarkFor(ctx context.Context, text, url string) (image.Image, error) {
	if text != "" {
		return imaging.RenderWatermarkText(text), nil
	}
	if url == "" {
		return nil, nil
	}
	return s.watermarks.get(ctx, url, func(ctx context.Context, u string) (image.Image, error) {
		res, err := s.fetcher.Fetch(ctx, u)
		if err != nil {
			return nil, fmt.Errorf("fetch watermark: %w", err)
		}
		img, info, err := imaging.DecodeAny(res.Body)
		if err != nil {
			return nil, fmt.Errorf("decode watermark: %w", err)
		}
		if info.Width > 4096 || info.Height > 4096 {
			return nil, fmt.Errorf("watermark exceeds 4096 px")
		}
		return img, nil
	})
}
