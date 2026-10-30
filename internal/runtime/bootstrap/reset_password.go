package bootstrap

import (
	"bufio"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	accessapplication "bansoko/internal/accesscontrol/application"
	accessdomain "bansoko/internal/accesscontrol/domain"
	"bansoko/internal/runtime/database"
)

// reset-password is the out-of-band owner credential reset. It runs inside
// the container (`docker compose exec app /app/bootstrap reset-password`), so
// the authority is physical access to the deployment; no network credential
// gates it. The command applies the selected tree's schema first (the same
// owner-DSN migrate path a server launch takes), then replaces the owner
// password — optionally renaming the account — and revokes every web session.
// Scoped API tokens survive: they are machine-held and stay bound to the
// owner account id.
//
// Session authentication is a per-request database lookup, so the revocation
// is visible to a running server immediately; no restart or signal is needed.

type resetPasswordOptions struct {
	generate bool
	username string
}

func parseResetPasswordFlags(args []string) (resetPasswordOptions, error) {
	var opts resetPasswordOptions
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--generate":
			opts.generate = true
		case "--username":
			if i+1 >= len(args) {
				return resetPasswordOptions{}, errors.New("--username requires a value")
			}
			i++
			opts.username = args[i]
		default:
			return resetPasswordOptions{}, fmt.Errorf("unknown argument %q", args[i])
		}
	}
	return opts, nil
}

// generateResetPassword returns a fresh 24-character password. It is shown
// once on stdout; only its bcrypt hash is stored.
func generateResetPassword() (string, error) {
	raw := make([]byte, 18)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate password: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func promptResetLine(reader *bufio.Reader, out io.Writer, label string) (string, error) {
	fmt.Fprint(out, label)
	line, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("read input: %w", err)
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// collectResetCredentials resolves the username and password from the flags
// and, without --generate, the interactive prompts. The typed password is
// echoed by the terminal; use --generate to avoid typing one.
func collectResetCredentials(opts resetPasswordOptions, stdin io.Reader, stderr io.Writer) (username, password string, err error) {
	username = strings.TrimSpace(opts.username)
	if opts.generate {
		password, err = generateResetPassword()
		return username, password, err
	}
	reader := bufio.NewReader(stdin)
	if opts.username == "" {
		username, err = promptResetLine(reader, stderr, "New owner username (empty keeps the current one): ")
		if err != nil {
			return "", "", err
		}
		username = strings.TrimSpace(username)
	}
	password, err = promptResetLine(reader, stderr, "New owner password (12-1024 characters): ")
	if err != nil {
		return "", "", err
	}
	confirm, err := promptResetLine(reader, stderr, "Repeat the new password: ")
	if err != nil {
		return "", "", err
	}
	if password != confirm {
		return "", "", errors.New("the two passwords do not match")
	}
	return username, password, nil
}

// Process seam: tests replace it to observe the reset without a database.
var resetDatabaseOpen = func(ctx context.Context) (*sql.DB, error) {
	return database.Open(ctx, database.Config{DSN: maintenanceDatabaseURL()})
}

func runResetPassword(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	opts, err := parseResetPasswordFlags(args)
	if err != nil {
		fmt.Fprintf(stderr, "[bootstrap] reset-password: %v; usage: reset-password [--generate] [--username NAME]\n", err)
		return 2
	}
	if err := prepareEnvironment(); err != nil {
		fmt.Fprintf(stderr, "[bootstrap] reset-password: deployment environment unusable: %v\n", err)
		return 1
	}
	username, password, err := collectResetCredentials(opts, stdin, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "[bootstrap] reset-password: %v\n", err)
		return 2
	}
	// Reset against the tree the server would serve. An in-flight update or a
	// parked deployment keeps its evidence untouched: refuse instead of
	// migrating or editing credentials mid-flight.
	chosen, _ := selectBootstrapPlan("migrate", os.Getenv)
	if chosen.mode != modeBase && chosen.mode != modeOverlay {
		fmt.Fprintf(stderr, "[bootstrap] reset-password refused: %s\n", chosen.reason)
		return 1
	}
	logf("applying the %s schema before the owner reset", chosen.mode)
	if err := runStartupMigration(chosen.binaryPath, envWithOverrides(map[string]string{envSchemaDir: chosen.schemaDir})); err != nil {
		fmt.Fprintf(stderr, "[bootstrap] reset-password: migration failed, credentials unchanged: %v\n", err)
		return 1
	}
	db, err := resetDatabaseOpen(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "[bootstrap] reset-password: database open failed, credentials unchanged: %v\n", err)
		return 1
	}
	defer db.Close()
	service := accessapplication.NewService(db)
	account, revoked, err := service.ResetOwnerCredentials(ctx, username, password)
	if err != nil {
		if errors.Is(err, accessdomain.ErrSetupNotFinalized) {
			fmt.Fprintln(stderr, "[bootstrap] reset-password: the instance has no owner yet; create it on the first web visit instead")
			return 1
		}
		if errors.Is(err, accessdomain.ErrOwnerInputInvalid) {
			fmt.Fprintf(stderr, "[bootstrap] reset-password: %v\n", err)
			return 2
		}
		fmt.Fprintf(stderr, "[bootstrap] reset-password failed, credentials unchanged: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Owner credentials reset for %q; %d session(s) revoked; API tokens unchanged.\n", account.Username, revoked)
	if opts.generate {
		fmt.Fprintf(stdout, "Generated password (shown once): %s\n", password)
	}
	return 0
}
