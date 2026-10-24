package migrations

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"bansoko/internal/attrrules"
	attrpersistence "bansoko/internal/attrrules/adapter/persistence"
	"bansoko/internal/namingvars"
	varspersistence "bansoko/internal/namingvars/adapter/persistence"
	"bansoko/internal/runtime/database"
)

func TestExactProgramPrivilegesAndPublication(t *testing.T) {
	owner := installedDB(t)
	ctx := context.Background()
	password := randomTestPassword(t)
	if err := database.ProvisionRuntimeRole(ctx, owner, database.RuntimeRoleConfig{Role: runtimeRoleTestName, Password: password}); err != nil {
		t.Fatal(err)
	}
	runtime := openRuntimeConnection(t, password)
	defer runtime.Close()

	rules := attrrules.Rules{Version: 1, Resolution: []string{"1080p"}, Quality: []string{"web"}, Language: []string{"jpn"}, SpecialMarkers: []string{"special"}, GroupBrackets: []string{"[", "]"}}
	attrFP, err := attrrules.Fingerprint(rules)
	if err != nil {
		t.Fatal(err)
	}
	attrRepo := attrpersistence.NewRepository(runtime)
	if err := attrRepo.Publish(ctx, rules, attrFP); err != nil {
		t.Fatal(err)
	}
	if err := attrRepo.Publish(ctx, rules, attrFP); err != nil {
		t.Fatalf("runtime attr republish: %v", err)
	}
	varsRepo := varspersistence.NewRepository(runtime)
	source := "vars.a = {b: 'first'};\r\n"
	varsFP, err := namingvars.SourceFingerprint(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := varsRepo.Publish(ctx, source, varsFP); err != nil {
		t.Fatal(err)
	}
	if got, fp, err := varsRepo.Current(ctx); err != nil || fp != varsFP || got != "vars.a = {b: 'first'};\n" {
		t.Fatalf("canonical runtime Vars read: %q %q %v", got, fp, err)
	}

	for _, table := range []string{"attr_rule_versions", "naming_vars_versions"} {
		t.Run(table, func(t *testing.T) {
			for _, statement := range []string{
				"UPDATE " + table + " SET content = '{}'::jsonb",
				"UPDATE " + table + " SET fingerprint = 'other'",
				"UPDATE " + table + " SET id = id + 100",
				"UPDATE " + table + " SET created_at = created_at + INTERVAL '1 second'",
				"DELETE FROM " + table,
				"TRUNCATE " + table,
			} {
				if _, err := runtime.ExecContext(ctx, statement); sqlState(err) != "42501" {
					t.Errorf("runtime mutation %s: %v, want 42501", statement, err)
				}
			}
			// Owner DML also cannot silently rewrite an existing content identity.
			for _, statement := range []string{
				"UPDATE " + table + " SET content = '{}'::jsonb",
				"UPDATE " + table + " SET fingerprint = 'other'",
				"UPDATE " + table + " SET id = id + 100",
				"UPDATE " + table + " SET created_at = created_at + INTERVAL '1 second'",
			} {
				if _, err := owner.ExecContext(ctx, statement); sqlState(err) != "23514" || adoptAcceptPGMessage(err) != "EXACT_PROGRAM_CONTENT_IMMUTABLE" {
					t.Errorf("immutable identity %s: %v", statement, err)
				}
			}
			if _, err := owner.ExecContext(ctx, "DELETE FROM "+table); sqlState(err) != "23514" || adoptAcceptPGMessage(err) != "EXACT_PROGRAM_DELETE_FORBIDDEN" {
				t.Errorf("owner delete: %v", err)
			}
			// Publication is the only mutable field and remains usable.
			if _, err := runtime.ExecContext(ctx, "UPDATE "+table+" SET active = FALSE"); err != nil {
				t.Fatal(err)
			}
		})
	}
	if err := attrRepo.Publish(ctx, rules, attrFP); err != nil {
		t.Fatalf("historical attr reactivation: %v", err)
	}
	if err := varsRepo.Publish(ctx, source, varsFP); err != nil {
		t.Fatalf("historical Vars reactivation: %v", err)
	}
}

func TestExactProgramPublicationRejectsConflictingStoredIdentity(t *testing.T) {
	db := installedDB(t)
	ctx := context.Background()
	// Admin-seeded corruption must fail closed, not silently preserve a
	// different body while claiming the requested fingerprint was published.
	rules := attrrules.Rules{Version: 1, Resolution: []string{"1080p"}, Quality: []string{"web"}, Language: []string{"jpn"}, SpecialMarkers: []string{"special"}, GroupBrackets: []string{"[", "]"}}
	goodAttr, _ := attrrules.Fingerprint(rules)
	otherRules := rules
	otherRules.Resolution = []string{"720p"}
	otherAttr, _ := attrrules.Fingerprint(otherRules)
	attrRepo := attrpersistence.NewRepository(db)
	if err := attrRepo.Publish(ctx, otherRules, otherAttr); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO attr_rule_versions(fingerprint,content,active) VALUES ($1,'{}',FALSE)`, goodAttr); err != nil {
		t.Fatal(err)
	}
	if err := attrRepo.Publish(ctx, rules, goodAttr); err == nil {
		t.Fatal("conflicting attr identity must fail")
	}
	var active string
	if err := db.QueryRowContext(ctx, `SELECT fingerprint FROM attr_rule_versions WHERE active`).Scan(&active); err != nil || active != otherAttr {
		t.Fatalf("failed attr publication changed active: %q %v", active, err)
	}

	varsRepo := varspersistence.NewRepository(db)
	goodSource, otherSource := "vars.a = {b: 'good'};", "vars.a = {b: 'other'};"
	goodVars, _ := namingvars.SourceFingerprint(goodSource)
	otherVars, _ := namingvars.SourceFingerprint(otherSource)
	if err := varsRepo.Publish(ctx, otherSource, otherVars); err != nil {
		t.Fatal(err)
	}
	badBody, _ := json.Marshal("vars.a = {b: 'corrupt'};")
	if _, err := db.ExecContext(ctx, `INSERT INTO naming_vars_versions(fingerprint,content,active) VALUES ($1,$2,FALSE)`, goodVars, badBody); err != nil {
		t.Fatal(err)
	}
	if err := varsRepo.Publish(ctx, goodSource, goodVars); err == nil {
		t.Fatal("conflicting Vars identity must fail")
	}
	if err := db.QueryRowContext(ctx, `SELECT fingerprint FROM naming_vars_versions WHERE active`).Scan(&active); err != nil || active != otherVars {
		t.Fatalf("failed Vars publication changed active: %q %v", active, err)
	}
	if err := varsRepo.Publish(ctx, goodSource, otherVars); err == nil {
		t.Fatal("caller-supplied fingerprint mismatch must fail before writing")
	}
	// The active no-op also checks immutable content instead of returning success.
	if _, err := db.ExecContext(ctx, `UPDATE naming_vars_versions SET active=FALSE WHERE active`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE naming_vars_versions SET active=TRUE WHERE fingerprint=$1`, goodVars); err != nil {
		t.Fatal(err)
	}
	if err := varsRepo.Publish(ctx, goodSource, goodVars); err == nil {
		t.Fatal("active corrupted identity must not become a successful no-op")
	}
	if _, _, err := varsRepo.Current(ctx); err == nil {
		t.Fatal("reading corrupted active content must fail closed")
	}
}

func TestExactProgramShareLocksPublication(t *testing.T) {
	db := installedDB(t)
	ctx := context.Background()
	for _, table := range []string{"attr_rule_versions", "naming_vars_versions"} {
		t.Run(table, func(t *testing.T) {
			if _, err := db.ExecContext(ctx, "INSERT INTO "+table+"(fingerprint,content,active) VALUES ('old','{}',TRUE)"); err != nil {
				t.Fatal(err)
			}
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			var before string
			if err := tx.QueryRowContext(ctx, "SELECT content::text FROM "+table+" WHERE fingerprint='old' FOR SHARE").Scan(&before); err != nil {
				t.Fatal(err)
			}
			conn, err := db.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			var pid int
			if err := conn.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
				t.Fatal(err)
			}
			callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, err := conn.ExecContext(callCtx, "UPDATE "+table+" SET active=FALSE WHERE active")
				done <- err
			}()
			ticker := time.NewTicker(10 * time.Millisecond)
			defer ticker.Stop()
			for {
				var blocked bool
				if err := db.QueryRowContext(callCtx, `SELECT cardinality(pg_blocking_pids($1)) > 0`, pid).Scan(&blocked); err != nil {
					t.Fatal(err)
				}
				if blocked {
					break
				}
				select {
				case err := <-done:
					t.Fatalf("publication did not wait for exact-content share lock: %v", err)
				case <-callCtx.Done():
					t.Fatal("publication never reached the row lock")
				case <-ticker.C:
				}
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			var after string
			var active bool
			if err := db.QueryRowContext(ctx, fmt.Sprintf("SELECT content::text,active FROM %s WHERE fingerprint='old'", table)).Scan(&after, &active); err != nil || after != before || active {
				t.Fatalf("historical content changed after publication: %q %t %v", after, active, err)
			}
		})
	}
}
