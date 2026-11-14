// Package deps opens connections to the stateful services and exposes health checks.
package deps

import (
	"context"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/twmb/franz-go/pkg/kgo"

	"iotp/internal/config"
	"iotp/internal/health"
)

type Deps struct {
	PG    *pgxpool.Pool
	Redis *redis.Client
	Kafka *kgo.Client
	CH    driver.Conn
}

// Open connects to the services a run mode needs. Connections are lazy where the
// client library allows, so a missing service shows up in /readyz, not as a crash.
func Open(ctx context.Context, c *config.Config, pg, rds, kafka, ch bool) (*Deps, error) {
	d := &Deps{}
	var err error
	if pg {
		if d.PG, err = pgxpool.New(ctx, c.PGDSN); err != nil {
			return nil, err
		}
	}
	if rds {
		d.Redis = redis.NewClient(&redis.Options{Addr: c.RedisAddr})
	}
	if kafka {
		if d.Kafka, err = kgo.NewClient(kgo.SeedBrokers(c.Brokers...)); err != nil {
			return nil, err
		}
	}
	if ch {
		if d.CH, err = clickhouse.Open(&clickhouse.Options{
			Addr: []string{c.CHAddr},
			Auth: clickhouse.Auth{Database: "default", Username: c.CHUser, Password: c.CHPassword},
		}); err != nil {
			return nil, err
		}
	}
	return d, nil
}

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

func (d *Deps) Close() {
	if d.PG != nil {
		d.PG.Close()
	}
	if d.Redis != nil {
		d.Redis.Close()
	}
	if d.Kafka != nil {
		d.Kafka.Close()
	}
	if d.CH != nil {
		d.CH.Close()
	}
}
