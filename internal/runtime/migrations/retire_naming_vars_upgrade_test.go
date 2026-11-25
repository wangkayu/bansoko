package migrations

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"bansoko/internal/runtime/migrations/varspreflight"
)

func TestRetireNamingVarsBlocksBeforeChanging162(t *testing.T) {
	ctx := context.Background()
	db := openDisposableDB(t)
	defer db.Close()
	installMigrationsThrough(t, db, "162_notification_episode_span_facts.sql")

	// Bracket access is intentionally covered by the AST, not the SQL backstop.
	const rules = `{"tv":{"directory_template":"${context[\"vars\"].anime.dir}"}}`
	if _, err := db.ExecContext(ctx, `INSERT INTO global_config_sections(section_name, schema_version, value, version)
VALUES ('naming_rules', 'naming-rules-v1', $1::jsonb, 1)
ON CONFLICT (section_name) DO UPDATE SET value=EXCLUDED.value`, rules); err != nil {
		t.Fatalf("seed 162 naming rules: %v", err)
	}
	runner := Runner{DB: db, Dir: migrationsDir(t)}
	for _, check := range []struct {
		name string
		err  error
	}{
		{"read-only pending check", varspreflight.CheckPending(ctx, db)},
		{"transactional migration check", runner.Apply(ctx)},
	} {
		var blocked *varspreflight.Blocked
		if !errors.As(check.err, &blocked) {
			t.Fatalf("%s: got %v, want a Vars preflight block", check.name, check.err)
		}
		want := varspreflight.Hit{Location: "global_config_sections:naming_rules", Field: "tv.directory_template"}
		if len(blocked.Hits) != 1 || blocked.Hits[0] != want {
			t.Fatalf("%s: hits=%+v, want %+v", check.name, blocked.Hits, want)
		}
	}
	var recorded, oldTable, unchanged bool
	if err := db.QueryRowContext(ctx, `SELECT
EXISTS (SELECT 1 FROM schema_migrations WHERE version=$1),
to_regclass('public.naming_vars_versions') IS NOT NULL,
(SELECT value=$2::jsonb FROM global_config_sections WHERE section_name='naming_rules')`, varspreflight.Migration, rules).Scan(&recorded, &oldTable, &unchanged); err != nil {
		t.Fatal(err)
	}
	if recorded || !oldTable || !unchanged {
		t.Fatalf("blocked migration changed 162: recorded=%v old table=%v original rules=%v", recorded, oldTable, unchanged)
	}

	if _, err := db.ExecContext(ctx, `UPDATE global_config_sections SET value='{"tv":{"directory_template":"${anime.title}"}}' WHERE section_name='naming_rules'`); err != nil {
		t.Fatal(err)
	}
	if err := runner.Apply(ctx); err != nil {
		t.Fatalf("apply 163 after correcting the template: %v", err)
	}
	if err := varspreflight.CheckPending(ctx, db); err != nil {
		t.Fatalf("replay after applying 163: %v", err)
	}
}

func TestRetireNamingVarsChecksOnlyCustomNotificationTemplates(t *testing.T) {
	ctx := context.Background()
	db := openDisposableDB(t)
	defer db.Close()
	installMigrationsThrough(t, db, "162_notification_episode_span_facts.sql")

	const custom = `{"events":{"download_submitted":{"mode":"custom","title_template":"{{vars.fixture.title}}","body_template":"Fixed body"}}}`
	const system = `{"events":{"download_submitted":{"mode":"system_default","title_template":"{{vars.fixture.title}}","body_template":"Fixed body"}}}`
	if _, err := db.ExecContext(ctx, `
INSERT INTO plugin_target_configs(id,name,plugin_kind,adapter_kind,trigger_kinds,config_schema_version,config,template_set)
VALUES ('vars-notify','Fixture','notification','webhook','["download_submitted"]',1,
        '{"adapter_kind":"webhook","auth_kind":"none"}',$1::jsonb)`, custom); err != nil {
		t.Fatal(err)
	}
	var blocked *varspreflight.Blocked
	err := varspreflight.CheckPending(ctx, db)
	want := varspreflight.Hit{Location: "plugin_target_configs:vars-notify", Field: "events.download_submitted.title_template"}
	if !errors.As(err, &blocked) || len(blocked.Hits) != 1 || blocked.Hits[0] != want {
		t.Fatalf("custom template must block the Go preflight at %+v: %v", want, err)
	}

	// Direct SQL installations use the lexical backstop instead of the Go AST.
	dir := migrationsDir(t)
	raw, err := os.ReadFile(filepath.Join(dir, varspreflight.Migration))
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, string(raw))
	if adoptAcceptPGMessage(err) != "NAMING_VARS_TEMPLATES_PRESENT" {
		t.Fatalf("custom template must block the SQL backstop: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}

	if _, err := db.ExecContext(ctx, `UPDATE plugin_target_configs SET template_set=$1::jsonb WHERE id='vars-notify'`, system); err != nil {
		t.Fatal(err)
	}
	if err := varspreflight.CheckPending(ctx, db); err != nil {
		t.Fatalf("system-default template must not block the Go preflight: %v", err)
	}
	if err := (Runner{DB: db, Dir: dir}).Apply(ctx); err != nil {
		t.Fatalf("system-default template must not block migration 163: %v", err)
	}
	var unchanged bool
	if err := db.QueryRowContext(ctx, `SELECT template_set=$1::jsonb FROM plugin_target_configs WHERE id='vars-notify'`, system).Scan(&unchanged); err != nil || !unchanged {
		t.Fatalf("upgrade rewrote inactive notification templates: unchanged=%v err=%v", unchanged, err)
	}
}
