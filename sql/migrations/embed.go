package migrations

import "embed"

// FS contains Setika core database migrations.
//
//go:embed *.sql
var FS embed.FS
