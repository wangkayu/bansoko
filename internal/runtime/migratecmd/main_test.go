package migratecmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	observabilityapplication "bansoko/internal/observability/application"
	"bansoko/internal/runtime/migrations"
)

func TestMigrationFailureSummaryUsesStructuredErrorThroughWrapping(t *testing.T) {
	cause := errors.New("permission denied: password=top-secret dsn=postgres://user:pass@example/db SQL=/private/data")
	for _, operation := range []string{"apply", "record"} {
		err := fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", &migrations.MigrationApplyError{Operation: operation, Version: "999_custom.sql", Err: cause}))
		got := migrationFailureSummary(err)
		if !strings.Contains(got, operation) || !strings.Contains(got, "999_custom.sql") || !strings.Contains(got, "permission_denied") {
			t.Fatalf("summary = %q, want operation, version and class", got)
		}
		for _, secret := range []string{"top-secret", "postgres://", "user:pass", "/private/data", "password", "dsn="} {
			if strings.Contains(got, secret) {
				t.Fatalf("summary leaked %q: %q", secret, got)
			}
		}
	}
}

func TestMigrationFailureSummaryUsesCauseSentinelsDespitePollutedApplyError(t *testing.T) {
	cases := []struct {
		name   string
		signal error
		class  string
	}{
		{"checksum", migrations.ErrChecksumMismatch, "checksum_mismatch"},
		{"invalid set", migrations.ErrInvalidMigrationSet, "invalid_migration_set"},
		{"unsupported schema", migrations.ErrUnsupportedSchema, "schema_mismatch"},
		{"identity", migrations.ErrSchemaIdentity, "schema_mismatch"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cause := fmt.Errorf("wrapped cause: %w", tc.signal)
			err := fmt.Errorf("outer checksum migration %s: %w", "polluted_checksum.sql", fmt.Errorf("inner: %w", &migrations.MigrationApplyError{
				Operation: "apply", Version: "999_checksum.sql", Err: cause,
			}))
			got := migrationFailureSummary(err)
			if !strings.Contains(got, tc.class) || !strings.Contains(got, "apply") || !strings.Contains(got, "999_checksum.sql") {
				t.Fatalf("summary = %q, want operation, version and %s", got, tc.class)
			}
		})
	}

	permission := migrationFailureSummary(fmt.Errorf("outer checksum %w", &migrations.MigrationApplyError{
		Operation: "record", Version: "999_checksum.sql", Err: errors.New("permission denied"),
	}))
	if !strings.Contains(permission, "permission_denied") || strings.Contains(permission, "checksum_mismatch") {
		t.Fatalf("summary = %q, want permission_denied only", permission)
	}
}

func TestMigrationFailureSummaryUsesCauseTextNotOuterWrapper(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"generic cause with known outer wrapper", fmt.Errorf("connection refused: %w", errors.New("migration failed")), "migration_error"},
		{"permission cause", fmt.Errorf("connection refused: %w", errors.New("permission denied")), "permission_denied"},
		{"connection cause", fmt.Errorf("permission denied: %w", errors.New("connection refused")), "database_unavailable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := safeMigrationErrorClass(tc.err); got != tc.want {
				t.Fatalf("class = %q, want %q", got, tc.want)
			}
		})
	}
}
func TestFatalStartupWritesRedactedBoundedJSON(t *testing.T) {
	cases := []struct {
		name      string
		message   string
		wantExact string
	}{
		{"non-sensitive", strings.Repeat("界", 300), strings.Repeat("界", 170)},
		{"invalid UTF-8", "prefix\xff\xfe界", "prefix�界"},
		{"sensitive", "database failed: password=top-secret dsn=postgres://user:pass@example/db", "startup failed (details redacted)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var output bytes.Buffer
			logger := observabilityapplication.NewJSONRuntimeLogger(&output)
			previousExit := exitProcess
			exitProcess = func(code int) {
				if code != 1 {
					t.Errorf("exit code = %d, want 1", code)
				}
			}
			defer func() { exitProcess = previousExit }()

			fatalStartup(logger, tc.message, "migration_apply_failed")
			var record map[string]any
			if err := json.Unmarshal(output.Bytes(), &record); err != nil {
				t.Fatalf("decode runtime JSON: %v; output=%q", err, output.String())
			}
			for key, want := range map[string]string{"level": "error", "component": "migrate", "operation": "startup", "outcome": "failure", "reason_code": "migration_apply_failed", "error_class": "startup_fatal"} {
				if got := record[key]; got != want {
					t.Errorf("%s = %v, want %q", key, got, want)
				}
			}
			message, ok := record["message"].(string)
			if !ok || !utf8.ValidString(message) || len(message) > 512 {
				t.Fatalf("message = %q, valid UTF-8 and at most 512 bytes required", message)
			}
			if message != tc.wantExact {
				t.Errorf("message = %q, want %q", message, tc.wantExact)
			}
			for _, secret := range []string{"top-secret", "postgres://", "user:pass", "password", "dsn"} {
				if strings.Contains(output.String(), secret) {
					t.Fatalf("runtime output leaked %q: %q", secret, output.String())
				}
			}
		})
	}
}

func TestMigrationFailureSummaryKeepsMigrationNameAndRedactsDetails(t *testing.T) {
	err := &migrations.MigrationApplyError{
		Operation: "apply",
		Version:   "073_filemaintenance.sql",
		Err:       errors.New("password=top-secret dsn=postgres://user:pass@example/db permission denied: /private/data"),
	}

	got := migrationFailureSummary(err)
	if !strings.Contains(got, "073_filemaintenance.sql") {
		t.Fatalf("summary = %q, want migration filename", got)
	}
	if !strings.Contains(got, "permission_denied") {
		t.Fatalf("summary = %q, want safe error class", got)
	}
	for _, secret := range []string{"top-secret", "postgres://", "user:pass", "/private/data", "password", "dsn="} {
		if strings.Contains(got, secret) {
			t.Fatalf("summary leaked %q: %q", secret, got)
		}
	}
}

func TestMigrationFailureSummaryClassifiesWithoutMigrationName(t *testing.T) {
	got := migrationFailureSummary(errors.New("read migration directory: open /home/app/schema: no such file or directory"))
	if got != "migration apply failed (migration_error)" {
		t.Fatalf("summary = %q, want bounded generic summary", got)
	}
	if strings.Contains(got, "/home/app/schema") {
		t.Fatalf("summary leaked path: %q", got)
	}
}

func TestFatalStartupJSONFieldsAndMessageSafety(t *testing.T) {
	var output bytes.Buffer
	logger := observabilityapplication.NewJSONRuntimeLogger(&output)
	previousExit := exitProcess
	exitProcess = func(int) {}
	defer func() { exitProcess = previousExit }()

	fatalStartup(logger, strings.Repeat("界", 300), "input_reason")
	var record map[string]any
	if err := json.Unmarshal(output.Bytes(), &record); err != nil {
		t.Fatalf("decode runtime JSON: %v", err)
	}
	for key, want := range map[string]string{"level": "error", "component": "migrate", "operation": "startup", "outcome": "failure", "reason_code": "input_reason", "error_class": "startup_fatal"} {
		if got := record[key]; got != want {
			t.Errorf("%s = %v, want %q", key, got, want)
		}
	}
	message, ok := record["message"].(string)
	if !ok || !utf8.ValidString(message) || len(message) > 512 {
		t.Fatalf("message = %q, valid UTF-8 and at most 512 bytes required", message)
	}
	if message != strings.Repeat("界", 170) {
		t.Fatalf("message = %q, want exact safe truncation", message)
	}
}
