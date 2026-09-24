// Package migrations embeds the PostgreSQL schema migrations so the Panel
// image can apply them itself (`panel-api migrate`) without a psql client or
// a source checkout on the host.
package migrations

import "embed"

// Files holds every NNNNNN_name.up.sql and .down.sql file of this directory.
//
//go:embed *.sql
var Files embed.FS
