// Command server is the single binary of the workout tracker: the HTTP API and
// its admin tooling (docs/implementation-plan.md sections 2 and 7).
//
// Files:
//
//	main.go    subcommand dispatcher and exit codes
//	setup.go   what every subcommand needs first: config, logger, database
//	serve.go   `serve`: startup checks, HTTP server, graceful shutdown
//	migrate.go `migrate up|status|down`
//	wire.go    the composition root: stores, services, handlers, router
//	admin.go   `admin ...` (create-user, reset-password)
//	media.go   `media gc`
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"
)

// version is set at build time (-ldflags "-X main.version=..."), see the
// Makefile.
var version = "dev"

const (
	exitOK    = 0
	exitError = 1 // the command failed
	exitUsage = 2 // unknown command or bad arguments
)

const usageText = `Usage: server <command> [arguments]

Commands:
  serve                 run the HTTP server
  migrate up            apply pending database migrations
  migrate status        list the migrations and whether they are applied
  migrate down          roll back the latest migration (development only)
  admin create-user     create a user, for example the first admin
  admin reset-password  reset a user's password and revoke their tokens
  media gc              delete image files no exercise refers to
  version               print the version
  help                  print this text

Configuration comes from environment variables, see deploy/env.example.
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	// After the first signal, restore the default behaviour: a second
	// Ctrl-C kills a process that is stuck in shutdown.
	context.AfterFunc(ctx, stop)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

// usageError is returned for a command line that cannot be run: run prints it
// with the usage text and exits 2.
type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }

func usagef(format string, args ...any) error {
	return &usageError{msg: fmt.Sprintf(format, args...)}
}

// run executes the command line (without the program name) and returns the
// process exit code. Subcommands load their own configuration; help and version
// need none.
func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usageText)
		return exitUsage
	}
	cmd, rest := args[0], args[1:]

	var err error
	switch cmd {
	case "serve":
		err = runServe(ctx, rest)
	case "migrate":
		err = runMigrate(ctx, rest, stdout)
	case "admin":
		err = runAdmin(ctx, rest)
	case "media":
		err = runMedia(ctx, rest)
	case "version", "-v", "--version":
		fmt.Fprintln(stdout, versionString())
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usageText)
	default:
		err = usagef("unknown command %q", cmd)
	}

	var usage *usageError
	switch {
	case err == nil:
		return exitOK
	case errors.As(err, &usage):
		fmt.Fprintf(stderr, "server: %v\n\n%s", err, usageText)
		return exitUsage
	default:
		fmt.Fprintf(stderr, "server: %v\n", err)
		return exitError
	}
}

// versionString is "<version> <go version>", plus the commit when the binary
// was built from a git checkout.
func versionString() string {
	s := "workout-tracker " + version
	if info, ok := debug.ReadBuildInfo(); ok {
		s += " " + info.GoVersion
		for _, kv := range info.Settings {
			if kv.Key == "vcs.revision" && len(kv.Value) >= 7 {
				s += " " + kv.Value[:7]
			}
		}
	}
	return s
}
