package persistence

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"bansoko/internal/namingvars"
	"bansoko/internal/testutil/disposabledb"
)

// versionRows returns every stored version as (fingerprint, active) so a test can
// assert what publication did to the version history, not only what Current reads.
func versionRows(t *testing.T, repository *Repository) map[string]bool {
	t.Helper()
	rows, err := repository.db.QueryContext(context.Background(), `SELECT fingerprint, active FROM naming_vars_versions`)
	if err != nil {
		t.Fatalf("read naming vars versions: %v", err)
	}
	defer rows.Close()
	versions := map[string]bool{}
	for rows.Next() {
		var fingerprint string
		var active bool
		if err := rows.Scan(&fingerprint, &active); err != nil {
			t.Fatalf("scan naming vars version: %v", err)
		}
		versions[fingerprint] = active
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate naming vars versions: %v", err)
	}
	return versions
}

func TestPublishActivatesExactlyOneVersionAndIsIdempotent(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	ctx := context.Background()
	repository := NewRepository(db)

	first := "vars.anime = {ep: '01'};"
	firstFP, err := namingvars.SourceFingerprint(first)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.Publish(ctx, first, firstFP); err != nil {
		t.Fatalf("publish first version: %v", err)
	}
	source, fingerprint, err := repository.Current(ctx)
	if err != nil {
		t.Fatalf("read current version: %v", err)
	}
	if source != first || fingerprint != firstFP {
		t.Fatalf("current = %q / %q, want the published version", source, fingerprint)
	}

	// A restart republishes the same source. Publication is a no-op then: no new
	// version and no rewrite of the active one, so the version history stays a
	// record of what actually changed.
	if err := repository.Publish(ctx, first, firstFP); err != nil {
		t.Fatalf("republish identical version: %v", err)
	}
	if versions := versionRows(t, repository); len(versions) != 1 || !versions[firstFP] {
		t.Fatalf("versions after identical republish = %v, want the single active version", versions)
	}
	if active := activeRowCount(t, repository); active != 1 {
		t.Fatalf("active versions after identical republish = %d, want 1", active)
	}

	second := "vars.anime = {ep: '02'};"
	secondFP, err := namingvars.SourceFingerprint(second)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.Publish(ctx, second, secondFP); err != nil {
		t.Fatalf("publish changed version: %v", err)
	}
	versions := versionRows(t, repository)
	if len(versions) != 2 {
		t.Fatalf("versions after change = %v, want the previous version retained", versions)
	}
	if versions[firstFP] || !versions[secondFP] {
		t.Fatalf("versions after change = %v, want exactly the new version active", versions)
	}
	if active := activeRowCount(t, repository); active != 1 {
		t.Fatalf("active versions after change = %d, want 1", active)
	}
	source, fingerprint, err = repository.Current(ctx)
	if err != nil {
		t.Fatalf("read current version after change: %v", err)
	}
	if source != second || fingerprint != secondFP {
		t.Fatalf("current after change = %q / %q, want the changed version", source, fingerprint)
	}

	// Rolling back to a previously published source reactivates the version that
	// already exists: the history must not grow a duplicate row, and the old row
	// must be the active one again.
	if err := repository.Publish(ctx, first, firstFP); err != nil {
		t.Fatalf("republish historical version: %v", err)
	}
	versions = versionRows(t, repository)
	if len(versions) != 2 || !versions[firstFP] || versions[secondFP] {
		t.Fatalf("versions after rollback = %v, want the historical version reactivated", versions)
	}
	if source, fingerprint, err = repository.Current(ctx); err != nil {
		t.Fatalf("read current version after rollback: %v", err)
	}
	if source != first || fingerprint != firstFP {
		t.Fatalf("current after rollback = %q / %q, want the historical version", source, fingerprint)
	}
}

// activeRowCount is the invariant the schema's partial unique index enforces:
// publishing must never leave zero or two active versions.
func activeRowCount(t *testing.T, repository *Repository) int {
	t.Helper()
	var count int
	if err := repository.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM naming_vars_versions WHERE active`).Scan(&count); err != nil {
		t.Fatalf("count active naming vars versions: %v", err)
	}
	return count
}

func TestCurrentWithoutPublishedVersionFailsClosed(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()

	if _, _, err := NewRepository(db).Current(context.Background()); err == nil {
		t.Fatal("reading the active version before any publication must fail closed")
	}
}

func TestPublishRejectsEmptyIdentityOrSource(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	ctx := context.Background()
	repository := NewRepository(db)

	for name, tc := range map[string]struct{ source, fingerprint string }{
		"empty source":      {source: "", fingerprint: "sha256:x"},
		"empty fingerprint": {source: "vars.a = {b: 'c'};", fingerprint: ""},
	} {
		t.Run(name, func(t *testing.T) {
			if err := repository.Publish(ctx, tc.source, tc.fingerprint); err == nil {
				t.Fatal("incomplete publication must be rejected")
			}
		})
	}
	if versions := versionRows(t, repository); len(versions) != 0 {
		t.Fatalf("rejected publications must not be stored: %v", versions)
	}
}

func TestPublishVerifiesFingerprintAndNormalizesSource(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	ctx := context.Background()
	repo := NewRepository(db)
	source := "vars.a = {b: 'value'};\r\n"
	fp, err := namingvars.SourceFingerprint(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Publish(ctx, source, "sha256:not-the-content"); err == nil {
		t.Fatal("an arbitrary fingerprint must be rejected")
	}
	if rows := versionRows(t, repo); len(rows) != 0 {
		t.Fatalf("invalid fingerprint stored a version: %v", rows)
	}
	if err := repo.Publish(ctx, source, fp); err != nil {
		t.Fatal(err)
	}
	if err := repo.Publish(ctx, "vars.a = {b: 'value'};\n", fp); err != nil {
		t.Fatalf("normalized equivalent source must republish: %v", err)
	}
	got, actual, err := repo.Current(ctx)
	if err != nil || actual != fp || got != "vars.a = {b: 'value'};\n" {
		t.Fatalf("canonical source: %q %q %v", got, actual, err)
	}
}

func TestVersionedSaveNumbersRevisionsAndUsesCompareAndSwap(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	ctx := context.Background()
	repository := NewRepository(db)

	first, second := "vars.a = {b: '1'};\n", "vars.a = {b: '2'};\n"
	firstFP, _ := namingvars.SourceFingerprint(first)
	secondFP, _ := namingvars.SourceFingerprint(second)
	if _, err := repository.ActiveVersion(ctx); !errors.Is(err, namingvars.ErrVersionNotFound) {
		t.Fatalf("empty store active = %v", err)
	}
	seeded, err := repository.Seed(ctx, namingvars.SaveParams{Source: first, Fingerprint: firstFP, Origin: namingvars.OriginFile})
	if err != nil || !seeded {
		t.Fatalf("seed = %v / %v", seeded, err)
	}
	if again, err := repository.Seed(ctx, namingvars.SaveParams{Source: second, Fingerprint: secondFP, Origin: namingvars.OriginFile}); err != nil || again {
		t.Fatalf("a second seed must change nothing: %v / %v", again, err)
	}
	active, err := repository.ActiveVersion(ctx)
	if err != nil || active.Revision != 1 || active.Digest != firstFP || active.Source != first || active.ActivatedAt == nil {
		t.Fatalf("seeded active = %+v / %v", active, err)
	}

	// A save from a stale revision conflicts and writes nothing.
	_, err = repository.SaveVersion(ctx, namingvars.SaveParams{Source: second, Fingerprint: secondFP, Origin: namingvars.OriginEditor, ExpectedRevision: 5})
	var conflict *namingvars.ConflictError
	if !errors.As(err, &conflict) || conflict.Current.Revision != 1 {
		t.Fatalf("stale save = %v", err)
	}
	if rows := versionRows(t, repository); len(rows) != 1 {
		t.Fatalf("a conflicting save wrote %d versions", len(rows))
	}

	saved, err := repository.SaveVersion(ctx, namingvars.SaveParams{Source: second, Fingerprint: secondFP, Origin: namingvars.OriginEditor, Note: "two", ExpectedRevision: 1})
	if err != nil || saved.Revision != 2 || !saved.Active || saved.Note != "two" || saved.Origin != namingvars.OriginEditor {
		t.Fatalf("save = %+v / %v", saved, err)
	}
	if active := activeRowCount(t, repository); active != 1 {
		t.Fatalf("active rows = %d", active)
	}

	// Rolling back is an activation of revision 1: no new row, digests untouched.
	rolled, err := repository.ActivateRevision(ctx, 2, 1)
	if err != nil || rolled.Revision != 1 || rolled.Digest != firstFP || !rolled.Active {
		t.Fatalf("rollback = %+v / %v", rolled, err)
	}
	if _, err := repository.ActivateRevision(ctx, 2, 2); err == nil {
		t.Fatal("activation from a stale revision must conflict")
	}
	if _, err := repository.ActivateRevision(ctx, 1, 42); !errors.Is(err, namingvars.ErrVersionNotFound) {
		t.Fatalf("unknown revision = %v", err)
	}
	// Saving a source that is already a version re-activates it: no duplicate.
	again, err := repository.SaveVersion(ctx, namingvars.SaveParams{Source: second, Fingerprint: secondFP, Origin: namingvars.OriginImport, ExpectedRevision: 1})
	if err != nil || again.Revision != 2 || again.Origin != namingvars.OriginEditor || again.Note != "two" {
		t.Fatalf("re-save of an existing source = %+v / %v, want revision 2 with its original origin and note", again, err)
	}
	list, err := repository.ListVersions(ctx, 10)
	if err != nil || len(list) != 2 || list[0].Revision != 2 || !list[0].Active || list[1].Revision != 1 || list[1].SourceBytes != len(first) {
		t.Fatalf("list = %+v / %v", list, err)
	}
	// A caller that supplies a different fingerprint cannot publish.
	if _, err := repository.SaveVersion(ctx, namingvars.SaveParams{Source: second, Fingerprint: firstFP, Origin: namingvars.OriginEditor, ExpectedRevision: 2}); err == nil {
		t.Fatal("a fingerprint that does not match the source must be refused")
	}
}

func TestVersionedSaveSerializesConcurrentEditors(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	ctx := context.Background()
	repository := NewRepository(db)
	base := "vars.a = {b: '0'};\n"
	baseFP, _ := namingvars.SourceFingerprint(base)
	if _, err := repository.Seed(ctx, namingvars.SaveParams{Source: base, Fingerprint: baseFP, Origin: namingvars.OriginFile}); err != nil {
		t.Fatal(err)
	}
	const editors = 8
	results := make(chan error, editors)
	for i := 0; i < editors; i++ {
		source := fmt.Sprintf("vars.a = {b: 'editor-%d'};\n", i)
		fp, _ := namingvars.SourceFingerprint(source)
		go func() {
			_, err := repository.SaveVersion(ctx, namingvars.SaveParams{Source: source, Fingerprint: fp, Origin: namingvars.OriginEditor, ExpectedRevision: 1})
			results <- err
		}()
	}
	won, conflicts := 0, 0
	for i := 0; i < editors; i++ {
		err := <-results
		var conflict *namingvars.ConflictError
		switch {
		case err == nil:
			won++
		case errors.As(err, &conflict):
			conflicts++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if won != 1 || conflicts != editors-1 {
		t.Fatalf("%d editors won and %d conflicted, want exactly one winner", won, conflicts)
	}
	if rows := versionRows(t, repository); len(rows) != 2 {
		t.Fatalf("%d versions stored, want the seed and the single winner", len(rows))
	}
	if active := activeRowCount(t, repository); active != 1 {
		t.Fatalf("active rows = %d", active)
	}
}
