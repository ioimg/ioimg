// Package config loads ioimg configuration from environment variables.
//
// Every value has a safe default so `ioimg serve` runs locally with no config.
// Production deployments are expected to set at least IOIMG_ALLOWLIST and
// IOIMG_SIGNING_KEYS.
package config

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ioimg/ioimg/service/pkg/ops"
	"github.com/ioimg/ioimg/service/pkg/sign"
)

// Config is the immutable runtime configuration.
type Config struct {
	Addr      string
	BaseURL   string
	LogLevel  slog.Level
	LogFormat string // text|json

	Allowlist        []string
	AllowAnyHost     bool
	AllowPrivate     bool
	SignedOnly       bool
	FallbackRedirect bool

	Signing *sign.Signer
	Presets map[string]string

	SourceTimeout  time.Duration
	SourceMaxBytes int64
	// SourceRawMaxBytes bounds raw_ pass-through fetches, which skip the decode
	// and resolution guards but are still buffered in memory.
	SourceRawMaxBytes int64
	SourceUserAgent   string
	// WatermarkURL is the default watermark image used by wm_ when the request
	// does not supply wmu_ (imgproxy's IMGPROXY_WATERMARK_URL).
	WatermarkURL string
	MaxDimension int
	Concurrency  int

	CacheDir      string
	CacheMaxBytes int64

	AccessLog    bool
	MetricsToken string

	L2Enabled bool
	L2        L2Config
	CDN       CDNConfig

	ControlURL         string
	ControlToken       string
	ControlSync        time.Duration
	ControlTimeout     time.Duration
	AllowProjectHeader bool
	ProjectHeader      string

	DisableAVIF bool
	DisableWebP bool
}

// L2Config configures the shared S3-compatible cache.
type L2Config struct {
	Endpoint  string
	Bucket    string
	AccessKey string
	SecretKey string
	Region    string
	Prefix    string
	UseSSL    bool
	UseSSLSet bool
	Timeout   time.Duration
}

// CDNConfig configures best-effort purge fan-out.
type CDNConfig struct {
	Providers       []string
	CloudflareToken string
	CloudflareZone  string
	BunnyKey        string
	FastlyToken     string
	FastlyService   string
}

// Load reads the environment and validates the configuration.
// validateEnvSyntax rejects malformed values before anything reads them: a typo
// such as IOIMG_MAX_DIMENSION=8O48 must fail startup, not silently fall back to
// the default and quietly change production behaviour.
func validateEnvSyntax() error {
	var bad []string
	check := func(keys []string, parse func(string) error) {
		for _, k := range keys {
			v := strings.TrimSpace(os.Getenv(k))
			if v == "" {
				continue
			}
			if err := parse(v); err != nil {
				bad = append(bad, k+"="+v)
			}
		}
	}
	check([]string{"IOIMG_CACHE_MAX_MB", "IOIMG_CONCURRENCY", "IOIMG_MAX_DIMENSION", "IOIMG_SOURCE_MAX_BYTES", "IOIMG_SOURCE_RAW_MAX_BYTES", "IOIMG_AVIF_EFFORT", "IOIMG_AVIF_SPEED", "IOIMG_CELERIS_WORKERS"}, func(v string) error { _, err := strconv.Atoi(v); return err })
	check([]string{}, func(v string) error { _, err := strconv.ParseFloat(v, 64); return err })
	check([]string{"IOIMG_ACCESS_LOG", "IOIMG_ALLOW_ANY_HOST", "IOIMG_ALLOW_PRIVATE", "IOIMG_ALLOW_PROJECT_HEADER", "IOIMG_DISABLE_AVIF", "IOIMG_DISABLE_WEBP", "IOIMG_FALLBACK_REDIRECT", "IOIMG_L2_ENABLED", "IOIMG_L2_USE_SSL", "IOIMG_SIGNATURE_EXPIRY_REQUIRED", "IOIMG_SIGNATURE_INCLUDE_EXP", "IOIMG_SIGNED_ONLY", "IOIMG_OFFLOAD", "IOIMG_JPEG_PROGRESSIVE"}, func(v string) error { _, err := strconv.ParseBool(v); return err })
	check([]string{"IOIMG_CONTROL_SYNC", "IOIMG_CONTROL_TIMEOUT", "IOIMG_L2_TIMEOUT", "IOIMG_SOURCE_TIMEOUT"}, func(v string) error { _, err := time.ParseDuration(v); return err })
	if len(bad) > 0 {
		return fmt.Errorf("invalid environment value(s): %s", strings.Join(bad, ", "))
	}
	return nil
}

func Load() (Config, error) {
	if err := validateEnvSyntax(); err != nil {
		return Config{}, err
	}
	c := Config{
		Addr:              env("IOIMG_ADDR", ":8080"),
		BaseURL:           strings.TrimRight(env("IOIMG_BASE_URL", "http://localhost:8080"), "/"),
		LogFormat:         env("IOIMG_LOG_FORMAT", "text"),
		AllowAnyHost:      envBool("IOIMG_ALLOW_ANY_HOST", false),
		AllowPrivate:      envBool("IOIMG_ALLOW_PRIVATE", false),
		SignedOnly:        envBool("IOIMG_SIGNED_ONLY", false),
		FallbackRedirect:  envBool("IOIMG_FALLBACK_REDIRECT", false),
		SourceTimeout:     envDuration("IOIMG_SOURCE_TIMEOUT", 15*time.Second),
		SourceMaxBytes:    int64(envInt("IOIMG_SOURCE_MAX_BYTES", 25*1024*1024)),
		SourceRawMaxBytes: int64(envInt("IOIMG_SOURCE_RAW_MAX_BYTES", 0)),
		SourceUserAgent:   env("IOIMG_SOURCE_USER_AGENT", "github.com/ioimg/ioimg/service/0.1 (+https://ioimg.net)"),
		WatermarkURL:      env("IOIMG_WATERMARK_URL", ""),
		MaxDimension:      envInt("IOIMG_MAX_DIMENSION", ops.MaxDimension),
		Concurrency:       envInt("IOIMG_CONCURRENCY", 0),
		CacheDir:          env("IOIMG_CACHE_DIR", "./cache"),
		CacheMaxBytes:     int64(envInt("IOIMG_CACHE_MAX_MB", 4096)) * 1024 * 1024,
		AccessLog:         envBool("IOIMG_ACCESS_LOG", false),
		MetricsToken:      os.Getenv("IOIMG_METRICS_TOKEN"),
		DisableAVIF:       envBool("IOIMG_DISABLE_AVIF", false),
		DisableWebP:       envBool("IOIMG_DISABLE_WEBP", false),
	}
	c.Allowlist = splitList(os.Getenv("IOIMG_ALLOWLIST"))
	switch strings.ToLower(env("IOIMG_LOG_LEVEL", "info")) {
	case "debug":
		c.LogLevel = slog.LevelDebug
	case "warn":
		c.LogLevel = slog.LevelWarn
	case "error":
		c.LogLevel = slog.LevelError
	default:
		c.LogLevel = slog.LevelInfo
	}
	if c.LogFormat != "text" && c.LogFormat != "json" {
		return c, fmt.Errorf("IOIMG_LOG_FORMAT must be text or json")
	}
	if c.SourceTimeout <= 0 {
		return c, fmt.Errorf("IOIMG_SOURCE_TIMEOUT must be positive")
	}
	if c.SourceMaxBytes <= 0 {
		return c, fmt.Errorf("IOIMG_SOURCE_MAX_BYTES must be positive")
	}
	if c.MaxDimension <= 0 || c.MaxDimension > ops.MaxDimension {
		return c, fmt.Errorf("IOIMG_MAX_DIMENSION must be 1..%d", ops.MaxDimension)
	}
	if c.Concurrency < 0 {
		return c, fmt.Errorf("IOIMG_CONCURRENCY cannot be negative")
	}
	if c.CacheMaxBytes <= 0 {
		return c, fmt.Errorf("IOIMG_CACHE_MAX_MB must be positive")
	}

	presets, err := ops.ParsePresets(os.Getenv("IOIMG_PRESETS"))
	if err != nil {
		return c, err
	}
	c.Presets = presets

	keys, err := sign.ParseKeys(os.Getenv("IOIMG_SIGNING_KEYS"))
	if err != nil {
		return c, err
	}
	if len(keys) > 0 {
		c.Signing = &sign.Signer{
			Keys:           keys,
			IncludeExp:     envBool("IOIMG_SIGNATURE_INCLUDE_EXP", true),
			ExpiryRequired: envBool("IOIMG_SIGNATURE_EXPIRY_REQUIRED", false),
		}
	}
	c.L2Enabled = envBool("IOIMG_L2_ENABLED", false)
	c.L2 = L2Config{
		Endpoint:  env("IOIMG_L2_ENDPOINT", ""),
		Bucket:    env("IOIMG_L2_BUCKET", "ioimg"),
		AccessKey: os.Getenv("IOIMG_L2_ACCESS_KEY"),
		SecretKey: os.Getenv("IOIMG_L2_SECRET_KEY"),
		Region:    env("IOIMG_L2_REGION", ""),
		Prefix:    env("IOIMG_L2_PREFIX", "ioimg"),
		UseSSL:    envBool("IOIMG_L2_USE_SSL", false),
		UseSSLSet: os.Getenv("IOIMG_L2_USE_SSL") != "",
		Timeout:   envDuration("IOIMG_L2_TIMEOUT", 10*time.Second),
	}
	c.CDN = CDNConfig{
		Providers:       splitList(os.Getenv("IOIMG_CDN_PROVIDERS")),
		CloudflareToken: os.Getenv("IOIMG_CDN_CLOUDFLARE_TOKEN"),
		CloudflareZone:  os.Getenv("IOIMG_CDN_CLOUDFLARE_ZONE"),
		BunnyKey:        os.Getenv("IOIMG_CDN_BUNNY_KEY"),
		FastlyToken:     os.Getenv("IOIMG_CDN_FASTLY_TOKEN"),
		FastlyService:   os.Getenv("IOIMG_CDN_FASTLY_SERVICE"),
	}
	if c.L2Enabled {
		if c.L2.Endpoint == "" || c.L2.Bucket == "" || c.L2.AccessKey == "" || c.L2.SecretKey == "" {
			return c, fmt.Errorf("IOIMG_L2_ENABLED requires endpoint, bucket, access key and secret key")
		}
		if c.L2.Timeout <= 0 {
			return c, fmt.Errorf("IOIMG_L2_TIMEOUT must be positive")
		}
	}
	c.ControlURL = strings.TrimRight(env("IOIMG_CONTROL_URL", ""), "/")
	c.ControlToken = os.Getenv("IOIMG_CONTROL_TOKEN")
	c.ControlSync = envDuration("IOIMG_CONTROL_SYNC", 30*time.Second)
	c.ControlTimeout = envDuration("IOIMG_CONTROL_TIMEOUT", 5*time.Second)
	c.AllowProjectHeader = envBool("IOIMG_ALLOW_PROJECT_HEADER", false)
	c.ProjectHeader = env("IOIMG_PROJECT_HEADER", "X-IOIMG-Project")
	if c.ControlURL != "" {
		if c.ControlToken == "" {
			return c, fmt.Errorf("IOIMG_CONTROL_URL requires IOIMG_CONTROL_TOKEN")
		}
		if c.ControlSync <= 0 || c.ControlTimeout <= 0 {
			return c, fmt.Errorf("IOIMG_CONTROL_SYNC and IOIMG_CONTROL_TIMEOUT must be positive")
		}
	}
	if c.SignedOnly && c.Signing == nil && c.ControlURL == "" {
		return c, fmt.Errorf("IOIMG_SIGNED_ONLY=true requires IOIMG_SIGNING_KEYS")
	}
	if len(c.Allowlist) == 0 && !c.AllowAnyHost {
		// Open-by-default is convenient locally but must be a conscious choice.
		c.AllowAnyHost = true
	}
	return c, nil
}

// AllowedHost reports whether a hostname matches the allowlist. Entries may be
// exact hosts or wildcards such as *.example.com.
func (c Config) AllowedHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if c.AllowAnyHost || len(c.Allowlist) == 0 {
		return true
	}
	for _, entry := range c.Allowlist {
		entry = strings.ToLower(entry)
		if entry == host {
			return true
		}
		if strings.HasPrefix(entry, "*.") {
			suffix := entry[1:] // ".example.com"
			if strings.HasSuffix(host, suffix) && len(host) > len(suffix) {
				return true
			}
		}
	}
	return false
}

// CacheKeyVersion is bumped whenever the transform pipeline changes in a way
// that alters pixels. Old entries then age out naturally.
const CacheKeyVersion = "v1"

func env(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}

func envBool(key string, fallback bool) bool {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return fallback
	}
	return b
}

func envDuration(key string, fallback time.Duration) time.Duration {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return fallback
	}
	return d
}

func splitList(raw string) []string {
	var out []string
	for _, p := range strings.Split(raw, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
