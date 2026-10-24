package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/persistence/txscope"
	plugintargetport "bansoko/internal/plugintarget/port"
)

// appendPendingAdmission admits one notice and returns its ledger row id. The
// admission stays pending: the resolver resolves it against the current target
// configuration on the next sweep. The request is carried verbatim, so a caller
// can name an owner the delivery row cannot store.
func appendPendingAdmission(t *testing.T, ctx context.Context, db *sql.DB, service *Service, request plugintargetport.BusinessDeliveryRequest) string {
	t.Helper()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.EnqueueBusinessDelivery(ctx, txscope.Wrap(tx), request); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var admissionID string
	if err := db.QueryRowContext(ctx, `SELECT id FROM notification_admissions WHERE result_key = $1`, request.ResultKey).Scan(&admissionID); err != nil {
		t.Fatal(err)
	}
	return admissionID
}

// poisonAdmissionRequest names a managed anime the delivery row cannot store:
// plugin_deliveries bounds the id shape, so every attempt fails the same way
// with a check violation. That is neither a transient database error nor a
// deterministic target failure, so the admission is retried until the bounded
// budget is spent.
func poisonAdmissionRequest(animeID, resultKey string) plugintargetport.BusinessDeliveryRequest {
	return plugintargetport.BusinessDeliveryRequest{
		SourceOwnerType: plugintargetport.OwnerDownloadTask, SourceOwnerID: "task-" + resultKey, SourceOwnerVersion: 1,
		ManagedAnimeID: animeID, TriggerKind: plugintargetport.TriggerDownloadSubmitted,
		Payload: json.RawMessage(`{"anime.title":"Poison"}`), ResultKey: resultKey,
	}
}

// fileAdmissionRequest is the well-formed notice the newer admission carries.
func fileAdmissionRequest(animeID, resultKey string) plugintargetport.BusinessDeliveryRequest {
	return plugintargetport.BusinessDeliveryRequest{
		SourceOwnerType: plugintargetport.OwnerFileOperation, SourceOwnerID: "operation-" + resultKey, SourceOwnerVersion: 1,
		ManagedAnimeID: animeID, TriggerKind: plugintargetport.TriggerFileOperationCompleted,
		Payload: json.RawMessage(`{"anime.title":"Poison"}`), ResultKey: resultKey,
	}
}

// A poison admission is retried with backoff, reaches a terminal failed state
// with a reason code after the bounded attempts, and a backlog of them does not
// starve a newer admission.
func TestAdmissionResolverPoisonAdmissionTerminatesWithoutStarvingNewer(t *testing.T) {
	db := openTestDB(t)
	service := newTestService(t, db)
	ctx := context.Background()

	// Every target is enabled (only an enabled target is eligible) and carries a
	// secret, which the enabled CHECK requires.
	poisonInput := validCreateInput(t)
	poisonInput.Name = "poison-target"
	poisonInput.Enabled = true
	poisonInput.Secret = secretSetInput(t)
	poisonInput.TriggerKinds = []TriggerKind{TriggerDownloadSubmitted}
	if _, err := service.CreateTarget(ctx, poisonInput, "poison-create"); err != nil {
		t.Fatal(err)
	}
	goodInput := validCreateInput(t)
	goodInput.Name = "good-target"
	goodInput.Enabled = true
	goodInput.Secret = secretSetInput(t)
	goodInput.TriggerKinds = []TriggerKind{TriggerFileOperationCompleted}
	if _, err := service.CreateTarget(ctx, goodInput, "good-create"); err != nil {
		t.Fatal(err)
	}
	// Each target serves one trigger, so each admission resolves against exactly
	// one target: the outcome under test belongs to that target alone.
	invalidInput := validCreateInput(t)
	invalidInput.Name = "invalid-config-target"
	invalidInput.Enabled = true
	invalidInput.Secret = secretSetInput(t)
	invalidInput.TriggerKinds = []TriggerKind{TriggerFileOperationFailed}
	invalid, err := service.CreateTarget(ctx, invalidInput, "invalid-create")
	if err != nil {
		t.Fatal(err)
	}
	animeID := "anime-poison"
	if _, err := db.ExecContext(ctx, `INSERT INTO managed_anime(id, title) VALUES ($1, 'Poison')`, animeID); err != nil {
		t.Fatal(err)
	}

	// The invalid-config target is deterministic: it settles on first sight.
	if _, err := db.ExecContext(ctx, `UPDATE plugin_target_configs SET config = '{"adapter_kind":"telegram","chat_id":"1","bot_token":"x"}'::jsonb WHERE id = $1`, invalid.ID); err != nil {
		t.Fatal(err)
	}

	poisonID := appendPendingAdmission(t, ctx, db, service, poisonAdmissionRequest("not a valid anime id", "poison-0"))
	// A backlog larger than one sweep batch, all older than the good admission.
	if _, err := db.ExecContext(ctx, `
INSERT INTO notification_admissions(id, trigger_kind, source_owner_type, source_owner_id, source_owner_version,
    result_key, managed_anime_id, occurred_at, event_catalog_version, event_template_catalog_version, payload, state)
SELECT id || '-' || n, trigger_kind, source_owner_type, source_owner_id || '-' || n, source_owner_version,
       result_key || '-' || n, managed_anime_id, occurred_at, event_catalog_version, event_template_catalog_version, payload, state
FROM notification_admissions, generate_series(1, 24) AS n WHERE id = $1`, poisonID); err != nil {
		t.Fatal(err)
	}
	goodID := appendPendingAdmission(t, ctx, db, service, fileAdmissionRequest(animeID, "good"))
	invalidID := appendPendingAdmission(t, ctx, db, service, plugintargetport.BusinessDeliveryRequest{
		SourceOwnerType: plugintargetport.OwnerFileOperation, SourceOwnerID: "operation-invalid", SourceOwnerVersion: 1,
		ManagedAnimeID: animeID, TriggerKind: plugintargetport.TriggerFileOperationFailed,
		Payload: json.RawMessage(`{"anime.title":"Poison"}`), ResultKey: "invalid",
	})

	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	resolver := &AdmissionResolver{
		Service: service, Logger: observabilityport.NopRuntimeLogger{},
		Now:         func() time.Time { return now },
		Backoff:     func(int) time.Duration { return time.Minute },
		MaxAttempts: 3,
	}
	admissionState := func(id string) (state, code string, attempts int) {
		t.Helper()
		var failure sql.NullString
		if err := db.QueryRowContext(ctx, `SELECT state, failure_code, attempt_count FROM notification_admissions WHERE id = $1`, id).Scan(&state, &failure, &attempts); err != nil {
			t.Fatal(err)
		}
		return state, failure.String, attempts
	}

	// Sweep 1 sees only the oldest batch, all poison. Sweep 2 at the same
	// instant skips the backed-off rows and reaches the newer admissions.
	for i := 0; i < 2; i++ {
		if _, err := resolver.SweepOnce(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if state, _, _ := admissionState(goodID); state != AdmissionResolved {
		t.Fatalf("newer admission state=%q, want resolved behind the poison backlog", state)
	}
	if state, code, _ := admissionState(invalidID); state != AdmissionFailed || code != "notification_target_failed" {
		t.Fatalf("invalid-config admission state=%q code=%q", state, code)
	}
	var deliveryState, deliveryCode, configStatus string
	if err := db.QueryRowContext(ctx, `SELECT state, last_error_code, config_status FROM plugin_deliveries WHERE plugin_target_config_id = $1`, invalid.ID).Scan(&deliveryState, &deliveryCode, &configStatus); err != nil {
		t.Fatal(err)
	}
	if deliveryState != "failed" || deliveryCode != codeTargetConfigInvalid || configStatus != "invalid" {
		t.Fatalf("invalid-config delivery state=%q code=%q config_status=%q", deliveryState, deliveryCode, configStatus)
	}
	if state, _, attempts := admissionState(poisonID); state != AdmissionPendingTargetResolution || attempts != 1 {
		t.Fatalf("poison after first failure state=%q attempts=%d, want pending_target_resolution/1", state, attempts)
	}

	// Advance past each backoff until the budget is spent.
	for i := 0; i < 6; i++ {
		now = now.Add(2 * time.Minute)
		for j := 0; j < 2; j++ {
			if _, err := resolver.SweepOnce(ctx); err != nil {
				t.Fatal(err)
			}
		}
	}
	var open, exhausted int
	if err := db.QueryRowContext(ctx, `
SELECT COUNT(*) FILTER (WHERE state = 'pending_target_resolution'),
       COUNT(*) FILTER (WHERE state = 'admission_failed' AND failure_code = $1 AND attempt_count = 3)
FROM notification_admissions WHERE id LIKE $2`, codeAdmissionRetryExhausted, poisonID+"%").Scan(&open, &exhausted); err != nil {
		t.Fatal(err)
	}
	if open != 0 || exhausted != 25 {
		t.Fatalf("poison admissions open=%d exhausted=%d, want 0/25", open, exhausted)
	}
	// Terminal rows are never swept again.
	now = now.Add(time.Hour)
	if created, err := resolver.SweepOnce(ctx); err != nil || created != 0 {
		t.Fatalf("resweep created=%d err=%v", created, err)
	}
}

func TestDefaultAdmissionBackoffIsBounded(t *testing.T) {
	if got := DefaultAdmissionBackoff(1); got != 5*time.Second {
		t.Fatalf("attempt 1 backoff=%s", got)
	}
	if got := DefaultAdmissionBackoff(3); got != 20*time.Second {
		t.Fatalf("attempt 3 backoff=%s", got)
	}
	if got := DefaultAdmissionBackoff(100); got != 30*time.Minute {
		t.Fatalf("attempt 100 backoff=%s", got)
	}
}
