// Package deps keeps every third-party dependency of the project pinned in
// go.mod from the start, so later tasks never have to edit go.mod or go.sum.
//
// Delete each blank import (and eventually this whole package) once real code
// imports the module.
package deps

import (
	_ "github.com/google/uuid"
	_ "github.com/jackc/pgx/v5"
	_ "github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "github.com/pressly/goose/v3"
	_ "golang.org/x/crypto/argon2"
	_ "golang.org/x/term"
)
