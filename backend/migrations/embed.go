// Package migrations embeds the goose SQL migrations into every binary that
// needs them (the API runs them at boot; the Helm pre-upgrade Job reuses it).
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
