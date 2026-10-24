package migrations

import (
	"context"
	"testing"

	"bansoko/internal/namingvars"
	varspersistence "bansoko/internal/namingvars/adapter/persistence"
	"bansoko/internal/runtime/database"
)

// Vars versions are numbered gap-free in publication order by the insert
// trigger, one row is active, and program content and numbering are immutable.
func TestNamingVarsRevisions(t *testing.T) {
	db := installedDB(t)
	ctx := context.Background()

	for i, row := range []struct {
		fingerprint string
		active      bool
	}{{"sha256:first", false}, {"sha256:second", false}, {"sha256:third", true}} {
		if _, err := db.ExecContext(ctx, `INSERT INTO naming_vars_versions(fingerprint, content, active) VALUES ($1, to_jsonb($2::text), $3)`, row.fingerprint, "vars.a = {b: '"+string(rune('1'+i))+"'};", row.active); err != nil {
			t.Fatal(err)
		}
	}

	rows, err := db.QueryContext(ctx, `SELECT fingerprint, revision, origin, note, active, activated_at IS NOT NULL FROM naming_vars_versions ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	want := []struct {
		fingerprint string
		revision    int64
		active      bool
	}{{"sha256:first", 1, false}, {"sha256:second", 2, false}, {"sha256:third", 3, true}}
	for i := 0; rows.Next(); i++ {
		var fingerprint, origin, note string
		var revision int64
		var active, activated bool
		if err := rows.Scan(&fingerprint, &revision, &origin, &note, &active, &activated); err != nil {
			t.Fatal(err)
		}
		if fingerprint != want[i].fingerprint || revision != want[i].revision || active != want[i].active || origin != "file" || note != "" || activated != want[i].active {
			t.Fatalf("row %d = %s rev %d origin %s note %q active %v activated %v", i, fingerprint, revision, origin, note, active, activated)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	// A later insert is numbered by the trigger, gap-free.
	var revision int64
	if err := db.QueryRowContext(ctx, `INSERT INTO naming_vars_versions(fingerprint, content, active, origin, note) VALUES ('sha256:fourth', to_jsonb('vars.a = {b: ''4''};'::text), FALSE, 'editor', 'later') RETURNING revision`).Scan(&revision); err != nil || revision != 4 {
		t.Fatalf("new row revision = %d / %v, want 4", revision, err)
	}
	for _, statement := range []string{
		`UPDATE naming_vars_versions SET revision = 99 WHERE revision = 4`,
		`UPDATE naming_vars_versions SET origin = 'import' WHERE revision = 4`,
		`UPDATE naming_vars_versions SET note = 'edited' WHERE revision = 4`,
		`UPDATE naming_vars_versions SET content = to_jsonb('x'::text) WHERE revision = 4`,
		`DELETE FROM naming_vars_versions WHERE revision = 4`,
		`INSERT INTO naming_vars_versions(fingerprint, content, active, origin) VALUES ('sha256:bad', to_jsonb('x'::text), FALSE, 'unknown')`,
	} {
		if _, err := db.ExecContext(ctx, statement); err == nil {
			t.Fatalf("%q must be refused", statement)
		}
	}
	// The pointer itself still moves.
	if _, err := db.ExecContext(ctx, `UPDATE naming_vars_versions SET active = FALSE WHERE active`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE naming_vars_versions SET active = TRUE, activated_at = CURRENT_TIMESTAMP WHERE revision = 2`); err != nil {
		t.Fatalf("moving the active pointer: %v", err)
	}
}

// The segregated runtime role is the one that publishes in production: it must
// be able to seed, save, roll back and list versions with its column grants, and still not touch content, numbering or deletion.
func TestNamingVarsRuntimeRoleVersionsPrograms(t *testing.T) {
	owner := installedDB(t)
	ctx := context.Background()
	password := randomTestPassword(t)
	if err := database.ProvisionRuntimeRole(ctx, owner, database.RuntimeRoleConfig{Role: runtimeRoleTestName, Password: password}); err != nil {
		t.Fatal(err)
	}
	runtime := openRuntimeConnection(t, password)
	defer runtime.Close()
	repo := varspersistence.NewRepository(runtime)

	save := func(source string) namingvars.SaveParams {
		fp, err := namingvars.SourceFingerprint(source)
		if err != nil {
			t.Fatal(err)
		}
		return namingvars.SaveParams{Source: source, Fingerprint: fp, Origin: namingvars.OriginEditor}
	}
	first, second := save("vars.a = {b: '1'};\n"), save("vars.a = {b: '2'};\n")
	first.Origin = namingvars.OriginFile
	if seeded, err := repo.Seed(ctx, first); err != nil || !seeded {
		t.Fatalf("runtime seed = %v / %v", seeded, err)
	}
	second.ExpectedRevision = 1
	saved, err := repo.SaveVersion(ctx, second)
	if err != nil || saved.Revision != 2 || !saved.Active {
		t.Fatalf("runtime save = %+v / %v", saved, err)
	}
	rolled, err := repo.ActivateRevision(ctx, 2, 1)
	if err != nil || rolled.Revision != 1 || rolled.ActivatedAt == nil {
		t.Fatalf("runtime rollback = %+v / %v", rolled, err)
	}
	if list, err := repo.ListVersions(ctx, 10); err != nil || len(list) != 2 {
		t.Fatalf("runtime list = %+v / %v", list, err)
	}
	for _, statement := range []string{
		`UPDATE naming_vars_versions SET note = 'x' WHERE revision = 1`,
		`UPDATE naming_vars_versions SET revision = 9 WHERE revision = 1`,
		`DELETE FROM naming_vars_versions WHERE revision = 1`,
	} {
		if _, err := runtime.ExecContext(ctx, statement); err == nil {
			t.Fatalf("runtime role ran %q", statement)
		}
	}
}
