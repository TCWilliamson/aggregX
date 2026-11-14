// Package migrate applies the SQL files in migrations/ in order, once each.
package migrate

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"sort"
	"strings"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/jackc/pgx/v5/pgxpool"

	"iotp/migrations"
)

func files(dir string) ([]string, error) {
	names, err := fs.Glob(migrations.FS, dir+"/*.sql")
	if err != nil {
		return nil, fmt.Errorf("glob %s: %w", dir, err)
	}
	sort.Strings(names)
	return names, nil
}

// Postgres applies migrations/postgres/*.sql. Each file manages its own transaction.
func Postgres(ctx context.Context, pg *pgxpool.Pool) error {
	if _, err := pg.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}
	names, err := files("postgres")
	if err != nil {
		return err
	}
	for _, n := range names {
		var exists bool
		if err := pg.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)`, n).Scan(&exists); err != nil {
			return fmt.Errorf("%s: check applied: %w", n, err)
		}
		if exists {
			continue
		}
		sql, err := fs.ReadFile(migrations.FS, n)
		if err != nil {
			return fmt.Errorf("%s: %w", n, err)
		}
		// Simple protocol (no args) allows multiple statements per Exec.
		if _, err := pg.Exec(ctx, string(sql)); err != nil {
			return fmt.Errorf("%s: %w", n, err)
		}
		if _, err := pg.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, n); err != nil {
			return fmt.Errorf("%s: record applied: %w", n, err)
		}
		slog.Info("applied migration", "file", n)
	}
	return nil
}

// ClickHouse applies migrations/clickhouse/*.sql, one statement at a time.
// Statements are separated by a line ending in ';'. Files must be idempotent
// (IF NOT EXISTS), because ClickHouse DDL is not transactional.
func ClickHouse(ctx context.Context, ch driver.Conn) error {
	names, err := files("clickhouse")
	if err != nil {
		return err
	}
	for _, n := range names {
		sql, err := fs.ReadFile(migrations.FS, n)
		if err != nil {
			return fmt.Errorf("%s: %w", n, err)
		}
		for _, stmt := range splitStatements(string(sql)) {
			if err := ch.Exec(ctx, stmt); err != nil {
				return fmt.Errorf("%s: %w\n%s", n, err, stmt)
			}
		}
		slog.Info("applied clickhouse migration", "file", n)
	}
	return nil
}

func splitStatements(sql string) []string {
	var out []string
	var cur strings.Builder
	for _, line := range strings.Split(sql, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "--") || t == "" {
			continue
		}
		cur.WriteString(line)
		cur.WriteString("\n")
		if strings.HasSuffix(t, ";") {
			s := strings.TrimSuffix(strings.TrimSpace(cur.String()), ";")
			out = append(out, s)
			cur.Reset()
		}
	}
	return out
}
