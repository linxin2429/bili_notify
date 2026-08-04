package migrations

import "embed"

// FS holds numbered goose SQL migrations.
//
//go:embed *.sql
var FS embed.FS
