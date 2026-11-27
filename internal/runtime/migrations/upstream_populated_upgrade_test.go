package migrations

import (
	"database/sql"
	"testing"
	"time"

	"bansoko/internal/runtime/database"
)

func TestUpgradePopulatedUpstreamHealthFrom163(t *testing.T) {
	ctx := t.Context()
	db := openDisposableDB(t)
	installMigrationsThrough(t, db, "163_retire_naming_vars.sql")
	const attempted = "2026-01-02T00:00:00Z"
	const success = "2026-01-01T00:00:00Z"
	if _, err := db.ExecContext(ctx, `INSERT INTO provider_group_refresh_health(provider,catalog_entry_ref,state,last_attempt_at,last_success_at,error_code,error_message,version) VALUES
   ('anibt','101','fresh',$1,$2,NULL,NULL,3),
   ('anibt','102','empty',$1,$2,NULL,NULL,4),
   ('mikan','103','error',$1,$2,'UPSTREAM_FAILED','original failure',5)`, attempted, success); err != nil {
		t.Fatal(err)
	}
	runner := Runner{DB: db, Dir: migrationsDir(t)}
	for range 2 {
		if err := runner.Apply(ctx); err != nil {
			t.Fatal(err)
		}
	}
	password := "upstream-upgrade-test-role"
	if err := database.ProvisionRuntimeRole(ctx, db, database.RuntimeRoleConfig{Role: runtimeRoleTestName, Password: password}); err != nil {
		t.Fatal(err)
	}
	runtime := openRuntimeConnection(t, password)
	defer runtime.Close()
	for _, tc := range []struct {
		entry, state, code, message string
		version                     int
	}{{"101", "available", "", "", 3}, {"102", "empty", "", "", 4}, {"103", "error", "UPSTREAM_FAILED", "original failure", 5}} {
		var state, code, message string
		var lastAttempt, lastSuccess, checked time.Time
		var cooldown sql.NullTime
		var version int
		err := runtime.QueryRowContext(ctx, `SELECT state,coalesce(error_code,''),coalesce(error_message,''),last_attempt_at,last_success_at,checked_at,cooldown_until,version FROM provider_group_refresh_health WHERE catalog_entry_ref=$1`, tc.entry).Scan(&state, &code, &message, &lastAttempt, &lastSuccess, &checked, &cooldown, &version)
		if err != nil || state != tc.state || code != tc.code || message != tc.message || version != tc.version || cooldown.Valid || !checked.Equal(lastAttempt) || checked.UTC().Format(time.RFC3339) != attempted || lastSuccess.UTC().Format(time.RFC3339) != success {
			t.Fatalf("upgraded row %s: state=%s code=%s message=%s version=%d attempted=%v success=%v checked=%v cooldown=%v err=%v", tc.entry, state, code, message, version, lastAttempt, lastSuccess, checked, cooldown, err)
		}
	}
	if _, err := runtime.ExecContext(ctx, `UPDATE provider_group_refresh_health SET state='rate_limited',error_code='PROVIDER_RATE_LIMITED',cooldown_until=checked_at+interval '1 minute' WHERE provider='anibt' AND catalog_entry_ref='101'`); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := runtime.QueryRowContext(ctx, `SELECT state FROM provider_group_refresh_health WHERE provider='anibt' AND catalog_entry_ref='101' AND cooldown_until>checked_at`).Scan(&state); err != nil || state != "rate_limited" {
		t.Fatalf("runtime update state=%s err=%v", state, err)
	}
}
