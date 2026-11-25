// Command ioimg runs the image proxy data plane.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ioimg/ioimg/service/internal/config"
	"github.com/ioimg/ioimg/service/internal/server"
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

	httpSrv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      120 * time.Second,
		IdleTimeout:       90 * time.Second,
		MaxHeaderBytes:    64 << 10,
	}

	logger.Info("ioimg listening",
		"addr", cfg.Addr,
		"version", version,
		"signed_only", cfg.SignedOnly,
		"allow_any_host", cfg.AllowAnyHost,
		"cache_dir", cfg.CacheDir,
		"cache_max_bytes", cfg.CacheMaxBytes,
		"control_url", cfg.ControlURL,
		"concurrency", cfg.Concurrency,
	)

	errCh := make(chan error, 1)
	go func() {
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	select {
	case err := <-errCh:
		logger.Error("server failed", "error", err)
		os.Exit(1)
	case sig := <-sigCh:
		logger.Info("shutting down", "signal", sig.String())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(ctx); err != nil {
		logger.Error("shutdown failed", "error", err)
		os.Exit(1)
	}
}

func newLogger(cfg config.Config) *slog.Logger {
	opts := &slog.HandlerOptions{Level: cfg.LogLevel}
	if cfg.LogFormat == "json" {
		return slog.New(slog.NewJSONHandler(os.Stdout, opts))
	}
	return slog.New(slog.NewTextHandler(os.Stdout, opts))
}
