// Command atsume downloads manga using the website modules from FMD2.
//
// The modules are GPL-2.0-only and are fetched at runtime rather than
// distributed with this program; see docs/MODULES.md.
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
	"path/filepath"
	"syscall"
	"time"

	"github.com/dyskette/atsume/internal/app"
	"github.com/dyskette/atsume/internal/config"
	"github.com/dyskette/atsume/internal/scraper"
	"github.com/dyskette/atsume/internal/store"
	"github.com/dyskette/atsume/internal/web"
)

func main() {
	// "atsume module …" runs one website module for someone fixing it, and
	// starts nothing else.
	if len(os.Args) > 1 && os.Args[1] == "module" {
		if err := runModule(os.Args[2:], os.Stdout); err != nil {
			if !errors.Is(err, flag.ErrHelp) {
				fmt.Fprintln(os.Stderr, "atsume module:", err)
			}
			os.Exit(1)
		}
		return
	}
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	setupLogging(cfg.LogLevel)

	// Signals cancel the root context, which unwinds the worker pool and any
	// in-flight HTTP request together.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.LibraryDir, 0o755); err != nil {
		return err
	}

	st, err := store.Open(cfg.DataDir)
	if err != nil {
		return err
	}
	defer st.Close()

	reg := scraper.NewRegistry(filepath.Join(cfg.DataDir, "modules"), cfg.ModulesRepo)
	slog.Info("fetching website modules", "repo", cfg.ModulesRepo, "ref", cfg.ModulesRef)
	if err := reg.Fetch(ctx, cfg.ModulesRef); err != nil {
		return err
	}
	slog.Info("modules ready", "count", len(reg.Modules()), "ref", reg.Ref())

	a := app.New(cfg, st, reg)

	// Series followed before a module file was understood to hold several
	// sites refer to the file. Resolving that on every lookup works, but
	// leaving the rows saying one thing and meaning another is how the last
	// identity bug went unnoticed.
	if err := a.RepairModuleKeys(ctx); err != nil {
		slog.Error("could not repair series module keys", "err", err)
	}

	go a.Pool.Run(ctx)
	go a.Scheduler.Run(ctx)

	srv := web.New(a, cfg.Addr)
	errs := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errs <- err
		}
	}()

	select {
	case err := <-errs:
		return err
	case <-ctx.Done():
		slog.Info("shutting down")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

func setupLogging(level string) {
	var l slog.Level
	if err := l.UnmarshalText([]byte(level)); err != nil {
		l = slog.LevelInfo
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: l})))
}
