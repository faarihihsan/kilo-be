// Package migrations embeds the goose SQL migrations so the single server
// binary carries its own schema. internal/store applies them (MigrateUp).
//
// Rules (docs/implementation-plan.md section 6): never edit an applied
// migration, add the next number instead. The schema is defined by
// docs/data-model.md. Enum CHECK lists must match internal/domain, which
// schema_test.go verifies against a live database.
package migrations

import "embed"

// FS holds every *.sql file in this directory, at the root of the FS.
//
//go:embed *.sql
var FS embed.FS
