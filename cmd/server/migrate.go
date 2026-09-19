package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"text/tabwriter"

	"workout-tracker-be/internal/config"
	"workout-tracker-be/internal/store"
)

// runMigrate implements `server migrate up|status|down`. Migrations are
// forward-only in production: `down` is refused there, `up` runs before
// `serve` (see deploy/workout-tracker.service) and `serve` refuses to start on
// a schema that is behind.
func runMigrate(ctx context.Context, args []string, stdout io.Writer) error {
	if len(args) != 1 {
		return usagef("migrate needs exactly one of: up, status, down")
	}
	sub := args[0]
	if sub != "up" && sub != "status" && sub != "down" {
		return usagef("unknown migrate command %q (want up, status or down)", sub)
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	// The store logs each applied migration through slog.Default.
	slog.SetDefault(newLogger(os.Stderr, cfg.LogLevel))

	switch sub {
	case "up":
		return store.MigrateUp(ctx, cfg.DatabaseURL)
	case "down":
		if cfg.Env.IsProduction() {
			return fmt.Errorf("migrate down is for development only: migrations are forward-only in %s (set APP_ENV=%s to roll back a local database)",
				config.EnvProduction, config.EnvDevelopment)
		}
		return store.MigrateDown(ctx, cfg.DatabaseURL)
	default:
		st, err := store.MigrateStatus(ctx, cfg.DatabaseURL)
		if err != nil {
			return err
		}
		return printStatus(stdout, st)
	}
}

func printStatus(w io.Writer, st store.Status) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "VERSION\tSTATE\tFILE")
	for _, m := range st.Migrations {
		state := "pending"
		if m.Applied {
			state = "applied"
		}
		fmt.Fprintf(tw, "%d\t%s\t%s\n", m.Version, state, m.Name)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	_, err := fmt.Fprintf(w, "database at version %d, binary at version %d\n", st.Current, st.Latest)
	return err
}
