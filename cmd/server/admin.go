package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"

	"workout-tracker-be/internal/config"
	"workout-tracker-be/internal/domain"
	"workout-tracker-be/internal/service"
)

// runAdmin implements `server admin create-user|reset-password ...`
// (docs/implementation-plan.md section 7). It is the out-of-band path used to
// bootstrap the first admin and to reset a forgotten password; the routine
// admin work goes through the HTTP endpoints.
//
// create-user may set the admin role and bypasses the reserved-name list.
// reset-password replaces the hash and revokes every token of the user. When
// --password is absent the password is read from the terminal without echo (or
// from stdin when input is piped).
func runAdmin(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return usagef("admin needs a subcommand: create-user or reset-password")
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "create-user":
		return runAdminCreateUser(ctx, rest)
	case "reset-password":
		return runAdminResetPassword(ctx, rest)
	default:
		return usagef("unknown admin command %q", sub)
	}
}

// runAdminCreateUser implements `admin create-user --username U [--password P]
// [--role admin|user]`.
func runAdminCreateUser(ctx context.Context, args []string) error {
	fs := adminFlags("admin create-user")
	username := fs.String("username", "", "username of the new user (required)")
	password := fs.String("password", "", "password; prompted when empty")
	role := fs.String("role", string(domain.RoleUser), "role: user or admin")
	if err := fs.Parse(args); err != nil {
		return usagef("admin create-user: %v", err)
	}
	if fs.NArg() != 0 {
		return usagef("admin create-user: unexpected arguments %q", fs.Args())
	}
	if *username == "" {
		return usagef("admin create-user: --username is required")
	}
	parsedRole := domain.Role(*role)
	if !parsedRole.IsValid() {
		return usagef("admin create-user: --role must be %q or %q, got %q", domain.RoleUser, domain.RoleAdmin, *role)
	}
	pw, err := adminReadPassword(*password)
	if err != nil {
		return err
	}

	deps, closeDB, err := openAdminDeps(ctx)
	if err != nil {
		return err
	}
	defer closeDB()

	user, err := service.NewAdmin(deps).CreateUser(ctx, service.CreateUserRequest{
		Username: *username,
		Password: pw,
		Role:     parsedRole,
	})
	if err != nil {
		return err
	}
	deps.Logger.InfoContext(ctx, "user created",
		"user_id", user.ID.String(), "username", user.Username, "role", user.Role)
	return nil
}

// runAdminResetPassword implements `admin reset-password --username U
// [--password P]`.
func runAdminResetPassword(ctx context.Context, args []string) error {
	fs := adminFlags("admin reset-password")
	username := fs.String("username", "", "username of the user (required)")
	password := fs.String("password", "", "new password; prompted when empty")
	if err := fs.Parse(args); err != nil {
		return usagef("admin reset-password: %v", err)
	}
	if fs.NArg() != 0 {
		return usagef("admin reset-password: unexpected arguments %q", fs.Args())
	}
	if *username == "" {
		return usagef("admin reset-password: --username is required")
	}
	pw, err := adminReadPassword(*password)
	if err != nil {
		return err
	}

	deps, closeDB, err := openAdminDeps(ctx)
	if err != nil {
		return err
	}
	defer closeDB()

	if err := service.NewAdmin(deps).ResetPassword(ctx, *username, pw); err != nil {
		return err
	}
	deps.Logger.InfoContext(ctx, "password reset and tokens revoked",
		"username", domain.NormalizeUsername(*username))
	return nil
}

// adminFlags returns a FlagSet that does not print to stdout or stderr; the
// caller turns a parse error into a usage error.
func adminFlags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

// openAdminDeps loads the configuration, opens the database and builds the
// service dependencies. The returned close function releases the database.
func openAdminDeps(ctx context.Context) (service.Deps, func(), error) {
	cfg, err := config.Load()
	if err != nil {
		return service.Deps{}, func() {}, err
	}
	logger := newLogger(os.Stderr, cfg.LogLevel)
	db, err := openDB(ctx, cfg)
	if err != nil {
		return service.Deps{}, func() {}, err
	}
	deps, err := newServiceDeps(cfg, logger, db)
	if err != nil {
		db.Close()
		return service.Deps{}, func() {}, err
	}
	return deps, db.Close, nil
}

// adminReadPassword returns the --password value when non-empty. Otherwise it
// reads the password from the terminal with echo disabled, or, when input is
// not a terminal (a pipe or a file), reads one line from stdin. The prompt is
// written to stderr so stdout stays clean.
func adminReadPassword(flagValue string) (string, error) {
	if flagValue != "" {
		return flagValue, nil
	}
	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		fmt.Fprint(os.Stderr, "Password: ")
		b, err := term.ReadPassword(fd)
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return "", fmt.Errorf("read password: %w", err)
		}
		return string(b), nil
	}
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("read password: %w", err)
	}
	return strings.TrimRight(line, "\r\n"), nil
}
