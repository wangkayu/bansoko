package migrations

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// constraintProbe runs statements against the installed baseline and asserts
// whether the schema accepts or rejects them.
type constraintProbe struct {
	t   *testing.T
	db  *sql.DB
	ctx context.Context
}

func newConstraintProbe(t *testing.T) constraintProbe {
	t.Helper()
	return constraintProbe{t: t, db: installedDB(t), ctx: context.Background()}
}

// accept fails the test if the statement is rejected.
func (p constraintProbe) accept(name, statement string, args ...any) {
	p.t.Helper()
	if _, err := p.db.ExecContext(p.ctx, statement, args...); err != nil {
		p.t.Fatalf("%s: expected the statement to persist: %v", name, err)
	}
}

// reject fails the test if the statement succeeds.
func (p constraintProbe) reject(name, statement string, args ...any) {
	p.t.Helper()
	if _, err := p.db.ExecContext(p.ctx, statement, args...); err == nil {
		p.t.Fatalf("%s: expected a constraint violation, got success", name)
	}
}

// baselineText returns the shipped baseline migration as text, for static
// assertions on contract fragments that have no cheaper behavioural probe.
func baselineText(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(migrationsDir(t), baselineMigrationFile))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// requireFragments fails the test for every fragment missing from text.
func requireFragments(t *testing.T, text, what string, fragments ...string) {
	t.Helper()
	for _, fragment := range fragments {
		if !strings.Contains(text, fragment) {
			t.Errorf("%s is missing %q", what, fragment)
		}
	}
}
