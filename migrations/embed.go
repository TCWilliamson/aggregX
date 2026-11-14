// Package migrations embeds the SQL migration files.
package migrations

import "embed"

// FS holds migrations/postgres/*.sql and migrations/clickhouse/*.sql.
//
//go:embed postgres/*.sql clickhouse/*.sql
var FS embed.FS
