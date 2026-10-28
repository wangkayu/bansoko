package migrations

import (
	"context"
	"strings"
	"testing"
)

// 092 restarts the business journal on message codes: it reaches a database
// whose journal holds text-only entries (and a retired rss_refresh_failed
// entry), deletes them, and leaves message_code and message_params required
// and bounded.
func TestActivityMessageCodesUpgradeAPopulatedJournal(t *testing.T) {
	ctx := context.Background()
	db := openDisposableDB(t)
	installMigrationsThrough(t, db, "091_acquisition_settle_liveness.sql")

	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("seed the journal an installed database holds: %v", err)
		}
	}
	exec(`INSERT INTO managed_anime(id, title) VALUES ('anime-092', 'Journal Anime')`)
	exec(`INSERT INTO subscriptions(id, managed_anime_id, origin, instance_label, state)
		VALUES ('sub-092', 'anime-092', 'custom', 'journal', 'active')`)
	exec(`INSERT INTO subscription_log_entries(id, subscription_id, managed_anime_id, kind, outcome, title, summary, dedupe_key)
		VALUES ('log-created', 'sub-092', 'anime-092', 'subscription_created', 'info', 'Journal Anime', 'Subscription created', 'created'),
		       ('log-refresh', 'sub-092', 'anime-092', 'rss_refresh_failed', 'failure', 'Journal Anime', 'RSS refresh failed', 'refresh'),
		       ('log-replace', 'sub-092', 'anime-092', 'replacement_decided', 'info', 'Journal Anime', 'Will replace the old version', 'replace')`)

	if err := (Runner{DB: db, Dir: migrationsDir(t)}).Apply(ctx); err != nil {
		t.Fatalf("upgrade a populated journal: %v", err)
	}

	var rows int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM subscription_log_entries`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("journal rows after 092 = %d, want 0: text-only entries are deleted", rows)
	}

	columns := map[string]string{}
	columnRows, err := db.QueryContext(ctx, `
SELECT column_name, is_nullable || ':' || COALESCE(column_default, '')
FROM information_schema.columns
WHERE table_schema = 'public' AND table_name = 'subscription_log_entries'`)
	if err != nil {
		t.Fatal(err)
	}
	defer columnRows.Close()
	for columnRows.Next() {
		var name, shape string
		if err := columnRows.Scan(&name, &shape); err != nil {
			t.Fatal(err)
		}
		columns[name] = shape
	}
	if err := columnRows.Err(); err != nil {
		t.Fatal(err)
	}
	for _, retired := range []string{"title", "summary"} {
		if _, ok := columns[retired]; ok {
			t.Errorf("column %s survives 092", retired)
		}
	}
	for _, required := range []string{"message_code", "message_params"} {
		if columns[required] != "NO:" {
			t.Errorf("column %s is %q, want NOT NULL without a default", required, columns[required])
		}
	}

	insert := `INSERT INTO subscription_log_entries(id, subscription_id, managed_anime_id, kind, outcome, message_code, message_params, dedupe_key)
		VALUES ($1, 'sub-092', 'anime-092', $2, 'info', $3, $4::jsonb, $1)`
	if _, err := db.ExecContext(ctx, insert, "log-ok", "replacement_decided", "replacement_not_replaced", `{"name":"x","count":2}`); err != nil {
		t.Fatalf("a well-formed entry is rejected: %v", err)
	}
	rejected := []struct {
		name, id, kind, code, params, constraint string
	}{
		{"code shape", "log-bad-code", "subscription_created", "Subscription-Created", `{}`, "subscription_log_entries_message_code_check"},
		{"empty code", "log-empty-code", "subscription_created", "", `{}`, "subscription_log_entries_message_code_check"},
		{"nested object param", "log-nested", "subscription_created", "subscription_created", `{"name":{"x":1}}`, "subscription_log_entries_message_params_check"},
		{"array param", "log-array", "subscription_created", "subscription_created", `{"name":["x"]}`, "subscription_log_entries_message_params_check"},
		{"params not an object", "log-not-object", "subscription_created", "subscription_created", `["x"]`, "subscription_log_entries_message_params_check"},
		{"oversized params", "log-oversized", "subscription_created", "subscription_created", `{"name":"` + strings.Repeat("x", 5000) + `"}`, "subscription_log_entries_message_params_check"},
		{"retired kind", "log-retired", "rss_refresh_failed", "subscription_created", `{}`, "subscription_log_entries_kind_check"},
	}
	for _, tc := range rejected {
		_, err := db.ExecContext(ctx, insert, tc.id, tc.kind, tc.code, tc.params)
		if err == nil || !strings.Contains(err.Error(), tc.constraint) {
			t.Errorf("%s: err = %v, want a %s violation", tc.name, err, tc.constraint)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO subscription_log_entries(id, subscription_id, managed_anime_id, kind, outcome, message_code, dedupe_key)
		VALUES ('log-no-params', 'sub-092', 'anime-092', 'subscription_created', 'info', 'subscription_created', 'log-no-params')`); err == nil {
		t.Error("an entry without message_params persists; the column has no default")
	}
}
