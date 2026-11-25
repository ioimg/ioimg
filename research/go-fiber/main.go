// ioimg PoC versi Go. Dua mode: Fiber (fasthttp) dan net/http standar.
//
//	go run . -mode fiber -prefork
//	go run . -mode http
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/ioimg/ioimg/research/go-fiber/internal/ioimg"

	"github.com/gofiber/fiber/v2"
)

type app struct {
	noCache  bool
	srcDir   string
	cacheDir string
	filter   ioimg.Filter
	stats    *ioimg.Stats
}

func main() {
	mode := flag.String("mode", "fiber", "fiber|http")
	addr := flag.String("addr", ":8080", "listen address")
	src := flag.String("src", "../../artifacts/datasets/src", "direktori sumber")
	cache := flag.String("cache", "../../artifacts/cache/go", "direktori cache")
	prefork := flag.Bool("prefork", false, "aktifkan SO_REUSEPORT prefork (fiber)")
	filter := flag.String("filter", "catmullrom", "catmullrom|bilinear")
	nocache := flag.Bool("nocache", false, "selalu transform, tanpa cache (untuk ukur kapasitas CPU)")
	flag.Parse()

	a := &app{
		srcDir:   *src,
		cacheDir: *cache,
		filter:   ioimg.CatmullRom,
		stats:    &ioimg.Stats{},
		noCache:  *nocache,
	}
	if *filter == "bilinear" {
		a.filter = ioimg.ApproxBiLinear
	}
	for _, d := range []string{a.srcDir, a.cacheDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			log.Fatal(err)
		}
	}

	fmt.Printf("[runtime] NumCPU=%d GOMAXPROCS=%d nocache=%v filter=%s\n", runtime.NumCPU(), runtime.GOMAXPROCS(0), a.noCache, *filter)

	switch *mode {
	case "fiber":
		app := fiber.New(fiber.Config{
			Prefork:               *prefork,
			DisableStartupMessage: true,
			ReadBufferSize:        16 * 1024,
		})
		app.Get("/healthz", func(c *fiber.Ctx) error { return c.SendString("ok") })
		app.Get("/metrics", func(c *fiber.Ctx) error { return c.SendString(a.stats.Render()) })
		app.Get("/:ops/:name", func(c *fiber.Ctx) error {
			body, ct, cacheState, code, errMsg := a.handle(c.Params("ops"), c.Params("name"))
			if code != 200 {
				return c.Status(code).SendString(errMsg)
			}
			c.Set("Content-Type", ct)
			c.Set("Cache-Control", "public, max-age=31536000, immutable")
			c.Set("Vary", "Accept")
			c.Set("X-Ioimg-Cache", cacheState)
			return c.Send(body)
		})
		fmt.Printf("[fiber prefork=%v] listening on %s\n", *prefork, *addr)
		log.Fatal(app.Listen(*addr))
	case "http":
		mux := http.NewServeMux()
		mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte("ok"))
		})
		mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(a.stats.Render()))
		})
		mux.HandleFunc("GET /{ops}/{name}", func(w http.ResponseWriter, r *http.Request) {
			body, ct, cacheState, code, errMsg := a.handle(r.PathValue("ops"), r.PathValue("name"))
			if code != 200 {
				http.Error(w, errMsg, code)
				return
			}
			w.Header().Set("Content-Type", ct)
			w.Header().Set("Content-Length", fmt.Sprint(len(body)))
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			w.Header().Set("Vary", "Accept")
			w.Header().Set("X-Ioimg-Cache", cacheState)
			w.Write(body)
		})
		fmt.Printf("[net/http] listening on %s\n", *addr)
		srv := &http.Server{Addr: *addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
		log.Fatal(srv.ListenAndServe())
	default:
		log.Fatalf("mode tidak dikenal: %s", *mode)
	}
}

func (a *app) handle(opsRaw, name string) (body []byte, ct, cacheState string, code int, errMsg string) {
	a.stats.Requests.Add(1)
	if strings.Contains(name, "..") || strings.Contains(name, "/") {
		a.stats.Errors.Add(1)
		return nil, "", "", 400, "nama sumber tidak valid"
	}
	ops, err := ioimg.ParseOps(opsRaw)
	if err != nil {
		a.stats.Errors.Add(1)
		return nil, "", "", 400, err.Error()
	}

	key := ioimg.CacheKey(ops, name)
	outPath := ioimg.CachePath(a.cacheDir, key, ops.Format)

	if a.noCache {
		a.stats.Misses.Add(1)
		src, err := os.ReadFile(filepath.Join(a.srcDir, name))
		if os.IsNotExist(err) {
			return nil, "", "", 404, "sumber tidak ada"
		}
		if err != nil {
			a.stats.Errors.Add(1)
			return nil, "", "", 500, "src read: " + err.Error()
		}
		t1 := time.Now()
		encoded, err := ioimg.Transform(src, ops, a.filter)
		a.stats.TransformNs.Add(uint64(time.Since(t1)))
		if err != nil {
			a.stats.Errors.Add(1)
			return nil, "", "", 422, "transform: " + err.Error()
		}
		a.stats.BytesOut.Add(uint64(len(encoded)))
		return encoded, ops.Format.ContentType(), "MISS", 200, ""
	}

	t0 := time.Now()
	cached, err := os.ReadFile(outPath)
	a.stats.CacheReadNs.Add(uint64(time.Since(t0)))
	if err == nil {
		a.stats.Hits.Add(1)
		a.stats.BytesOut.Add(uint64(len(cached)))
		return cached, ops.Format.ContentType(), "HIT", 200, ""
	}
	if !os.IsNotExist(err) {
		a.stats.Errors.Add(1)
		return nil, "", "", 500, "cache read: " + err.Error()
	}

	a.stats.Misses.Add(1)
	src, err := os.ReadFile(filepath.Join(a.srcDir, name))
	if os.IsNotExist(err) {
		return nil, "", "", 404, "sumber tidak ada"
	}
	if err != nil {
		a.stats.Errors.Add(1)
		return nil, "", "", 500, "src read: " + err.Error()
	}

	t1 := time.Now()
	encoded, err := ioimg.Transform(src, ops, a.filter)
	a.stats.TransformNs.Add(uint64(time.Since(t1)))
	if err != nil {
		a.stats.Errors.Add(1)
		return nil, "", "", 422, "transform: " + err.Error()
	}

	// tulis atomik: tmp + rename, supaya pembaca tidak pernah lihat file separuh
	tmp := outPath + ".tmp"
	if err := os.WriteFile(tmp, encoded, 0o644); err == nil {
		_ = os.Rename(tmp, outPath)
	}
	a.stats.BytesOut.Add(uint64(len(encoded)))
	return encoded, ops.Format.ContentType(), "MISS", 200, ""
}
