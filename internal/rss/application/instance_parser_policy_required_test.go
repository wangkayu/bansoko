package application

import (
	"database/sql"
	"errors"
	"testing"

	"bansoko/internal/parserpolicy"
)

// The release/source snapshot queries LEFT JOIN the instance parser policy and
// scan NULL-able columns. A missing row must raise the shared typed integrity
// failure; it must never be projected into a code-default policy. This is the
// database-free pin for that projection.
func TestInstanceParserPolicyFromColumnsRequiresTheRow(t *testing.T) {
	_, err := instanceParserPolicyFromColumns("anime-1", sql.NullString{}, sql.NullInt64{})
	if !errors.Is(err, parserpolicy.ErrInstanceParserPolicyRequired) {
		t.Fatalf("missing row error = %v, want ErrInstanceParserPolicyRequired", err)
	}
	// A partially NULL join row is equally unusable: both columns come from the
	// same owner row.
	_, err = instanceParserPolicyFromColumns("anime-1", sql.NullString{String: "regex", Valid: true}, sql.NullInt64{})
	if !errors.Is(err, parserpolicy.ErrInstanceParserPolicyRequired) {
		t.Fatalf("partial row error = %v, want ErrInstanceParserPolicyRequired", err)
	}
}

func TestInstanceParserPolicyFromColumnsRejectsUnregisteredKind(t *testing.T) {
	_, err := instanceParserPolicyFromColumns("anime-1",
		sql.NullString{String: "not-a-parser", Valid: true},
		sql.NullInt64{Int64: 2, Valid: true})
	if !errors.Is(err, parserpolicy.ErrInstanceParserPolicyRequired) {
		t.Fatalf("unregistered kind error = %v, want ErrInstanceParserPolicyRequired", err)
	}
}

func TestInstanceParserPolicyFromColumnsProjectsAValidRow(t *testing.T) {
	instance, err := instanceParserPolicyFromColumns("anime-1",
		sql.NullString{String: "regex", Valid: true},
		sql.NullInt64{Int64: 7, Valid: true})
	if err != nil {
		t.Fatalf("valid row: %v", err)
	}
	if string(instance.Policy.Kind) != "regex" || instance.Version != 7 {
		t.Fatalf("instance = %+v", instance)
	}
}

// selectedParserKind is now a pure projection: an empty kind stays empty so the
// recognition registry rejects it, instead of the removed regex fallback
// silently choosing the bundled parser.
func TestSelectedParserKindDoesNotFallBackToRegex(t *testing.T) {
	if got := selectedParserKind(releaseSnapshot{}); got != "" {
		t.Fatalf("selectedParserKind(empty) = %q, want empty", got)
	}
	if got := selectedParserKind(releaseSnapshot{parserKind: " regex "}); got != "regex" {
		t.Fatalf("selectedParserKind(trimmed) = %q", got)
	}
}
