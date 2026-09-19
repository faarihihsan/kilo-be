package main

import (
	"context"
	"flag"
	"io"
	"os"

	"github.com/google/uuid"

	"workout-tracker-be/internal/config"
	"workout-tracker-be/internal/domain"
	"workout-tracker-be/internal/media"
	"workout-tracker-be/internal/store"
)

// runMedia implements `server media gc [--dry-run]`
// (docs/implementation-plan.md section 7). The dispatcher in main.go calls it
// with the arguments after "media".
//
// gc loads the referenced image set from the exercises table and removes the
// files under MEDIA_DIR that no row points at, plus leftover temp files. It
// keeps the default grace period (media.DefaultSweepMinAge), so a file an
// upload just wrote but has not committed yet is never removed. --dry-run
// reports what would go without touching anything.
func runMedia(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return usagef("media needs a subcommand: gc")
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "gc":
		return runMediaGC(ctx, rest)
	default:
		return usagef("unknown media command %q", sub)
	}
}

// runMediaGC implements `media gc [--dry-run]`.
func runMediaGC(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("media gc", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	dryRun := flags.Bool("dry-run", false, "report what would be removed without removing it")
	if err := flags.Parse(args); err != nil {
		return usagef("media gc: %v", err)
	}
	if flags.NArg() != 0 {
		return usagef("media gc: unexpected arguments %q", flags.Args())
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	logger := newLogger(os.Stderr, cfg.LogLevel)

	db, err := openDB(ctx, cfg)
	if err != nil {
		return err
	}
	defer db.Close()

	files, err := media.NewStore(cfg.MediaDir)
	if err != nil {
		return err
	}

	refs := media.NewRefSet()
	if err := store.NewExercises(db).ImageRefs(ctx, func(id uuid.UUID, img domain.ExerciseImage) error {
		refs[media.Ref{ID: id, Hash: img.Hash, Ext: img.Ext}] = struct{}{}
		return nil
	}); err != nil {
		return err
	}

	result, err := media.Sweep(files.Root(), refs.Has, media.SweepOptions{DryRun: *dryRun})
	if err != nil {
		return err
	}
	logger.InfoContext(ctx, "media gc finished",
		"dry_run", result.DryRun,
		"scanned", result.Scanned,
		"removed", result.Removed,
		"orphans", len(result.Paths),
		"bytes", result.Bytes,
		"removed_dirs", len(result.RemovedDirs),
		"skipped", len(result.Skipped),
	)
	return nil
}
