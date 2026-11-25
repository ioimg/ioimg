// Command ioimg runs the image proxy data plane.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/ioimg/ioimg/service/internal/cache"
	"github.com/ioimg/ioimg/service/internal/config"
	"github.com/ioimg/ioimg/service/internal/server"
	"github.com/ioimg/ioimg/service/internal/transport"
)

// version is injected with -ldflags "-X main.version=...".
var version = "0.1.0-dev"

func main() {
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "config error:", err)
		os.Exit(2)
	}
	logger := newLogger(cfg)
	slog.SetDefault(logger)

	srv, err := server.New(cfg, server.Options{Logger: logger, Version: version})
	if err != nil {
		logger.Error("startup failed", "error", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger.Info("ioimg starting",
		"addr", cfg.Addr,
		"version", version,
		"signed_only", cfg.SignedOnly,
		"allow_any_host", cfg.AllowAnyHost,
		"cache_dir", cfg.CacheDir,
		"cache_max_bytes", cfg.CacheMaxBytes,
		"control_url", cfg.ControlURL,
		"concurrency", cfg.Concurrency,
		"io_uring_available", cache.UringAvailable(),
	)

	if err := transport.Serve(ctx, srv, cfg, logger); err != nil {
		logger.Error("server failed", "error", err)
		os.Exit(1)
	}
	logger.Info("shutdown complete")
}

func newLogger(cfg config.Config) *slog.Logger {
	opts := &slog.HandlerOptions{Level: cfg.LogLevel}
	if cfg.LogFormat == "json" {
		return slog.New(slog.NewJSONHandler(os.Stdout, opts))
	}
	return slog.New(slog.NewTextHandler(os.Stdout, opts))
}
