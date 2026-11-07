// Package migrations holds the SQL schema files.
//
// Task 5 owns this package. The files run in name order, one time each.
// Never edit a file after it reaches GitHub; add a new numbered file.
package migrations

import "embed"

// FS holds every *.sql file in this folder.
//
//go:embed *.sql
var FS embed.FS
