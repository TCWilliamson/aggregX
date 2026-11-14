// Package config reads all settings from environment variables (design: config in env vars).
package config

import (
	"fmt"
	"os"
	"strings"
)

// Config holds every setting a run mode can use.
type Config struct {
	RunMode    string // ingest | core | workers | migrate
	HTTPAddr   string
	Cell       string
	NodeID     string
	PGDSN      string
	RedisAddr  string
	Brokers    []string
	CHAddr     string
	CHDatabase string
	CHUser     string
	CHPassword string
	Topic      string
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// Load reads the environment for the given run mode and rejects unknown modes.
func Load(mode string) (*Config, error) {
	host, _ := os.Hostname()
	c := &Config{
		RunMode:    mode,
		HTTPAddr:   env("HTTP_ADDR", ":8080"),
		Cell:       env("CELL", "uk1"),
		NodeID:     env("NODE_ID", host),
		PGDSN:      env("PG_DSN", "postgres://iotp:iotp@postgres:5432/iotp?sslmode=disable"),
		RedisAddr:  env("REDIS_ADDR", "redis:6379"),
		Brokers:    strings.Split(env("KAFKA_BROKERS", "redpanda:9092"), ","),
		CHAddr:     env("CLICKHOUSE_ADDR", "clickhouse:9000"),
		CHDatabase: env("CLICKHOUSE_DB", "iot"),
		CHUser:     env("CLICKHOUSE_USER", "iotp"),
		CHPassword: env("CLICKHOUSE_PASSWORD", "iotp"),
		Topic:      env("INGEST_TOPIC", "iot.ingest.v1"),
	}
	switch mode {
	case "ingest", "core", "workers", "migrate":
	default:
		return nil, fmt.Errorf("unknown run mode %q (want ingest, core, workers or migrate)", mode)
	}
	return c, nil
}
