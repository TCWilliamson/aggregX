// Command platform is the single binary with three run modes (design doc,
// "One codebase, three run modes") plus a one-shot migrate mode.
//
//	platform ingest | core | workers | migrate
//
// The run mode may also be given as RUN_MODE.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"iotp/internal/config"
	"iotp/internal/deps"
	"iotp/internal/health"
	"iotp/internal/ingest"
	"iotp/internal/migrate"
)

func main() {
	mode := os.Getenv("RUN_MODE")
	if len(os.Args) > 1 {
		mode = os.Args[1]
	}
	cfg, err := config.Load(mode)
	if err != nil {
		slog.Error("config", "err", err)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, cfg); err != nil {
		slog.Error("exit", "mode", mode, "err", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, cfg *config.Config) error {
	switch cfg.RunMode {
	case "migrate":
		return withDeps(ctx, cfg, deps.Need{PG: true, CH: true}, func(d *deps.Deps) error {
			if err := migrate.Postgres(ctx, d.PG); err != nil {
				return fmt.Errorf("migrate postgres: %w", err)
			}
			if err := migrate.ClickHouse(ctx, d.CH); err != nil {
				return fmt.Errorf("migrate clickhouse: %w", err)
			}
			return nil
		})
	case "ingest":
		// Ingest talks only to Redis and Redpanda (design doc, spec 03 §8).
		return withDeps(ctx, cfg, deps.Need{Redis: true, Kafka: true}, func(d *deps.Deps) error {
			mux := http.NewServeMux()
			health.Register(mux, d.Checks())
			(&ingest.Server{Cell: cfg.Cell}).Register(mux)
			return serve(ctx, cfg.HTTPAddr, mux)
		})
	case "core":
		return withDeps(ctx, cfg, deps.Need{PG: true, Redis: true, CH: true}, func(d *deps.Deps) error {
			mux := http.NewServeMux()
			health.Register(mux, d.Checks())
			mux.HandleFunc("GET /v1/version", func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if err := json.NewEncoder(w).Encode(map[string]string{"service": "core", "cell": cfg.Cell}); err != nil {
					slog.Debug("version write", "err", err)
				}
			})
			// TODO(phase0/1): registry, claims, directory publisher, query layer, API.
			return serve(ctx, cfg.HTTPAddr, mux)
		})
	case "workers":
		return withDeps(ctx, cfg, deps.Need{PG: true, Redis: true, Kafka: true, CH: true}, func(d *deps.Deps) error {
			mux := http.NewServeMux()
			health.Register(mux, d.Checks())
			// TODO(phase0): wk-store and wk-archive consumers (spec 06 §4, §10), rollup worker.
			return serve(ctx, cfg.HTTPAddr, mux)
		})
	}
	return fmt.Errorf("unknown run mode %q", cfg.RunMode)
}

// withDeps opens the clients a run mode needs, runs fn, and closes them.
func withDeps(ctx context.Context, cfg *config.Config, need deps.Need, fn func(*deps.Deps) error) (err error) {
	d, err := deps.Open(ctx, cfg, need)
	if err != nil {
		return fmt.Errorf("open dependencies: %w", err)
	}
	defer func() {
		if cerr := d.Close(); cerr != nil {
			err = errors.Join(err, cerr)
		}
	}()
	return fn(d)
}

func serve(ctx context.Context, addr string, h http.Handler) error {
	srv := &http.Server{Addr: addr, Handler: h, ReadHeaderTimeout: 5 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	slog.Info("listening", "addr", addr)
	select {
	case <-ctx.Done():
		// The parent is already cancelled; keep its values but give shutdown its own deadline.
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(sctx); err != nil {
			return fmt.Errorf("shutdown: %w", err)
		}
		return nil
	case err := <-errc:
		return fmt.Errorf("listen %s: %w", addr, err)
	}
}
