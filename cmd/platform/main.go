// Command platform is the single binary with three run modes (design doc,
// "One codebase, three run modes") plus a one-shot migrate mode.
//
//	platform ingest | core | workers | migrate
//
// The run mode may also be given as RUN_MODE.
package main

import (
	"context"
	"errors"
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
		d, err := deps.Open(ctx, cfg, true, false, false, true)
		if err != nil {
			return err
		}
		defer d.Close()
		if err := migrate.Postgres(ctx, d.PG); err != nil {
			return err
		}
		return migrate.ClickHouse(ctx, d.CH)
	case "ingest":
		// Ingest talks only to Redis and Redpanda (design doc, spec 03 §8).
		d, err := deps.Open(ctx, cfg, false, true, true, false)
		if err != nil {
			return err
		}
		defer d.Close()
		mux := http.NewServeMux()
		health.Register(mux, d.Checks())
		(&ingest.Server{Cell: cfg.Cell}).Register(mux)
		return serve(ctx, cfg.HTTPAddr, mux)
	case "core":
		d, err := deps.Open(ctx, cfg, true, true, false, true)
		if err != nil {
			return err
		}
		defer d.Close()
		mux := http.NewServeMux()
		health.Register(mux, d.Checks())
		mux.HandleFunc("GET /v1/version", func(w http.ResponseWriter, _ *http.Request) {
			w.Write([]byte(`{"service":"core","cell":"` + cfg.Cell + `"}`))
		})
		// TODO(phase0/1): registry, claims, directory publisher, query layer, API.
		return serve(ctx, cfg.HTTPAddr, mux)
	case "workers":
		d, err := deps.Open(ctx, cfg, true, true, true, true)
		if err != nil {
			return err
		}
		defer d.Close()
		mux := http.NewServeMux()
		health.Register(mux, d.Checks())
		// TODO(phase0): wk-store and wk-archive consumers (spec 06 §4, §10), rollup worker.
		return serve(ctx, cfg.HTTPAddr, mux)
	}
	return errors.New("unreachable")
}

func serve(ctx context.Context, addr string, h http.Handler) error {
	srv := &http.Server{Addr: addr, Handler: h, ReadHeaderTimeout: 5 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	slog.Info("listening", "addr", addr)
	select {
	case <-ctx.Done():
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(sctx)
	case err := <-errc:
		return err
	}
}
