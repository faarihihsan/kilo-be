package main

import (
	"context"
	"errors"
)

// runMedia implements `server media gc [--dry-run]`
// (docs/implementation-plan.md section 7).
//
// Stub from T0.9: T6 rewrites this file. Keep the signature; the dispatcher in
// main.go calls it with the arguments after "media". config.Load, newLogger and
// openDB (setup.go) give it configuration, logging and the database. Return
// usagef(...) for a bad command line (exit code 2); any other error exits 1.
func runMedia(ctx context.Context, args []string) error {
	return errors.New("media: not implemented yet")
}
