// Package config loads settings from the environment.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
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

	// Workers is the number of chapters downloaded concurrently.
	Workers int
	// HostConcurrency and HostRPS bound requests to any single site.
	HostConcurrency int
	HostRPS         float64

	// FlaresolverrURL, when set, is used to solve anti-bot challenges.
	FlaresolverrURL string

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
		SecretKey:       env("ATSUME_SECRET_KEY", ""),
		LogLevel:        env("ATSUME_LOG_LEVEL", "info"),
	}

	var err error
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
