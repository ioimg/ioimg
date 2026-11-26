// Optional ACME-backed HTTPS. This path is used only when IOIMG_TLS=auto; the
// default deployment terminates TLS at the CDN edge and never starts a
// certificate manager here.
//
// The celeris native engines extract the address from a listener and rebind
// their own SO_REUSEPORT sockets, so they cannot carry a tls.Listener. HTTPS
// therefore uses the standard library transport, which lets autocert own the
// handshake. Celeris remains the default when TLS is off.

package transport

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"

	"github.com/ioimg/ioimg/service/internal/acmetls"
	"github.com/ioimg/ioimg/service/internal/cache"
	"github.com/ioimg/ioimg/service/internal/config"
	"github.com/ioimg/ioimg/service/internal/server"
)

// serveTLS runs the HTTPS data plane plus plain-HTTP helpers:
//
//   - HTTPS on IOIMG_TLS_ADDR with an autocert certificate per allowed SNI.
//   - IOIMG_ADDR stays a plain HTTP listener serving /healthz, so a load
//     balancer can probe the process without installing a certificate.
//   - IOIMG_TLS_HTTP_ADDR, when set, serves the HTTP-01 challenge (usually
//     port 80). Every other request there is a 308 redirect to HTTPS.
func serveTLS(ctx context.Context, srv *server.Server, cfg config.Config, log *slog.Logger) error {
	if cfg.TLS.Addr == cfg.Addr {
		return fmt.Errorf("IOIMG_TLS_ADDR and IOIMG_ADDR must differ: HTTPS and the plain health listener cannot share one socket")
	}
	mgr, err := acmetls.New(acmetls.Options{
		CacheDir: cfg.TLS.CacheDir,
		Email:    cfg.TLS.Email,
		CA:       cfg.TLS.CA,
		Staging:  cfg.TLS.Staging,
		Hosts:    cfg.TLS.Hosts,
		Allowed:  srv.AllowedTLSHost,
	})
	if err != nil {
		return err
	}

	// net/http is the only transport that accepts a tls.Listener; see the file
	// comment. The cache read mode matches the other goroutine-per-connection
	// transports.
	srv.SetCacheReadMode(cache.ReadOpenStat)

	httpsSrv := &http.Server{
		Handler:           srv.Handler(),
		TLSConfig:         mgr.TLSConfig(),
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
		MaxHeaderBytes:    maxHeaderBytes,
	}
	httpsLn, err := net.Listen("tcp", cfg.TLS.Addr)
	if err != nil {
		return fmt.Errorf("listen https %s: %w", cfg.TLS.Addr, err)
	}
	tlsLn := tls.NewListener(httpsLn, httpsSrv.TLSConfig)

	servers := []*http.Server{httpsSrv}
	listeners := []net.Listener{tlsLn}
	errCh := make(chan error, 4)
	go func() {
		if err := httpsSrv.Serve(tlsLn); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("https: %w", err)
		}
	}()

	type plainListener struct {
		addr   string
		health bool
	}
	// IOIMG_ADDR keeps /healthz alive for load balancers; a separate
	// IOIMG_TLS_HTTP_ADDR is a pure ACME listener and redirects everything else.
	plainAddrs := []plainListener{{addr: cfg.Addr, health: true}}
	if cfg.TLS.HTTPAddr != "" && cfg.TLS.HTTPAddr != cfg.Addr {
		plainAddrs = append(plainAddrs, plainListener{addr: cfg.TLS.HTTPAddr})
	}
	for _, target := range plainAddrs {
		addr := target.addr
		if addr == cfg.TLS.Addr {
			shutdownServers(servers, listeners)
			return fmt.Errorf("plain HTTP address %q collides with IOIMG_TLS_ADDR", addr)
		}
		plainSrv := &http.Server{
			Handler:           newPlainHandler(mgr, srv.Handler(), cfg.TLS.Addr, target.health),
			ReadHeaderTimeout: readHeaderTimeout,
			ReadTimeout:       readTimeout,
			WriteTimeout:      writeTimeout,
			IdleTimeout:       idleTimeout,
			MaxHeaderBytes:    maxHeaderBytes,
		}
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			shutdownServers(servers, listeners)
			return fmt.Errorf("listen http %s: %w", addr, err)
		}
		servers = append(servers, plainSrv)
		listeners = append(listeners, ln)
		go func() {
			if err := plainSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errCh <- fmt.Errorf("http %s: %w", addr, err)
			}
		}()
		log.Info("plain HTTP listener", "addr", addr, "healthz", "/healthz", "http01", true)
	}

	log.Info("ioimg listening", "transport", string(NetHTTP), "tls", true,
		"addr", cfg.TLS.Addr, "cache_read", srv.CacheReadMode(),
		"acme_staging", cfg.TLS.Staging)

	select {
	case err := <-errCh:
		shutdownServers(servers, listeners)
		return err
	case <-ctx.Done():
	}
	shutdownServers(servers, listeners)
	return nil
}

// newPlainHandler serves the HTTP-01 challenge and, on the main HTTP address,
// /healthz. The health endpoint is intentionally the only application route
// exposed over plaintext: everything else is a permanent redirect to HTTPS.
// The dedicated IOIMG_TLS_HTTP_ADDR listener passes health=false so every
// non-challenge request there is redirected, as documented.
func newPlainHandler(mgr *acmetls.Manager, app http.Handler, httpsAddr string, health bool) http.Handler {
	fallback := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if health && r.URL.Path == "/healthz" {
			app.ServeHTTP(w, r)
			return
		}
		redirectToHTTPS(w, r, httpsAddr)
	})
	return mgr.HTTPHandler(fallback)
}

// redirectToHTTPS sends a plaintext request to the HTTPS listener. The port is
// omitted when HTTPS runs on 443, so the common case produces a clean URL.
func redirectToHTTPS(w http.ResponseWriter, r *http.Request, httpsAddr string) {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		host = "[" + host + "]"
	}
	target := "https://" + host + httpsPortSuffix(httpsAddr) + r.URL.RequestURI()
	http.Redirect(w, r, target, http.StatusPermanentRedirect)
}

func httpsPortSuffix(addr string) string {
	_, port, err := net.SplitHostPort(addr)
	if err != nil || port == "" || port == "443" {
		return ""
	}
	return ":" + port
}

func shutdownServers(servers []*http.Server, listeners []net.Listener) {
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	for _, s := range servers {
		_ = s.Shutdown(shutdownCtx)
	}
	for _, ln := range listeners {
		_ = ln.Close()
	}
}
