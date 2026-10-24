package application

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"bansoko/internal/apperrors"
	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/redact"
)

func TestNewServiceNilDB(t *testing.T) {
	_, err := NewService(nil)
	if err == nil {
		t.Fatal("expected error for nil db")
	}
}

// TestListRecordsRejectsUnknownKind guards against the 500 that occurred when
// an unknown or comma-joined kind value (for example "business,plugin" sent as
// a single query value) reached the service and narrowed the union to nothing,
// producing a "SELECT ... FROM () AS combined" syntax error. Each unknown
// value must now surface as a typed 400 INVALID_KIND before any SQL is built.
func TestListRecordsRejectsUnknownKind(t *testing.T) {
	// The service never reaches the database when kind validation fails, so a
	// nil DB is sufficient to prove the rejection happens first.
	svc, err := NewService(&sql.DB{})
	if err != nil {
		t.Fatalf("new observability service: %v", err)
	}

	cases := []struct {
		name string
		kind string
	}{
		{"comma joined single value", "business,plugin"},
		{"arbitrary unknown value", "bogus"},
		{"empty string", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kinds := []string{tc.kind}
			_, err := svc.ListRecords(t.Context(), RecordQuery{Kind: &kinds})
			var bad *apperrors.BadRequest
			if !errors.As(err, &bad) {
				t.Fatalf("expected *apperrors.BadRequest, got %T: %v", err, err)
			}
			if bad.Code != "INVALID_KIND" {
				t.Fatalf("code: got %q, want INVALID_KIND", bad.Code)
			}
		})
	}
}

// TestListRecordsRejectsMixedValidUnknownKind ensures a single unknown value
// in a repeated kind list still rejects the whole request, rather than
// silently dropping the unknown value and narrowing to the valid one.
func TestListRecordsRejectsMixedValidUnknownKind(t *testing.T) {
	svc, err := NewService(&sql.DB{})
	if err != nil {
		t.Fatalf("new observability service: %v", err)
	}

	kinds := []string{"business", "bogus"}
	_, err = svc.ListRecords(t.Context(), RecordQuery{Kind: &kinds})
	var bad *apperrors.BadRequest
	if !errors.As(err, &bad) {
		t.Fatalf("expected *apperrors.BadRequest, got %T: %v", err, err)
	}
	if bad.Code != "INVALID_KIND" {
		t.Fatalf("code: got %q, want INVALID_KIND", bad.Code)
	}
}

func TestRecordFromRowWrapsBusinessEntryWithLineageContract(t *testing.T) {
	record, err := recordFromRow("business", []byte(`{
		"id":"record-1",
		"subscription_id":"subscription-1",
		"managed_anime_id":"anime-1",
		"kind":"release_processed",
		"outcome":"info",
		"occurred_at":"2026-09-10T12:00:00Z",
		"title":"Trace Show",
		"summary":"filtered",
		"release_id":"release-1",
		"reason_code":"group_filtered",
		"subject_type":"release",
		"subject_id":"release-1",
		"subject_version":2,
		"correlation_id":null,
		"causation_type":null,
		"causation_id":null,
		"dedupe_key":"record-1",
		"retention_until":"2026-10-10T12:00:00Z",
		"created_at":"2026-09-10T12:00:00Z"
	}`))
	if err != nil {
		t.Fatal(err)
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(record.Raw, &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope) != 2 || string(envelope["record_type"]) != `"business"` || envelope["entry"] == nil {
		t.Fatalf("business record must match the discriminated union: %s", record.Raw)
	}
	if envelope["business"] != nil {
		t.Fatalf("business payload must use the contract key entry: %s", record.Raw)
	}
	var entry map[string]json.RawMessage
	if err := json.Unmarshal(envelope["entry"], &entry); err != nil {
		t.Fatal(err)
	}
	if string(entry["release_id"]) != `"release-1"` || string(entry["subject_version"]) != "2" {
		t.Fatalf("business lineage was dropped: %s", envelope["entry"])
	}
	// This wrapper forwards the row payload it is given, so a storage-only
	// column is excluded by the read projection's explicit allowlist and again
	// by the transport's contract projection. Both are covered where they live:
	// the union SQL is exercised by the PostgreSQL lane, and
	// TestMapObservabilityRecordProjectsContractAllowlist pins the transport
	// allowlist without a database.
}

func TestNewRetentionServiceNilDB(t *testing.T) {
	_, err := NewRetentionService(nil)
	if err == nil {
		t.Fatal("expected error for nil db")
	}
}

func TestRetentionResultTotal(t *testing.T) {
	result := RetentionResult{
		PluginDeliveryAttempts: 1,
		PluginDeliveries:       2,
		SubscriptionLogEntries: 3,
		AutomationDispatchRuns: 4,
		DiagnosticEpisodes:     5,
	}
	if got, want := result.Total(), int64(15); got != want {
		t.Fatalf("total: got %d, want %d", got, want)
	}
}

func TestCursorRoundTrip(t *testing.T) {
	now := time.Date(2026, 7, 24, 12, 30, 45, 123456789, time.UTC)
	id := "rec-abc-123"

	encoded := encodeCursor(now, 2, id)
	if encoded == "" {
		t.Fatal("encoded cursor is empty")
	}

	gotTime, gotRank, gotID, err := decodeCursor(encoded)
	if err != nil {
		t.Fatalf("decodeCursor: %v", err)
	}
	if !gotTime.Equal(now) {
		t.Errorf("time mismatch: got %v, want %v", gotTime, now)
	}
	if gotRank != 2 {
		t.Errorf("rank mismatch: got %d, want 2", gotRank)
	}
	if gotID != id {
		t.Errorf("id mismatch: got %q, want %q", gotID, id)
	}
}

func TestDecodeCursorRejectsMalformedShape(t *testing.T) {
	// A payload without the version and rank fields must not be silently
	// accepted: it would misposition the keyset and skip or repeat rows.
	malformed := base64.RawURLEncoding.EncodeToString([]byte("2026-07-24T12:30:45Z|rec-abc-123"))
	if _, _, _, err := decodeCursor(string(malformed)); err == nil {
		t.Fatal("expected two-field cursor to be rejected")
	}
}

func TestDecodeCursorRejectsWrongVersion(t *testing.T) {
	wrongVersion := base64.RawURLEncoding.EncodeToString([]byte("9|2026-07-24T12:30:45Z|2|rec-abc-123"))
	if _, _, _, err := decodeCursor(string(wrongVersion)); err == nil {
		t.Fatal("expected unknown cursor version to be rejected")
	}
}

func TestDecodeCursorInvalidBase64(t *testing.T) {
	_, _, _, err := decodeCursor("not-valid-base64!!!")
	if err == nil {
		t.Fatal("expected error for invalid base64")
	}
}

func TestDecodeCursorMalformed(t *testing.T) {
	// Valid base64 but no field separators
	_, _, _, err := decodeCursor("aGVsbG8")
	if err == nil {
		t.Fatal("expected error for malformed cursor")
	}
}

func TestDecodeCursorBadTime(t *testing.T) {
	// Valid base64 with fields but invalid time
	_, _, _, err := decodeCursor("Mnxyb2NrZXQtfDItcmVj")
	if err == nil {
		t.Fatal("expected error for bad time in cursor")
	}
}

// TestListRecordsRejectsStaleCursor proves a malformed or legacy cursor is a
// typed 400 INVALID_CURSOR raised before any database access, not a 500.
func TestListRecordsRejectsStaleCursor(t *testing.T) {
	svc, err := NewService(&sql.DB{})
	if err != nil {
		t.Fatalf("new observability service: %v", err)
	}

	legacy := base64.RawURLEncoding.EncodeToString([]byte("2026-07-24T12:30:45Z|rec-abc-123"))
	_, err = svc.ListRecords(t.Context(), RecordQuery{Cursor: &legacy})
	var bad *apperrors.BadRequest
	if !errors.As(err, &bad) {
		t.Fatalf("expected *apperrors.BadRequest, got %T: %v", err, err)
	}
	if bad.Code != "INVALID_CURSOR" {
		t.Fatalf("code: got %q, want INVALID_CURSOR", bad.Code)
	}

	_, err = svc.ListDiagnostics(t.Context(), DiagnosticQuery{Cursor: &legacy})
	if !errors.As(err, &bad) {
		t.Fatalf("expected *apperrors.BadRequest, got %T: %v", err, err)
	}
	if bad.Code != "INVALID_CURSOR" {
		t.Fatalf("code: got %q, want INVALID_CURSOR", bad.Code)
	}
}

// TestListStreamsRejectForeignCursorRank proves the shared cursor codec is not
// a cross-stream position: a union cursor carries a source rank that the
// single-table diagnostics stream never issues, and a diagnostics cursor never
// carries one, so neither stream may reinterpret the other's position.
func TestListStreamsRejectForeignCursorRank(t *testing.T) {
	svc, err := NewService(&sql.DB{})
	if err != nil {
		t.Fatalf("new observability service: %v", err)
	}
	position := time.Date(2026, 9, 12, 8, 30, 0, 0, time.UTC)

	unionCursor := encodeCursor(position, recordSourceRanks["business"], "record-1")
	diagnosticCursor := encodeCursor(position, diagnosticCursorRank, "episode-1")

	assertInvalid := func(t *testing.T, err error, surface string) {
		t.Helper()
		var bad *apperrors.BadRequest
		if !errors.As(err, &bad) {
			t.Fatalf("%s: expected *apperrors.BadRequest, got %T: %v", surface, err, err)
		}
		if bad.Code != "INVALID_CURSOR" {
			t.Fatalf("%s: code: got %q, want INVALID_CURSOR", surface, bad.Code)
		}
	}

	_, err = svc.ListDiagnostics(t.Context(), DiagnosticQuery{Cursor: &unionCursor})
	assertInvalid(t, err, "diagnostics with a union cursor")

	_, err = svc.ListRecords(t.Context(), RecordQuery{Cursor: &diagnosticCursor})
	assertInvalid(t, err, "records with a diagnostics cursor")

	// Every projected union rank must be one the validator accepts, otherwise a
	// cursor this stream issued could not be replayed against itself.
	for kind, rank := range recordSourceRanks {
		if !validRecordSourceRank(rank) {
			t.Fatalf("record source %q projects rank %d that the cursor validator rejects", kind, rank)
		}
		if rank == diagnosticCursorRank {
			t.Fatalf("record source %q must not share the diagnostics rank %d", kind, diagnosticCursorRank)
		}
	}
}

func TestNullableStringPointer(t *testing.T) {
	valid := sql.NullString{String: "hello", Valid: true}
	if p := nullableStringPointer(valid); p == nil || *p != "hello" {
		t.Errorf("expected 'hello', got %v", p)
	}

	invalid := sql.NullString{Valid: false}
	if p := nullableStringPointer(invalid); p != nil {
		t.Errorf("expected nil, got %v", *p)
	}
}

func TestValidateRoutineRejectsInvalidScopeAndNestedSecret(t *testing.T) {
	valid := observabilityport.RoutineRequest{
		Identity: observabilityport.AutomationDispatchRoutine(observabilityport.RoutineOutcomeSuccess, observabilityport.RoutineResultDispatchCompleted),
		ScopeID:  "dispatch", Summary: "completed",
	}
	if err := validateRoutine(valid); err != nil {
		t.Fatalf("valid routine rejected: %v", err)
	}
	valid.Identity.ScopeKind, valid.ScopeID = observabilityport.RoutineScopeGlobal, "global"
	if err := validateRoutine(valid); err != nil {
		t.Fatalf("valid global routine rejected: %v", err)
	}
	valid.ScopeID = "automation"
	valid.Identity.ScopeKind, valid.ScopeID = observabilityport.RoutineScopeAutomation, "rss_refresh"
	valid.Details = json.RawMessage(`{"nested":{"url":"https://example.test/hook?token=secret"}}`)
	if err := validateRoutine(valid); err == nil {
		t.Fatal("expected nested secret URL validation error")
	}
}

func TestValidateDiagnosticRequests(t *testing.T) {
	failure := observabilityport.DiagnosticFailureRequest{
		Identity: observabilityport.AutomationDispatchFailure(observabilityport.ScopeWorker), Severity: observabilityport.SeverityError,
		ScopeID: "rss_refresh", LastError: "dispatch failed",
	}
	failure.Identity.ScopeKind = "automation"
	if err := validateDiagnosticFailure(failure); err == nil {
		t.Fatal("diagnostic scope must reject automation; use worker or owner scope")
	}
	replacement := observabilityport.DiagnosticFailureRequest{Identity: observabilityport.ReplacementTriggerFailure(), Severity: observabilityport.SeverityError, ScopeID: "anime-1", ManagedAnimeID: "anime-1", LastError: "replacement trigger failed"}
	if err := validateDiagnosticFailure(replacement); err != nil {
		t.Fatalf("production replacement identity rejected: %v", err)
	}
	placement := observabilityport.DiagnosticFailureRequest{Identity: observabilityport.PlacementAdmissionFailure(), Severity: observabilityport.SeverityWarning, ScopeID: "op-1", LastError: "placement admission failed", Details: json.RawMessage(`{"reason_code":"placement_evidence_missing"}`)}
	if err := validateDiagnosticFailure(placement); err != nil {
		t.Fatalf("production placement identity rejected: %v", err)
	}
	if err := validateDiagnosticSuccess(observabilityport.DiagnosticSuccessRequest{Identity: observabilityport.PlacementAdmissionFailure(), ScopeID: "op-1"}); err != nil {
		t.Fatalf("production placement settle identity rejected: %v", err)
	}

}

func TestActivityValidationAndDedupe(t *testing.T) {
	record := observabilityport.ActivityRecord{
		SubscriptionID: "sub-1", ManagedAnimeID: "anime-1", Kind: "policy_changed", Outcome: "info",
		ReasonCode: "rules_policy_changed", SubjectType: "rules", SubjectID: "anime-1", SubjectVersion: 3,
		Title: "Anime", Summary: "规则策略已更新",
	}
	if err := validateActivity(record); err != nil {
		t.Fatalf("valid activity rejected: %v", err)
	}
	if got, want := activityDedupeKey(record), "policy_changed:rules:anime-1:v3:rules_policy_changed"; got != want {
		t.Fatalf("dedupe: got %q, want %q", got, want)
	}

	metadata := observabilityport.ActivityRecord{
		SubscriptionID: "sub-1", ManagedAnimeID: "anime-1", Kind: "metadata_applied", Outcome: "success",
		ReasonCode: "metadata_applied", SubjectType: "metadata", SubjectID: "anime-1", SubjectVersion: 2,
		Title: "Anime", Summary: "已关联 Bangumi、TMDB",
	}
	if err := validateActivity(metadata); err != nil {
		t.Fatalf("metadata activity rejected: %v", err)
	}
	if got, want := activityDedupeKey(metadata), "metadata_applied:metadata:anime-1:v2:metadata_applied"; got != want {
		t.Fatalf("metadata dedupe: got %q, want %q", got, want)
	}

	record.Kind = "rss_refresh_failed"
	if err := validateActivity(record); err != nil {
		t.Fatalf("RSS refresh failure activity rejected: %v", err)
	}
	record.Kind = "release_processed"
	if err := validateActivity(record); err != nil {
		t.Fatalf("release processed activity rejected: %v", err)
	}
	record.Kind = "group_changed"
	record.SubjectVersion = 0
	if err := validateActivity(record); err == nil {
		t.Fatal("expected invalid subject version rejection")
	}
}

func TestValidateActivityRejectsSensitiveText(t *testing.T) {
	base := observabilityport.ActivityRecord{
		SubscriptionID: "sub-1", ManagedAnimeID: "anime-1", Kind: "metadata_applied", Outcome: "success",
		ReasonCode: "metadata_applied", SubjectType: "metadata", SubjectID: "anime-1", SubjectVersion: 2,
		Title: "Anime", Summary: "已关联 Bangumi、TMDB",
	}
	cases := []struct {
		name string
		edit func(*observabilityport.ActivityRecord)
	}{
		{"url in title", func(record *observabilityport.ActivityRecord) {
			record.Title = "fetch https://user:pass@example.test/meta"
		}},
		{"magnet in summary", func(record *observabilityport.ActivityRecord) { record.Summary = "magnet:?xt=urn:btih:deadbeef" }},
		{"dsn in summary", func(record *observabilityport.ActivityRecord) {
			record.Summary = "connect postgres://user:pass@example.test/db"
		}},
		{"credential in details", func(record *observabilityport.ActivityRecord) {
			record.Details = json.RawMessage(`{"provider":"metadata","access_token":"secret"}`)
		}},
		{"cookie key in details", func(record *observabilityport.ActivityRecord) {
			record.Details = json.RawMessage(`{"cookie":"opaque"}`)
		}},
		{"passkey key in details", func(record *observabilityport.ActivityRecord) {
			record.Details = json.RawMessage(`{"passkey":"opaque"}`)
		}},
		{"dsn key in details", func(record *observabilityport.ActivityRecord) {
			record.Details = json.RawMessage(`{"dsn":"opaque"}`)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			record := base
			tc.edit(&record)
			if err := validateActivity(record); err == nil {
				t.Fatal("expected sensitive Activity text rejection")
			}
		})
	}
}

// TestSanitizeDiagnosticLastErrorRedactsLegacyText proves stored values written
// before the writer's validation are re-redacted and bounded at the read
// boundary: credential markers, URLs and private paths collapse, multi-byte
// text truncates by runes, and the result always satisfies the contract's
// non-empty bound.
func TestSanitizeDiagnosticLastErrorRedactsLegacyText(t *testing.T) {
	redacted := []string{
		"provider replied api_key=AKIAIOSFODNN7EXAMPLE",
		"token=abcdef0123456789",
		"fetch https://user:pass@example.test/private",
		"failed at /home/alice/private/torrent",
		`open C:\Users\alice\Downloads\show.mkv`,
		`copy \\nas\share\private\show.mkv`,
		"config ~/.config/app/settings.toml",
		"escape via ../secrets/key.pem",
	}
	for _, value := range redacted {
		got := sanitizeDiagnosticLastError(value)
		if got != redact.Redacted {
			t.Fatalf("sanitizeDiagnosticLastError(%q) = %q, want %q", value, got, redact.Redacted)
		}
	}
	if got := sanitizeDiagnosticLastError("   "); got != redact.Redacted {
		t.Fatalf("whitespace-only legacy value must stay non-empty, got %q", got)
	}
	ordinary := "provider rejected the request with http_403"
	if got := sanitizeDiagnosticLastError(ordinary); got != ordinary {
		t.Fatalf("ordinary text changed: %q", got)
	}
	// Multi-byte truncation must land exactly on the bound without splitting a
	// rune or overflowing it.
	multiByte := strings.Repeat("診", 9000)
	got := sanitizeDiagnosticLastError(multiByte)
	if len([]rune(got)) != 8192 {
		t.Fatalf("multi-byte truncation = %d runes, want 8192", len([]rune(got)))
	}
}

// TestRecordFromRowProjectsPluginPayloadStatus proves the union member carries
// the delivery payload through the same projection the dedicated endpoint
// uses: a canonical payload is served, a legacy payload is withheld with an
// invalid status, and a row without a payload reports unavailable.
func TestRecordFromRowProjectsPluginPayloadStatus(t *testing.T) {
	cases := []struct {
		name       string
		payload    string
		wantStatus string
		wantServed bool
	}{
		{
			name:       "canonical payload",
			payload:    `{"schema_version":1,"trigger_kind":"download_submitted","facts":{"anime.title":"Show"}}`,
			wantStatus: "available",
			wantServed: true,
		},
		{
			name:       "legacy test request body",
			payload:    `{"expected_version":1,"trigger_kind":"download_submitted"}`,
			wantStatus: "invalid",
		},
		{
			name:       "legacy facts map",
			payload:    `{"anime.title":"Show"}`,
			wantStatus: "invalid",
		},
		{
			name:       "missing payload",
			payload:    `null`,
			wantStatus: "unavailable",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			row := `{"id":"delivery-1","trigger_kind":"download_submitted","payload":` + testCase.payload + `,"payload_digest":"sha256:x"}`
			record, err := recordFromRow("plugin", []byte(row))
			if err != nil {
				t.Fatalf("recordFromRow: %v", err)
			}
			var envelope struct {
				RecordType string `json:"record_type"`
				Delivery   struct {
					Payload       json.RawMessage `json:"payload"`
					PayloadStatus string          `json:"payload_status"`
				} `json:"delivery"`
			}
			if err := json.Unmarshal(record.Raw, &envelope); err != nil {
				t.Fatal(err)
			}
			if envelope.RecordType != "plugin" {
				t.Fatalf("record_type = %q, want plugin", envelope.RecordType)
			}
			if envelope.Delivery.PayloadStatus != testCase.wantStatus {
				t.Fatalf("payload_status = %q, want %q", envelope.Delivery.PayloadStatus, testCase.wantStatus)
			}
			if testCase.wantServed {
				if string(envelope.Delivery.Payload) == "null" || len(envelope.Delivery.Payload) == 0 {
					t.Fatalf("a canonical payload must be served: %s", record.Raw)
				}
				return
			}
			if string(envelope.Delivery.Payload) != "null" {
				t.Fatalf("an unreadable payload must be withheld, got %s", envelope.Delivery.Payload)
			}
		})
	}
}
