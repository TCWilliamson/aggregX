// Package migrations embeds the SQL migration files.
package migrations

import "embed"

//go:embed postgres/*.sql clickhouse/*.sql
var FS embed.FS
