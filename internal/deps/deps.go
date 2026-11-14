// Package deps opens connections to the stateful services and exposes health checks.
package deps

import (
	"context"
	"errors"
	"fmt"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/twmb/franz-go/pkg/kgo"

	"iotp/internal/config"
	"iotp/internal/health"
)

// Deps holds the clients a run mode opened; unused ones are nil.
type Deps struct {
	PG    *pgxpool.Pool
	Redis *redis.Client
	Kafka *kgo.Client
	CH    driver.Conn
}

// Need says which services a run mode uses.
type Need struct {
	PG, Redis, Kafka, CH bool
}

// Open connects to the services a run mode needs. Connections are lazy where the
// client library allows, so a missing service shows up in /readyz, not as a crash.
func Open(ctx context.Context, c *config.Config, need Need) (*Deps, error) {
	d := &Deps{}
	var err error
	if need.PG {
		if d.PG, err = pgxpool.New(ctx, c.PGDSN); err != nil {
			return nil, fmt.Errorf("postgres: %w", err)
		}
	}
	if need.Redis {
		d.Redis = redis.NewClient(&redis.Options{Addr: c.RedisAddr})
	}
	if need.Kafka {
		if d.Kafka, err = kgo.NewClient(kgo.SeedBrokers(c.Brokers...)); err != nil {
			return nil, errors.Join(fmt.Errorf("redpanda: %w", err), d.Close())
		}
	}
	if need.CH {
		if d.CH, err = clickhouse.Open(&clickhouse.Options{
			Addr: []string{c.CHAddr},
			Auth: clickhouse.Auth{Database: "default", Username: c.CHUser, Password: c.CHPassword},
		}); err != nil {
			return nil, errors.Join(fmt.Errorf("clickhouse: %w", err), d.Close())
		}
	}
	return d, nil
}

// Checks returns one readiness check per opened client.
func (d *Deps) Checks() []health.Check {
	var cs []health.Check
	if d.PG != nil {
		cs = append(cs, health.Check{Name: "postgres", Fn: d.PG.Ping})
	}
	if d.Redis != nil {
		cs = append(cs, health.Check{Name: "redis", Fn: func(ctx context.Context) error { return d.Redis.Ping(ctx).Err() }})
	}
	if d.Kafka != nil {
		cs = append(cs, health.Check{Name: "redpanda", Fn: d.Kafka.Ping})
	}
	if d.CH != nil {
		cs = append(cs, health.Check{Name: "clickhouse", Fn: d.CH.Ping})
	}
	return cs
}

// Close closes every opened client and returns the joined close errors.
func (d *Deps) Close() error {
	var errs []error
	if d.PG != nil {
		d.PG.Close()
	}
	if d.Redis != nil {
		if err := d.Redis.Close(); err != nil {
			errs = append(errs, fmt.Errorf("redis close: %w", err))
		}
	}
	if d.Kafka != nil {
		d.Kafka.Close()
	}
	if d.CH != nil {
		if err := d.CH.Close(); err != nil {
			errs = append(errs, fmt.Errorf("clickhouse close: %w", err))
		}
	}
	return errors.Join(errs...)
}
