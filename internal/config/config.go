// Package config loads settings from the environment.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is the full runtime configuration. Every field has a working default
// so that `atsume` runs with no environment set at all.
type Config struct {
	// Addr is the listen address for the web UI.
	Addr string
	// DataDir holds the database and the fetched module checkout.
	DataDir string
	// LibraryDir is where CBZ files are written, typically a directory a
	// library server such as Komga also scans.
	LibraryDir string

	// ModulesRepo and ModulesRef pin the upstream module source. The modules are
	// GPL-2.0-only and are fetched at runtime rather than distributed with this
	// program; see docs/MODULES.md.
	ModulesRepo string
	ModulesRef  string

	// CheckInterval is how often a subscribed series is re-checked for new
	// chapters. Zero disables automatic checking entirely.
	CheckInterval time.Duration
	// CheckBatch caps how many series one scheduler tick enqueues, so a large
	// library spreads its checks out instead of flooding the queue at boot.
	CheckBatch int
	// AutoDownload queues newly discovered chapters for download. With it off,
	// a check only records that they exist.
	AutoDownload bool

	// Workers is the number of chapters downloaded concurrently.
	Workers int
	// HostConcurrency and HostRPS bound requests to any single site.
	HostConcurrency int
	HostRPS         float64

	// FlaresolverrURL, when set, is used to solve anti-bot challenges.
	FlaresolverrURL string
	// NotifyURL, when set, receives a JSON POST when a check finds new
	// chapters.
	NotifyURL string

	// SecretKey encrypts stored module credentials. Empty disables the feature
	// and module logins are refused rather than stored in the clear.
	SecretKey string

	LogLevel string
}

// Load reads the configuration from the environment.
//
// Every variable also accepts a _FILE suffix naming a file to read the value
// from, which is how container secrets (podman, docker, kubernetes) are
// normally delivered. This keeps atsume independent of any particular secret
// store.
func Load() (*Config, error) {
	c := &Config{
		Addr:            env("ATSUME_ADDR", ":8080"),
		DataDir:         env("ATSUME_DATA_DIR", "/data"),
		LibraryDir:      env("ATSUME_LIBRARY_DIR", "/library"),
		ModulesRepo:     env("ATSUME_MODULES_REPO", "https://github.com/dazedcat19/FMD2.git"),
		ModulesRef:      env("ATSUME_MODULES_REF", "master"),
		FlaresolverrURL: env("ATSUME_FLARESOLVERR_URL", ""),
		NotifyURL:       env("ATSUME_NOTIFY_URL", ""),
		SecretKey:       env("ATSUME_SECRET_KEY", ""),
		LogLevel:        env("ATSUME_LOG_LEVEL", "info"),
		AutoDownload:    envBool("ATSUME_AUTO_DOWNLOAD", true),
	}

	var err error
	if c.CheckInterval, err = envDuration("ATSUME_CHECK_INTERVAL", 6*time.Hour); err != nil {
		return nil, err
	}
	if c.CheckBatch, err = envInt("ATSUME_CHECK_BATCH", 10); err != nil {
		return nil, err
	}
	if c.Workers, err = envInt("ATSUME_WORKERS", 3); err != nil {
		return nil, err
	}
	if c.HostConcurrency, err = envInt("ATSUME_HOST_CONCURRENCY", 2); err != nil {
		return nil, err
	}
	if c.HostRPS, err = envFloat("ATSUME_HOST_RPS", 1.0); err != nil {
		return nil, err
	}

	if c.Workers < 1 {
		return nil, fmt.Errorf("ATSUME_WORKERS must be at least 1")
	}
	if c.HostConcurrency < 1 {
		return nil, fmt.Errorf("ATSUME_HOST_CONCURRENCY must be at least 1")
	}
	if c.HostRPS <= 0 {
		return nil, fmt.Errorf("ATSUME_HOST_RPS must be greater than 0")
	}
	if c.CheckInterval < 0 {
		return nil, fmt.Errorf("ATSUME_CHECK_INTERVAL cannot be negative")
	}
	// A very short interval would hammer every tracked site; the sites are not
	// ours and new chapters do not appear minute to minute.
	if c.CheckInterval > 0 && c.CheckInterval < 15*time.Minute {
		return nil, fmt.Errorf("ATSUME_CHECK_INTERVAL must be at least 15m, or 0 to disable")
	}
	if c.CheckBatch < 1 {
		return nil, fmt.Errorf("ATSUME_CHECK_BATCH must be at least 1")
	}
	return c, nil
}

// env reads a variable, honouring the _FILE indirection.
func env(key, def string) string {
	if path := os.Getenv(key + "_FILE"); path != "" {
		if b, err := os.ReadFile(path); err == nil {
			return strings.TrimSpace(string(b))
		}
	}
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) (int, error) {
	raw := env(key, "")
	if raw == "" {
		return def, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return n, nil
}

func envBool(key string, def bool) bool {
	switch strings.ToLower(env(key, "")) {
	case "":
		return def
	case "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

func envDuration(key string, def time.Duration) (time.Duration, error) {
	raw := env(key, "")
	if raw == "" {
		return def, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return d, nil
}

func envFloat(key string, def float64) (float64, error) {
	raw := env(key, "")
	if raw == "" {
		return def, nil
	}
	f, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return f, nil
}
