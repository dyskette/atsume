// Package migrations embeds the schema so that a release is a single binary.
package migrations

import "embed"

// FS holds the goose migration files.
//
//go:embed *.sql
var FS embed.FS
