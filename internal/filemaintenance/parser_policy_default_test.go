package filemaintenance

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"

	"bansoko/internal/apperrors"
	"bansoko/internal/parser"
	"bansoko/internal/parserpolicy"
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/testutil/disposabledb"
)

// parserPolicyDefaultEnv wires the real parser policy owner behind the
// file-maintenance transport adapter. The adapter itself is built with no
// other owner: the creation-template path must not need one, and in particular
// it must never reach a release-reevaluation scheduler (the owner is built
// without one, so any such call would fail closed).
func parserPolicyDefaultEnv(t *testing.T) (*Service, *parserpolicy.Service, *sql.DB) {
	t.Helper()
	db := disposabledb.ResetDisposableSchema(t)
	t.Cleanup(func() { db.Close() })
	owner := parserpolicy.NewService(db)
	service := NewService(nil, nil, nil, nil, nil)
	service.SetParserPolicy(owner)
	return service, owner, db
}

// seededVersion reads the version the shipped migration chain left on the
// singleton template. The baseline writes 1 and migration 012's parser-kind
// rename bumps it, so a test must not assume 1: read the row instead.
func seededVersion(t *testing.T, service *Service) int64 {
	t.Helper()
	view, err := service.GetDefaultParserPolicy(context.Background())
	if err != nil {
		t.Fatalf("read seeded default: %v", err)
	}
	return view.Version
}

// clearParserPolicyDefault removes the chain-seeded row so the absent-row path
// is reachable. The shipped migration chain seeds it (the baseline writes
// regex at version 1, and migration 012's parser-kind rename bumps the
// version), so the runtime "never saved" state only exists on a database
// without that seed.
func clearParserPolicyDefault(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(),
		`DELETE FROM global_config_sections WHERE section_name = 'parser_policy'`); err != nil {
		t.Fatal(err)
	}
}

func asNotFound(t *testing.T, err error) *apperrors.NotFound {
	t.Helper()
	var notFound *apperrors.NotFound
	if !errors.As(err, &notFound) {
		t.Fatalf("error=%v, want a typed NotFound", err)
	}
	return notFound
}

// An unwired owner is a dependency gap, never an empty 200 or a silent default
// save: both surfaces fail closed with the same typed NotFound the instance
// endpoint answers.
func TestParserPolicyDefaultFailsClosedWhenTheOwnerIsUnwired(t *testing.T) {
	service := NewService(nil, nil, nil, nil, nil)
	ctx := context.Background()

	if _, err := service.GetDefaultParserPolicy(ctx); asNotFound(t, err).Resource != "parser_policy" {
		t.Fatalf("unwired read resource: %+v", err)
	}
	_, err := service.SaveDefaultParserPolicy(ctx, ParserPolicyDefaultInput{ExpectedVersion: 0, ParserKind: "regex"})
	if asNotFound(t, err).ID != "not_wired" {
		t.Fatalf("unwired save id: %+v", err)
	}
}

// The absent row reports the versioned code default with version 0: "never
// saved". The migration chain seeds the row (reltitle since 040), so the absent
// state is reached by removing it — and it must stay a 200-shaped view, never a
// NotFound.
func TestParserPolicyDefaultAbsentRowIsTheCodeDefaultAtVersionZero(t *testing.T) {
	service, _, db := parserPolicyDefaultEnv(t)
	ctx := context.Background()
	clearParserPolicyDefault(t, db)

	view, err := service.GetDefaultParserPolicy(ctx)
	if err != nil {
		t.Fatalf("read absent default: %v", err)
	}
	if view.ParserKind != "reltitle" || view.Version != 0 || view.UpdatedAt != nil {
		t.Fatalf("absent default view=%+v, want reltitle/0/nil", view)
	}

	// The create save turns that marker 0 into a real row at version 1.
	created, err := service.SaveDefaultParserPolicy(ctx, ParserPolicyDefaultInput{ExpectedVersion: 0, ParserKind: "regex"})
	if err != nil {
		t.Fatalf("create default: %v", err)
	}
	if created.ParserKind != "regex" || created.Version != 1 || created.UpdatedAt == nil {
		t.Fatalf("created default=%+v, want regex/version-1/updated_at", created)
	}
}

// The seeded row is the real starting state. Saving with the matching
// expected version advances it by one; replaying the same expected version is
// a stale CAS, never a second write. The shipped chain seeds the row at
// whatever version the forward migrations last wrote — the baseline writes 1
// and migrations 012 (rename) and 040 (reltitle default) bump it — so the expected version
// is read from the row instead of assumed.
func TestParserPolicyDefaultSaveAdvancesTheSeededRowWithCAS(t *testing.T) {
	service, _, _ := parserPolicyDefaultEnv(t)
	ctx := context.Background()

	baseline, err := service.GetDefaultParserPolicy(ctx)
	if err != nil || baseline.ParserKind != "reltitle" {
		t.Fatalf("baseline default=%+v err=%v, want the seeded reltitle row (migration 040)", baseline, err)
	}
	seeded := baseline.Version

	saved, err := service.SaveDefaultParserPolicy(ctx, ParserPolicyDefaultInput{ExpectedVersion: seeded, ParserKind: "regex"})
	if err != nil {
		t.Fatalf("save default: %v", err)
	}
	if saved.Version != seeded+1 || saved.ParserKind != "regex" || saved.UpdatedAt == nil {
		t.Fatalf("saved default=%+v, want regex/version-%d/updated_at", saved, seeded+1)
	}

	view, err := service.GetDefaultParserPolicy(ctx)
	if err != nil || view.Version != seeded+1 {
		t.Fatalf("saved default view=%+v err=%v, want version-%d", view, err, seeded+1)
	}

	_, err = service.SaveDefaultParserPolicy(ctx, ParserPolicyDefaultInput{ExpectedVersion: seeded, ParserKind: "regex"})
	var conflict *apperrors.VersionConflict
	if !errors.As(err, &conflict) || conflict.CurrentVersion != seeded+1 || conflict.Owner != "parser_policy_default" {
		t.Fatalf("replayed save error=%v, want a parser_policy_default conflict at version %d", err, seeded+1)
	}
}

// An unregistered kind is refused before any write: the row keeps its previous
// value and version (fail closed, no partial save).
func TestParserPolicyDefaultSaveRejectsAnUnregisteredKind(t *testing.T) {
	service, _, _ := parserPolicyDefaultEnv(t)
	ctx := context.Background()
	// Advance from the version the shipped chain seeded, so the refused save
	// below has a real expected version to lose against.
	seeded := seededVersion(t, service)
	advanced := seeded + 1
	if _, err := service.SaveDefaultParserPolicy(ctx, ParserPolicyDefaultInput{ExpectedVersion: seeded, ParserKind: "regex"}); err != nil {
		t.Fatalf("advance default: %v", err)
	}

	_, err := service.SaveDefaultParserPolicy(ctx, ParserPolicyDefaultInput{ExpectedVersion: advanced, ParserKind: "not-a-registered-parser"})
	var business *apperrors.Business
	if !errors.As(err, &business) || business.Status != 422 || business.Code != "POLICY_INVALID" {
		t.Fatalf("unregistered kind error=%v, want 422 POLICY_INVALID", err)
	}

	view, err := service.GetDefaultParserPolicy(ctx)
	if err != nil {
		t.Fatalf("read after rejected save: %v", err)
	}
	if view.ParserKind != "regex" || view.Version != advanced {
		t.Fatalf("row after rejected save=%+v, want the unchanged regex/version-%d", view, advanced)
	}
}

// The default is a creation template only. Saving it must leave an existing
// instance byte-identical (its frozen kind, provenance and version), and the
// next created instance copies the new row version. The owner runs without a
// release-reevaluation scheduler here, so a default save that tried to
// schedule one would fail closed instead of passing.
func TestParserPolicyDefaultSaveNeverReinheritsExistingInstances(t *testing.T) {
	service, owner, db := parserPolicyDefaultEnv(t)
	ctx := context.Background()
	for _, animeID := range []string{"anime-existing", "anime-later"} {
		if _, err := db.ExecContext(ctx, `INSERT INTO managed_anime(id, title) VALUES ($1, 'Parser')`, animeID); err != nil {
			t.Fatal(err)
		}
	}

	// The first instance freezes the seeded default at whatever version the
	// shipped chain left it.
	copyDefaultToInstance(t, owner, db, "anime-existing")
	before := readParserInstanceRow(t, db, "anime-existing")

	// Re-saving the default changes the template version (seeded -> seeded+1).
	seeded := seededVersion(t, service)
	saved, err := service.SaveDefaultParserPolicy(ctx, ParserPolicyDefaultInput{ExpectedVersion: seeded, ParserKind: "regex"})
	if err != nil {
		t.Fatalf("re-save default: %v", err)
	}
	if saved.Version != seeded+1 {
		t.Fatalf("saved default version=%d, want %d", saved.Version, seeded+1)
	}

	if after := readParserInstanceRow(t, db, "anime-existing"); after != before {
		t.Fatalf("existing instance changed by a default save:\nbefore=%s\nafter =%s", before, after)
	}

	// A later instance copies the current template row version as provenance.
	copyDefaultToInstance(t, owner, db, "anime-later")
	instance, err := owner.GetInstance(ctx, "anime-later")
	if err != nil || instance == nil {
		t.Fatalf("later instance=%v err=%v, want a frozen row", instance, err)
	}
	if instance.Policy.Kind != parser.ParserRegex || instance.SourceDefaultVersion != seeded+1 || instance.Version != 1 {
		t.Fatalf("later instance=%+v, want regex/source-default-%d/version-1", instance, seeded+1)
	}
}

// copyDefaultToInstance mirrors the Subscribe creation port: the copy runs in
// the caller's transaction and is committed by the caller.
func copyDefaultToInstance(t *testing.T, owner *parserpolicy.Service, db *sql.DB, animeID string) {
	t.Helper()
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.CopyDefaultToInstanceTx(ctx, txscope.Wrap(tx), animeID); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// readParserInstanceRow renders the frozen business row by value so a default
// save cannot be hidden behind a formatting-insensitive comparison.
func readParserInstanceRow(t *testing.T, db *sql.DB, animeID string) string {
	t.Helper()
	var (
		kind          sql.NullString
		sourceDefault sql.NullInt64
		version       sql.NullInt64
		updatedAt     sql.NullString
	)
	err := db.QueryRowContext(context.Background(), `
SELECT parser_kind, parser_source_default_version, parser_version, updated_at::text
FROM anime_business_configs WHERE managed_anime_id = $1`, animeID).Scan(&kind, &sourceDefault, &version, &updatedAt)
	if err != nil {
		t.Fatalf("read instance row %s: %v", animeID, err)
	}
	return fmt.Sprintf("%s|%d|%d|%s", kind.String, sourceDefault.Int64, version.Int64, updatedAt.String)
}

// The stored global default accepts every catalog kind and the database
// refuses any other value, so a bad write cannot bypass the Go validation.
func TestParserPolicyDefaultStoredKindIsCatalogChecked(t *testing.T) {
	_, _, db := parserPolicyDefaultEnv(t)
	ctx := context.Background()
	for _, kind := range parser.Kinds() {
		if _, err := db.ExecContext(ctx, `UPDATE global_config_sections SET value = jsonb_build_object('parser_kind', $1::text) WHERE section_name = 'parser_policy'`, string(kind)); err != nil {
			t.Errorf("catalog kind %q refused by the database: %v", kind, err)
		}
	}
	for _, kind := range []string{"", "traditional", "Regex", "not-a-parser"} {
		if _, err := db.ExecContext(ctx, `UPDATE global_config_sections SET value = jsonb_build_object('parser_kind', $1::text) WHERE section_name = 'parser_policy'`, kind); err == nil {
			t.Errorf("kind %q must be refused by the database", kind)
		}
	}
}
