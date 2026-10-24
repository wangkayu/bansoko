// Package pgexec implements the embedded-update BackupExecutor port with the
// container-provided PostgreSQL client tooling. Commands and arguments are a
// fixed whitelist; only absolute, deployment-resolved binary paths and
// environment-carried connection facts vary. stderr from child processes is
// trimmed into error text; credentials never appear because they travel via
// PG* env exclusively.
package pgexec

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"bansoko/internal/update/application"
)

// DefaultCandidatePaths are the whitelisted runtime locations for the
// PostgreSQL 17 client tools inside the runtime image (postgresql17-client) and
// on native deployments with Homebrew.
var DefaultCandidatePaths = []string{
	"/usr/local/bin/",
	"/usr/bin/",
	"/opt/homebrew/opt/postgresql@17/bin/",
}

// Executor runs the fixed whitelist of backup commands.
type Executor struct {
	dumpBin    string
	restoreBin string
}

// New resolves the first existing pg_dump/pg_restore pair among candidates
// (each entry is a directory prefix). Construction fails closed when neither
// location hosts both binaries: an updater that cannot back up must not be
// wired at all.
func New(candidateDirs []string) (*Executor, error) {
	var e Executor
	for _, dir := range candidateDirs {
		if dump, err := exec.LookPath(dir + "pg_dump"); err == nil && strings.HasPrefix(dump, dir) {
			e.dumpBin = dump
			break
		}
	}
	if e.dumpBin == "" {
		return nil, fmt.Errorf("no whitelisted pg_dump found in %v", candidateDirs)
	}
	found := false
	for _, dir := range candidateDirs {
		if restore, err := exec.LookPath(dir + "pg_restore"); err == nil && strings.HasPrefix(restore, dir) {
			e.restoreBin = restore
			found = true
			break
		}
	}
	if !found {
		return nil, errors.New("no whitelisted pg_restore found")
	}
	return &e, nil
}

// Dump writes a custom-format archive of the PG* env database to outPath.
func (e *Executor) Dump(ctx context.Context, env []string, outPath string) error {
	cmd := exec.CommandContext(ctx, e.dumpBin,
		"--format=custom",
		"--file="+outPath,
	)
	cmd.Env = env
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%w: %s", err, trimToolOutput(out))
	}
	return nil
}

// Restore loads dumpPath into targetDatabase through pg_restore --clean so
// the verification observes object-recreation semantics like real promotion.
func (e *Executor) Restore(ctx context.Context, env []string, dumpPath, targetDatabase string) error {
	cmd := exec.CommandContext(ctx, e.restoreBin,
		"--clean",
		"--if-exists",
		"--no-owner",
		"--no-privileges",
		"--dbname="+targetDatabase,
		dumpPath,
	)
	cmd.Env = env
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%w: %s", err, trimToolOutput(out))
	}
	return nil
}

// RestoreFresh loads dumpPath into the empty targetDatabase in one
// transaction and stops at the first error, so a failed load leaves no
// partial objects behind. Ownership follows the connecting role; object
// privileges (the runtime role grants) are restored as dumped.
func (e *Executor) RestoreFresh(ctx context.Context, env []string, dumpPath, targetDatabase string) error {
	cmd := exec.CommandContext(ctx, e.restoreBin,
		"--exit-on-error",
		"--single-transaction",
		"--no-owner",
		"--dbname="+targetDatabase,
		dumpPath,
	)
	cmd.Env = env
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%w: %s", err, trimToolOutput(out))
	}
	return nil
}

// ListContents runs pg_restore --list against dumpPath: parse-without-apply
// proof that the archive is structurally readable before any restore effect.
// Bootstrap backup re-verification uses it; no target database is touched.
func (e *Executor) ListContents(ctx context.Context, env []string, dumpPath string) error {
	cmd := exec.CommandContext(ctx, e.restoreBin, "--list", dumpPath)
	cmd.Env = env
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%w: %s", err, trimToolOutput(out))
	}
	return nil
}

// Compile-time port check.
var _ application.BackupExecutor = (*Executor)(nil)

func trimToolOutput(out []byte) string {
	text := strings.TrimSpace(string(out))
	const cap = 400
	if len(text) > cap {
		text = text[:cap] + "…(trimmed)"
	}
	return text
}
