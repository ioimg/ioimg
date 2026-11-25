// Package transport selects and runs the HTTP transport of the data plane.
//
// Order (best measured throughput first, each tier verified in
// docs/benchmarks/io-uring.md):
//
//  1. celeris with the io_uring engine and blocking one-fd cache reads
//     (~65k rps HIT on a 16-core box; io_uring on the socket, not on the read).
//  2. Fiber (fasthttp) with io_uring cache reads (~55k rps). Used when the
//     celeris io_uring engine cannot start, which happens on kernels < 5.19 and
//     where io_uring is blocked for sockets but not for files.
//  3. Fiber with prefork and one-fd cache reads (~72k rps across 16 workers,
//     but per-process state: see the quota/usage caveat in ADR-0011).
//
// IOIMG_TRANSPORT forces one tier ("celeris", "fiber", "nethttp") and disables
// the fallback chain.
package transport

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/goceleris/celeris"
	"github.com/gofiber/fiber/v3"

	"github.com/ioimg/ioimg/service/internal/cache"
	"github.com/ioimg/ioimg/service/internal/config"
	"github.com/ioimg/ioimg/service/internal/server"
)

// Name identifies a transport tier.
type Name string

const (
	// Celeris is the default: io_uring sockets, blocking one-fd cache reads.
	Celeris Name = "celeris"
	// Fiber is fasthttp with io_uring cache reads.
	Fiber Name = "fiber"
	// NetHTTP is the standard library, always available.
	NetHTTP Name = "nethttp"
)

const (
	readHeaderTimeout = 10 * time.Second
	readTimeout       = 30 * time.Second
	writeTimeout      = 120 * time.Second
	idleTimeout       = 90 * time.Second
	maxHeaderBytes    = 64 << 10
	shutdownTimeout   = 15 * time.Second
)

// Serve runs the server on the best available transport until ctx is canceled.
func Serve(ctx context.Context, srv *server.Server, cfg config.Config, log *slog.Logger) error {
	forced := Name(strings.ToLower(strings.TrimSpace(os.Getenv("IOIMG_TRANSPORT"))))
	uringOK := cache.UringAvailable()
	prefork := preforkEnabled()

	if forced != "" {
		log.Info("transport forced", "transport", string(forced), "io_uring", uringOK)
		switch forced {
		case Celeris:
			return serveCeleris(ctx, srv, cfg, log)
		case Fiber:
			return serveFiber(ctx, srv, cfg, log, preforkRequested() || !uringOK)
		case NetHTTP:
			return serveNetHTTP(ctx, srv, cfg, log)
		}
		return fmt.Errorf("unknown IOIMG_TRANSPORT %q (celeris, fiber, nethttp)", forced)
	}

	if uringOK {
		err := serveCeleris(ctx, srv, cfg, log)
		if err == nil || !errors.Is(err, errEngineUnavailable) {
			return err
		}
		log.Warn("celeris io_uring engine unavailable, falling back to fiber",
			"error", err)
	}

	if uringOK {
		err := serveFiber(ctx, srv, cfg, log, false)
		if err == nil || !errors.Is(err, errEngineUnavailable) {
			return err
		}
		log.Warn("fiber with io_uring cache reads failed to start, falling back to prefork",
			"error", err)
	}

	return serveFiber(ctx, srv, cfg, log, prefork)
}

// preforkRequested reports whether IOIMG_PREFORK asks for prefork explicitly.
func preforkRequested() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("IOIMG_PREFORK"))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// preforkEnabled is the last-tier default: prefork unless it was turned off.
func preforkEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("IOIMG_PREFORK"))) {
	case "0", "false", "no", "off":
		return false
	}
	return true
}

// errEngineUnavailable marks a startup failure that makes the fallback chain move
// to the next tier. A failure to bind the address is not one of them: retrying the
// same address cannot succeed.
var errEngineUnavailable = errors.New("transport engine unavailable")

// celerisConfig is the celeris engine configuration. Timeouts are set here
// rather than in serveCeleris so a test can pin them: celeris is the default
// transport, and an untimed one is a slowloris target.
func celerisConfig(cfg config.Config) celeris.Config {
	return celeris.Config{
		Addr:               cfg.Addr,
		Engine:             celeris.IOUring,
		DisableMetrics:     true,
		MaxRequestBodySize: 4 << 20,
		ReadTimeout:        readTimeout,
		WriteTimeout:       writeTimeout,
		IdleTimeout:        idleTimeout,
	}
}

func serveCeleris(ctx context.Context, srv *server.Server, cfg config.Config, log *slog.Logger) error {
	// Pinned event loops lose to blocking reads: io_uring cache reads cost 26%
	// there. See ADR-0011.
	srv.SetCacheReadMode(cache.ReadOpenStat)

	// celeris binds the address itself: it rebinds with SO_REUSEPORT so several
	// processes can share one port, and a listener supplied from outside is
	// closed again for that reason.
	app := celeris.New(celerisConfig(cfg))
	app.GET("/*path", srv.CelerisHandler)
	app.HEAD("/*path", srv.CelerisHandler)

	done := make(chan struct{})
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := app.Shutdown(shutdownCtx); err != nil {
			log.Error("celeris shutdown failed", "error", err)
		}
		close(done)
	}()

	// celeris logs the engine's own "listening" line once the ring is up, so this
	// is the selection record, not a readiness claim.
	log.Info("transport selected", "transport", string(Celeris), "engine", "io_uring",
		"cache_read", srv.CacheReadMode(), "addr", cfg.Addr)
	err := app.Start()
	if ctx.Err() != nil {
		<-done
		return nil
	}
	if err != nil {
		return fmt.Errorf("%w: celeris io_uring: %v", errEngineUnavailable, err)
	}
	<-done
	return nil
}

func serveFiber(ctx context.Context, srv *server.Server, cfg config.Config, log *slog.Logger, prefork bool) error {
	mode := cache.ReadOpenStat
	if cache.UringAvailable() && !prefork {
		// Goroutine-per-connection transports win ~28% from io_uring reads.
		mode = cache.ReadUring
	}
	srv.SetCacheReadMode(mode)

	app := fiber.New(fiber.Config{
		AppName:      "ioimg",
		BodyLimit:    4 << 20,
		ReadTimeout:  readTimeout,
		WriteTimeout: writeTimeout,
		IdleTimeout:  idleTimeout,
	})
	app.Use(srv.FiberHandler)

	shutdown := func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := app.ShutdownWithContext(shutdownCtx); err != nil {
			log.Error("fiber shutdown failed", "error", err)
		}
	}

	if prefork {
		log.Warn("serving with prefork: quota, usage and limiter counters are per process",
			"workers", "NumCPU")
		go func() {
			<-ctx.Done()
			shutdown()
		}()
		log.Info("ioimg listening", "transport", string(Fiber), "prefork", true,
			"cache_read", srv.CacheReadMode(), "addr", cfg.Addr)
		return app.Listen(cfg.Addr, fiber.ListenConfig{
			EnablePrefork:         true,
			DisableStartupMessage: true,
		})
	}

	ln, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", cfg.Addr, err)
	}
	go func() {
		<-ctx.Done()
		shutdown()
	}()
	log.Info("ioimg listening", "transport", string(Fiber), "prefork", false,
		"cache_read", srv.CacheReadMode(), "addr", ln.Addr().String())
	if err := app.Listener(ln, fiber.ListenConfig{DisableStartupMessage: true}); err != nil {
		ln.Close()
		if ctx.Err() != nil {
			return nil
		}
		return fmt.Errorf("%w: fiber: %v", errEngineUnavailable, err)
	}
	return nil
}

func serveNetHTTP(ctx context.Context, srv *server.Server, cfg config.Config, log *slog.Logger) error {
	srv.SetCacheReadMode(cache.ReadOpenStat)
	httpSrv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
		MaxHeaderBytes:    maxHeaderBytes,
	}
	errCh := make(chan error, 1)
	go func() {
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()
	log.Info("ioimg listening", "transport", string(NetHTTP),
		"cache_read", srv.CacheReadMode(), "addr", cfg.Addr)
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	return httpSrv.Shutdown(shutdownCtx)
}
