package main

import (
	"context"
	"errors"
)

// runAdmin implements `server admin create-user|reset-password ...`
// (docs/implementation-plan.md section 7).
//
// Stub from T0.9: T5 rewrites this file. Keep the signature; the dispatcher in
// main.go calls it with the arguments after "admin". config.Load, newLogger and
// openDB (setup.go) and newServiceDeps (wire.go) give it configuration, logging,
// the database and the service dependencies. Return usagef(...) for a bad
// command line (exit code 2); any other error exits 1.
func runAdmin(ctx context.Context, args []string) error {
	return errors.New("admin: not implemented yet")
}
