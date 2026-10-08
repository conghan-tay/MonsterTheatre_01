// Package migrations embeds the SQL migration files.
//
// Rules, so the same files run on Aurora DSQL (docs/decisions.md, D032):
//   - one schema statement per file;
//   - never mix schema and data changes in one file;
//   - no column drops or type changes without recreating the table.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
