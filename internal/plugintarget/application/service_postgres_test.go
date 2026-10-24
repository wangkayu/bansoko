package application

import (
	"bansoko/internal/testutil/acquisitionfixture"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"bansoko/internal/apperrors"
	configurationpersistence "bansoko/internal/configuration/adapter/persistence"
	configurationapplication "bansoko/internal/configuration/application"
	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/persistence/txscope"
	deliverypayload "bansoko/internal/plugintarget/payload"
	plugintargetport "bansoko/internal/plugintarget/port"
	runtimeworker "bansoko/internal/runtime/worker"
	templateapplication "bansoko/internal/templatecatalog/application"
	"bansoko/internal/testutil/disposabledb"
	"bansoko/internal/workitems"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db := disposabledb.ResetDisposableSchema(t)
	return db
}

func newTestService(t *testing.T, db *sql.DB) *Service {
	t.Helper()
	secrets, err := configurationapplication.NewDatabaseSecretStore(db, []byte("test-secret-encryption-key-0123456789abcdef"), configurationpersistence.NewRepository(db))
	if err != nil {
		t.Fatalf("new secret store: %v", err)
	}
	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatalf("new work item store: %v", err)
	}
	service, err := NewService(db, secrets, store)
	if err != nil {
		t.Fatalf("new plugin target service: %v", err)
	}
	return service
}

func telegramSecretValue(t *testing.T, botToken string) json.RawMessage {
	t.Helper()
	return json.RawMessage(`{"adapter_kind":"telegram","bot_token":"` + botToken + `"}`)
}

func secretSetInput(t *testing.T) SecretInput {
	t.Helper()
	return SecretInput{Mode: "set", Value: telegramSecretValue(t, "123:abc")}
}

func replaceSecretInput(t *testing.T, botToken string) SecretInput {
	t.Helper()
	return SecretInput{Mode: "replace", Value: telegramSecretValue(t, botToken)}
}

func removeSecretInput(t *testing.T) SecretInput {
	t.Helper()
	return SecretInput{Mode: "remove"}
}

func updateTargetInput(t *testing.T, name string, enabled bool, secret SecretInput) TargetUpdateInput {
	t.Helper()
	return TargetUpdateInput{TargetInput: TargetInput{
		AdapterKind:         AdapterTelegram,
		Config:              telegramConfig(t, "67890"),
		ConfigSchemaVersion: 1,
		Enabled:             enabled,
		Name:                name,
		PluginKind:          PluginKindNotification,
		Secret:              secret,
		TemplateSet:         defaultTemplateSet(t),
		TriggerKinds:        []TriggerKind{TriggerDownloadSubmitted},
	}}
}

func TestCreateListUpdateTestDelivery(t *testing.T) {
	db := openTestDB(t)
	service := newTestService(t, db)
	ctx := context.Background()

	// Create an enabled target with a secret.
	createInput := validCreateInput(t)
	createInput.Enabled = true
	createInput.Secret = secretSetInput(t)
	created, err := service.CreateTarget(ctx, createInput, "create-idempotency-key-001")
	if err != nil {
		t.Fatalf("create target: %v", err)
	}
	if created.ID == "" || created.Version != 1 {
		t.Fatalf("expected created target version 1, got %+v", created)
	}
	if created.HasSecret == nil || !*created.HasSecret {
		t.Fatalf("expected has_secret true, got %+v", created.HasSecret)
	}

	// List returns the target without secret material.
	page, err := service.ListTargets(ctx)
	if err != nil {
		t.Fatalf("list targets: %v", err)
	}
	if len(page.Targets) != 1 {
		t.Fatalf("expected 1 target, got %d", len(page.Targets))
	}
	raw, err := json.Marshal(page.Targets[0])
	if err != nil {
		t.Fatalf("marshal target: %v", err)
	}
	if containsAny(string(raw), "secret_ref", "123:abc") {
		t.Fatalf("read model leaked secret material: %s", raw)
	}

	// Update with correct CAS version, keeping the secret.
	updateInput := TargetUpdateInput{TargetInput: TargetInput{
		AdapterKind:         AdapterTelegram,
		Config:              telegramConfig(t, "67890"),
		ConfigSchemaVersion: 1,
		Enabled:             true,
		Name:                "Renamed Target",
		PluginKind:          PluginKindNotification,
		Secret:              SecretInput{Mode: "keep"},
		TemplateSet:         defaultTemplateSet(t),
		TriggerKinds:        []TriggerKind{TriggerDownloadSubmitted, TriggerFileOperationCompleted, TriggerFileOperationFailed},
	}, ExpectedVersion: 1}
	updated, err := service.UpdateTarget(ctx, created.ID, 1, updateInput)
	if err != nil {
		t.Fatalf("update target: %v", err)
	}
	if updated.Version != 2 || updated.Name != "Renamed Target" {
		t.Fatalf("expected version 2 renamed target, got %+v", updated)
	}
	if updated.HasSecret == nil || !*updated.HasSecret {
		t.Fatalf("expected secret retained after keep, got %+v", updated.HasSecret)
	}

	// Stale-version update is a conflict.
	_, err = service.UpdateTarget(ctx, created.ID, 1, updateInput)
	if err == nil {
		t.Fatal("expected version conflict on stale update")
	}

	// Test delivery against version 2.
	accepted, err := service.TestTarget(ctx, created.ID, TargetTestInput{
		ExpectedVersion: 2,
		TriggerKind:     TriggerDownloadSubmitted,
	}, "test-idempotency-key-0001")
	if err != nil {
		t.Fatalf("test target: %v", err)
	}
	if !bool(accepted.Accepted) || accepted.DeliveryID == "" {
		t.Fatalf("expected accepted delivery, got %+v", accepted)
	}

	// Repeating the same test replays the same delivery id.
	replayed, err := service.TestTarget(ctx, created.ID, TargetTestInput{
		ExpectedVersion: 2,
		TriggerKind:     TriggerDownloadSubmitted,
	}, "test-idempotency-key-0001")
	if err != nil {
		t.Fatalf("replay test target: %v", err)
	}
	if replayed.DeliveryID != accepted.DeliveryID {
		t.Fatalf("expected replayed delivery id %s, got %s", accepted.DeliveryID, replayed.DeliveryID)
	}

	// Reusing the key with a different trigger is a conflict.
	_, err = service.TestTarget(ctx, created.ID, TargetTestInput{
		ExpectedVersion: 2,
		TriggerKind:     TriggerFileOperationCompleted,
	}, "test-idempotency-key-0001")
	if err == nil {
		t.Fatal("expected idempotency conflict on reused key with different trigger")
	}

	// A work item was created for the delivery.
	var workItemCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_item_history WHERE owner_type='plugin_delivery' AND owner_id = $1`, accepted.DeliveryID).Scan(&workItemCount); err != nil {
		t.Fatalf("count work items: %v", err)
	}
	if workItemCount != 1 {
		t.Fatalf("expected 1 work item for delivery, got %d", workItemCount)
	}

	// GetDelivery returns the delivery without secret material.
	detail, err := service.GetDelivery(ctx, accepted.DeliveryID)
	if err != nil {
		t.Fatalf("get delivery: %v", err)
	}
	deliveryID := deliveryField(t, detail.Delivery, "id")
	if deliveryID != accepted.DeliveryID {
		t.Fatalf("expected delivery %s, got %s", accepted.DeliveryID, deliveryID)
	}
	deliveryRaw, err := json.Marshal(detail.Delivery)
	if err != nil {
		t.Fatalf("marshal delivery: %v", err)
	}
	if containsAny(string(deliveryRaw), "secret_ref", "123:abc") {
		t.Fatalf("delivery read model leaked secret material: %s", deliveryRaw)
	}

	// Unknown delivery is not found.
	_, err = service.GetDelivery(ctx, "missing-delivery-id")
	var notFound *apperrors.NotFound
	if !errors.As(err, &notFound) {
		t.Fatalf("expected not found for missing delivery, got %v", err)
	}
}

func TestUpdateTargetRetainsFrozenSecretVersionsPostgres(t *testing.T) {
	db := openTestDB(t)
	service := newTestService(t, db)
	ctx := context.Background()

	createInput := validCreateInput(t)
	createInput.Name = "retained-secret-target"
	createInput.Enabled = true
	createInput.Secret = secretSetInput(t)
	created, err := service.CreateTarget(ctx, createInput, "retained-secret-create-key")
	if err != nil {
		t.Fatal(err)
	}

	var originalRef string
	if err := db.QueryRowContext(ctx, `SELECT secret_ref FROM plugin_target_configs WHERE id = $1`, created.ID).Scan(&originalRef); err != nil {
		t.Fatal(err)
	}
	accepted, err := service.TestTarget(ctx, created.ID, TargetTestInput{
		ExpectedVersion: 1,
		TriggerKind:     TriggerDownloadSubmitted,
	}, "retained-secret-delivery-key")
	if err != nil {
		t.Fatal(err)
	}

	replaced, err := service.UpdateTarget(ctx, created.ID, 1, updateTargetInput(t, "retained-secret-target", true, replaceSecretInput(t, "456:def")))
	if err != nil {
		t.Fatalf("replace target secret: %v", err)
	}
	if replaced.Version != 2 {
		t.Fatalf("expected replaced target version 2, got %d", replaced.Version)
	}

	var currentRef, deliveryRef string
	if err := db.QueryRowContext(ctx, `SELECT secret_ref FROM plugin_target_configs WHERE id = $1`, created.ID).Scan(&currentRef); err != nil {
		t.Fatal(err)
	}
	if currentRef == originalRef {
		t.Fatal("expected replace to switch the target to a new secret ref")
	}
	if err := db.QueryRowContext(ctx, `SELECT secret_ref FROM plugin_deliveries WHERE id = $1`, accepted.DeliveryID).Scan(&deliveryRef); err != nil {
		t.Fatal(err)
	}
	if deliveryRef != originalRef {
		t.Fatalf("expected frozen delivery ref %q, got %q", originalRef, deliveryRef)
	}
	assertResolvedPluginSecret(t, service.secrets, originalRef, "123:abc")
	assertResolvedPluginSecret(t, service.secrets, currentRef, "456:def")

	removed, err := service.UpdateTarget(ctx, created.ID, 2, updateTargetInput(t, "retained-secret-target", false, removeSecretInput(t)))
	if err != nil {
		t.Fatalf("remove target secret: %v", err)
	}
	if removed.Version != 3 || removed.HasSecret == nil || *removed.HasSecret {
		t.Fatalf("expected version 3 with no current secret, got %+v", removed)
	}
	var removedRef sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT secret_ref FROM plugin_target_configs WHERE id = $1`, created.ID).Scan(&removedRef); err != nil {
		t.Fatal(err)
	}
	if removedRef.Valid {
		t.Fatalf("expected target secret_ref NULL, got %q", removedRef.String)
	}
	if err := db.QueryRowContext(ctx, `SELECT secret_ref FROM plugin_deliveries WHERE id = $1`, accepted.DeliveryID).Scan(&deliveryRef); err != nil {
		t.Fatal(err)
	}
	if deliveryRef != originalRef {
		t.Fatalf("expected frozen delivery ref %q after remove, got %q", originalRef, deliveryRef)
	}
	assertResolvedPluginSecret(t, service.secrets, originalRef, "123:abc")
	assertResolvedPluginSecret(t, service.secrets, currentRef, "456:def")
}

func TestUpdateTargetSecretFailureAtomicityPostgres(t *testing.T) {
	db := openTestDB(t)
	service := newTestService(t, db)
	ctx := context.Background()

	createInput := validCreateInput(t)
	createInput.Name = "atomic-secret-target"
	createInput.Enabled = true
	createInput.Secret = secretSetInput(t)
	created, err := service.CreateTarget(ctx, createInput, "atomic-secret-create-key")
	if err != nil {
		t.Fatal(err)
	}
	blockerInput := validCreateInput(t)
	blockerInput.Name = "atomic-secret-name-conflict"
	if _, err := service.CreateTarget(ctx, blockerInput, "atomic-secret-blocker-key"); err != nil {
		t.Fatal(err)
	}

	var originalRef string
	var originalVersion int64
	if err := db.QueryRowContext(ctx, `SELECT secret_ref, version FROM plugin_target_configs WHERE id = $1`, created.ID).Scan(&originalRef, &originalVersion); err != nil {
		t.Fatal(err)
	}
	var originalSecretCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM secret_values`).Scan(&originalSecretCount); err != nil {
		t.Fatal(err)
	}

	if _, err := service.UpdateTarget(ctx, created.ID, 0, updateTargetInput(t, "atomic-secret-target", true, replaceSecretInput(t, "stale:new"))); err == nil {
		t.Fatal("expected stale CAS failure")
	}
	assertPluginTargetSecretState(t, ctx, db, service.secrets, created.ID, originalRef, originalVersion, originalSecretCount, "123:abc")

	if _, err := service.UpdateTarget(ctx, created.ID, originalVersion, updateTargetInput(t, "atomic-secret-name-conflict", true, replaceSecretInput(t, "owner-failure:new"))); err == nil {
		t.Fatal("expected owner unique-constraint failure")
	}
	assertPluginTargetSecretState(t, ctx, db, service.secrets, created.ID, originalRef, originalVersion, originalSecretCount, "123:abc")
}

func assertPluginTargetSecretState(t *testing.T, ctx context.Context, db *sql.DB, secrets SecretResolver, targetID, wantRef string, wantVersion int64, wantSecretCount int, wantToken string) {
	t.Helper()
	var ref string
	var version int64
	if err := db.QueryRowContext(ctx, `SELECT secret_ref, version FROM plugin_target_configs WHERE id = $1`, targetID).Scan(&ref, &version); err != nil {
		t.Fatal(err)
	}
	if ref != wantRef || version != wantVersion {
		t.Fatalf("target changed after failed update: ref=%q version=%d", ref, version)
	}
	var secretCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM secret_values`).Scan(&secretCount); err != nil {
		t.Fatal(err)
	}
	if secretCount != wantSecretCount {
		t.Fatalf("failed update leaked a secret version: got %d rows, want %d", secretCount, wantSecretCount)
	}
	assertResolvedPluginSecret(t, secrets, wantRef, wantToken)
}

func assertResolvedPluginSecret(t *testing.T, secrets SecretResolver, ref, wantToken string) {
	t.Helper()
	value, ok := secrets.Resolve(ref)
	if !ok {
		t.Fatalf("expected secret ref %q to resolve", ref)
	}
	if !strings.Contains(value, wantToken) {
		t.Fatalf("resolved secret ref %q did not contain expected token", ref)
	}
}

type businessDeliveryHTTPClient struct {
	statusCode int
}

func (c businessDeliveryHTTPClient) Do(req *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: c.statusCode,
		Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
		Header:     make(http.Header),
		Request:    req,
	}, nil
}

func TestBusinessDeliveryEnqueueAndDeliverCleanPostgres(t *testing.T) {
	db := openTestDB(t)
	service := newTestService(t, db)
	ctx := context.Background()

	webhookConfigValue := webhookConfig(t, "none")
	secret := SecretInput{Mode: "set", Value: json.RawMessage(`{"adapter_kind":"webhook","endpoint_url":"https://example.test/notify"}`)}
	input := validCreateInput(t)
	input.Name = "clean-business-target"
	input.AdapterKind = AdapterWebhook
	input.Config = webhookConfigValue
	input.Enabled = true
	input.Secret = secret
	created, err := service.CreateTarget(ctx, input, "clean-business-create-key")
	if err != nil {
		t.Fatal(err)
	}

	animeID := "anime-clean-business"
	seedTx, err := disposabledb.Owner(t).BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := seedTx.ExecContext(ctx, `INSERT INTO managed_anime(id, title) VALUES ($1, 'Clean Business Anime')`, animeID); err != nil {
		seedTx.Rollback()
		t.Fatal(err)
	}
	if _, err := seedTx.ExecContext(ctx, `INSERT INTO subscriptions(id, managed_anime_id, origin, instance_label, state) VALUES ('sub-clean-business', $1, 'custom', 'clean', 'active')`, animeID); err != nil {
		seedTx.Rollback()
		t.Fatal(err)
	}
	if _, err := seedTx.ExecContext(ctx, `INSERT INTO rss_sources(id, subscription_id, source_kind, locator, locator_display, group_name, title) VALUES ('source-clean-business', 'sub-clean-business', 'custom_rss', 'https://example.test/feed', 'example.test/feed', 'Custom', 'Clean Business Anime')`); err != nil {
		seedTx.Rollback()
		t.Fatal(err)
	}
	if _, err := seedTx.ExecContext(ctx, `INSERT INTO releases(id, rss_source_id, stable_item_key, raw_facts, source_facts, verdict, reason_code) VALUES ('release-clean-business', 'source-clean-business', 'clean-item', '{}', '{}', 'accepted_pending_download', 'accepted_pending_download')`); err != nil {
		seedTx.Rollback()
		t.Fatal(err)
	}
	acquisitionfixture.SeedObservedDownload(t, seedTx, "clean-business-root", "task-clean-business", "release-clean-business", "clean-result-key", `{}`, `{}`, "downloading", `null`)
	if _, err := seedTx.ExecContext(ctx, `UPDATE releases SET download_task_id = 'task-clean-business', verdict = 'submitted', reason_code = 'submitted' WHERE id = 'release-clean-business'`); err != nil {
		seedTx.Rollback()
		t.Fatal(err)
	}
	if err := seedTx.Commit(); err != nil {
		t.Fatal(err)
	}

	payload := json.RawMessage(`{"download_task_id":"task-clean-business","release_id":"release-clean-business","provider_task_id":"qb-task-1","task_name":"Clean Business Anime 01"}`)
	request := plugintargetport.BusinessDeliveryRequest{
		SourceOwnerType: plugintargetport.OwnerDownloadTask, SourceOwnerID: "task-clean-business", SourceOwnerVersion: 2,
		ManagedAnimeID: animeID, TriggerKind: plugintargetport.TriggerDownloadSubmitted, Payload: payload, ResultKey: "clean-result-key",
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Enqueue appends ONE ledger row; calling twice is idempotent (no second
	// admission). It must NOT create deliveries or work items synchronously.
	if err := service.EnqueueBusinessDelivery(ctx, txscope.Wrap(tx), request); err != nil {
		t.Fatal(err)
	}
	if err := service.EnqueueBusinessDelivery(ctx, txscope.Wrap(tx), request); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	var admissionCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM notification_admissions`).Scan(&admissionCount); err != nil {
		t.Fatal(err)
	}
	if admissionCount != 1 {
		t.Fatalf("expected one admission ledger row, got %d", admissionCount)
	}
	var deliveryBefore int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM plugin_deliveries WHERE plugin_target_config_id = $1`, created.ID).Scan(&deliveryBefore); err != nil {
		t.Fatal(err)
	}
	if deliveryBefore != 0 {
		t.Fatalf("admission must not create deliveries synchronously, got %d", deliveryBefore)
	}

	// Resolver: freeze event-time targets + create the frozen delivery.
	resolver := &AdmissionResolver{Service: service, Logger: observabilityport.NopRuntimeLogger{}, SweepInterval: time.Second}
	swept, err := resolver.SweepOnce(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if swept != 1 {
		t.Fatalf("resolver created %d deliveries, want 1", swept)
	}
	var admissionState string
	if err := db.QueryRowContext(ctx, `SELECT state FROM notification_admissions`).Scan(&admissionState); err != nil {
		t.Fatal(err)
	}
	if admissionState != AdmissionResolved {
		t.Fatalf("successful delivery creation must resolve admission, got %q", admissionState)
	}

	var deliveryID string
	var deliveryState string
	var workCount int
	if err := db.QueryRowContext(ctx, `SELECT id, state FROM plugin_deliveries WHERE plugin_target_config_id = $1`, created.ID).Scan(&deliveryID, &deliveryState); err != nil {
		t.Fatal(err)
	}
	if deliveryState != "pending" {
		t.Fatalf("expected pending delivery, got %q", deliveryState)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_item_history WHERE owner_type='plugin_delivery' AND owner_id = $1`, deliveryID).Scan(&workCount); err != nil {
		t.Fatal(err)
	}
	if workCount != 1 {
		t.Fatalf("expected one plugin work item, got %d", workCount)
	}

	store := service.workItems
	claim, err := store.ClaimDue(ctx, "clean-business-worker", []string{workitems.KindPluginDeliver}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	handler := NewPluginDeliverHandler(db, service.secrets, store, businessDeliveryHTTPClient{statusCode: http.StatusNoContent}, templateapplication.NewService())
	outcome, err := handler.Handle(ctx, claim)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Kind != runtimeworker.OutcomeFinish {
		t.Fatalf("expected finish outcome, got %+v", outcome)
	}
	if err := store.FinishWithResult(ctx, claim, outcome.TerminalState, outcome.OwnerResult, outcome.WriteOwner); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT state, attempt_count FROM plugin_deliveries WHERE id = $1`, deliveryID).Scan(&deliveryState, &workCount); err != nil {
		t.Fatal(err)
	}
	if deliveryState != "delivered" || workCount != 1 {
		t.Fatalf("expected delivered attempt 1, got state=%q attempts=%d", deliveryState, workCount)
	}
}

func TestCreateEnabledWithoutSecretRejected(t *testing.T) {
	db := openTestDB(t)
	service := newTestService(t, db)
	ctx := context.Background()

	createInput := validCreateInput(t)
	createInput.Enabled = true // secret mode=none
	_, err := service.CreateTarget(ctx, createInput, "create-idempotency-key-002")
	if err == nil {
		t.Fatal("expected enabled target without secret to be rejected")
	}
}

func containsAny(haystack string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(haystack, needle) {
			return true
		}
	}
	return false
}

// TestAdmissionResolverIdempotentSweep proves re-sweeping converges: the same
// stable key prevents duplicate deliveries/work items.
func TestAdmissionResolverIdempotentSweep(t *testing.T) {
	db := openTestDB(t)
	service := newTestService(t, db)
	ctx := context.Background()

	webhookConfigValue := webhookConfig(t, "none")
	secret := SecretInput{Mode: "set", Value: json.RawMessage(`{"adapter_kind":"webhook","endpoint_url":"https://example.test/notify"}`)}
	input := validCreateInput(t)
	input.Name = "idem-business-target"
	input.AdapterKind = AdapterWebhook
	input.Config = webhookConfigValue
	input.Enabled = true
	input.Secret = secret
	if _, err := service.CreateTarget(ctx, input, "idem-business-create-key"); err != nil {
		t.Fatal(err)
	}

	animeID := "anime-idem-business"
	if _, err := db.ExecContext(ctx, `INSERT INTO managed_anime(id, title) VALUES ($1, 'Idem Anime')`, animeID); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	payload := json.RawMessage(`{"download_task_id":"task-idem","task_name":"Idem 01"}`)
	request := plugintargetport.BusinessDeliveryRequest{
		SourceOwnerType: plugintargetport.OwnerDownloadTask, SourceOwnerID: "task-idem", SourceOwnerVersion: 1,
		ManagedAnimeID: animeID, TriggerKind: plugintargetport.TriggerDownloadSubmitted, Payload: payload, ResultKey: "idem-result-key",
	}
	if err := service.EnqueueBusinessDelivery(ctx, txscope.Wrap(tx), request); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	resolver := &AdmissionResolver{Service: service, Logger: observabilityport.NopRuntimeLogger{}, SweepInterval: time.Second}
	first, err := resolver.SweepOnce(ctx)
	if err != nil {
		t.Fatal(err)
	}
	second, err := resolver.SweepOnce(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if first != 1 || second != 0 {
		t.Fatalf("sweep deliveries = %d, %d; want 1, 0 (idempotent)", first, second)
	}
	var deliveryCount, workCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM plugin_deliveries`).Scan(&deliveryCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_item_history WHERE kind = 'plugin_deliver'`).Scan(&workCount); err != nil {
		t.Fatal(err)
	}
	if deliveryCount != 1 || workCount != 1 {
		t.Fatalf("deliveries=%d work_items=%d, want 1/1", deliveryCount, workCount)
	}
}

// TestAdmissionResolverUsesTargetConfigCurrentAtResolution proves a target that
// was edited after the admission is resolved with its new configuration: the
// delivery freezes the version the resolver read, not the one the event saw.
func TestAdmissionResolverUsesTargetConfigCurrentAtResolution(t *testing.T) {
	db := openTestDB(t)
	service := newTestService(t, db)
	ctx := context.Background()

	webhookConfigValue := webhookConfig(t, "none")
	secret := SecretInput{Mode: "set", Value: json.RawMessage(`{"adapter_kind":"webhook","endpoint_url":"https://example.test/notify"}`)}
	input := validCreateInput(t)
	input.Name = "changed-business-target"
	input.AdapterKind = AdapterWebhook
	input.Config = webhookConfigValue
	input.Enabled = true
	input.Secret = secret
	created, err := service.CreateTarget(ctx, input, "changed-business-create-key")
	if err != nil {
		t.Fatal(err)
	}

	animeID := "anime-changed-business"
	if _, err := db.ExecContext(ctx, `INSERT INTO managed_anime(id, title) VALUES ($1, 'Changed Anime')`, animeID); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	payload := json.RawMessage(`{"download_task_id":"task-changed","task_name":"Changed 01"}`)
	request := plugintargetport.BusinessDeliveryRequest{
		SourceOwnerType: plugintargetport.OwnerDownloadTask, SourceOwnerID: "task-changed", SourceOwnerVersion: 1,
		ManagedAnimeID: animeID, TriggerKind: plugintargetport.TriggerDownloadSubmitted, Payload: payload, ResultKey: "changed-result-key",
	}
	if err := service.EnqueueBusinessDelivery(ctx, txscope.Wrap(tx), request); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	// Edit the target after the admission. Resolution reads the current
	// configuration, so the delivery carries version 2.
	if _, err := db.ExecContext(ctx, `UPDATE plugin_target_configs SET version = version + 1, updated_at = clock_timestamp() WHERE id = $1`, created.ID); err != nil {
		t.Fatal(err)
	}
	var currentVersion int
	if err := db.QueryRowContext(ctx, `SELECT version FROM plugin_target_configs WHERE id = $1`, created.ID).Scan(&currentVersion); err != nil {
		t.Fatal(err)
	}
	if currentVersion != 2 {
		t.Fatalf("current target version = %d, want 2", currentVersion)
	}

	resolver := &AdmissionResolver{Service: service, Logger: observabilityport.NopRuntimeLogger{}, SweepInterval: time.Second}
	swept, err := resolver.SweepOnce(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if swept != 1 {
		t.Fatalf("sweep created %d deliveries, want 1", swept)
	}
	var deliveryCount int
	var deliveryVersion int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(MAX(plugin_target_config_version), 0) FROM plugin_deliveries`).Scan(&deliveryCount, &deliveryVersion); err != nil {
		t.Fatal(err)
	}
	if deliveryCount != 1 || deliveryVersion != 2 {
		t.Fatalf("deliveries = %d version = %d, want 1/version 2", deliveryCount, deliveryVersion)
	}
	// The resweep converges on the same row even though the stable key now
	// names version 2: the delivery identity already owns this fact snapshot.
	if again, err := resolver.SweepOnce(ctx); err != nil || again != 0 {
		t.Fatalf("resweep created=%d err=%v, want 0", again, err)
	}
}

// TestAdmissionResolverContinuesSiblingTargetsAfterFailure proves one target
// whose stored config is not canonical settles with its own failed delivery
// while a sibling still gets a pending one.
func TestAdmissionResolverContinuesSiblingTargetsAfterFailure(t *testing.T) {
	db := openTestDB(t)
	service := newTestService(t, db)
	ctx := context.Background()

	firstInput := validCreateInput(t)
	firstInput.Name = "oracle77-failing-target"
	firstInput.Enabled = true
	firstInput.Secret = secretSetInput(t)
	first, err := service.CreateTarget(ctx, firstInput, "oracle77-create-1")
	if err != nil {
		t.Fatal(err)
	}
	secondInput := validCreateInput(t)
	secondInput.Name = "oracle77-success-target"
	secondInput.Enabled = true
	secondInput.Secret = secretSetInput(t)
	second, err := service.CreateTarget(ctx, secondInput, "oracle77-create-2")
	if err != nil {
		t.Fatal(err)
	}
	animeID := "anime-oracle77"
	if _, err := db.ExecContext(ctx, `INSERT INTO managed_anime(id, title) VALUES ($1, 'Oracle 77')`, animeID); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.EnqueueBusinessDelivery(ctx, txscope.Wrap(tx), plugintargetport.BusinessDeliveryRequest{
		SourceOwnerType: plugintargetport.OwnerDownloadTask, SourceOwnerID: "task-oracle77",
		SourceOwnerVersion: 1, ManagedAnimeID: animeID, TriggerKind: plugintargetport.TriggerDownloadSubmitted,
		Payload: json.RawMessage(`{"task":"oracle77"}`), ResultKey: "oracle77-result",
	}); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var admissionID string
	if err := db.QueryRowContext(ctx, `SELECT id FROM notification_admissions WHERE result_key = 'oracle77-result'`).Scan(&admissionID); err != nil {
		t.Fatal(err)
	}
	// The failing target stores a document the adapter config allowlist cannot
	// prove canonical: every attempt fails the same way, so it settles at once.
	if _, err := db.ExecContext(ctx, `UPDATE plugin_target_configs SET config = '{"adapter_kind":"telegram","chat_id":"1","bot_token":"x"}'::jsonb WHERE id = $1`, first.ID); err != nil {
		t.Fatal(err)
	}

	resolver := &AdmissionResolver{Service: service, Logger: observabilityport.NopRuntimeLogger{}, SweepInterval: time.Second}
	created, err := resolver.SweepOnce(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if created != 1 {
		t.Fatalf("created=%d, want sibling delivery", created)
	}
	var admissionState string
	if err := db.QueryRowContext(ctx, `SELECT state FROM notification_admissions WHERE id = $1`, admissionID).Scan(&admissionState); err != nil {
		t.Fatal(err)
	}
	if admissionState != AdmissionFailed {
		t.Fatalf("admission state=%q, want %q", admissionState, AdmissionFailed)
	}
	rows, err := db.QueryContext(ctx, `SELECT plugin_target_config_id, state, COALESCE(last_error_code, ''), config_status FROM plugin_deliveries ORDER BY plugin_target_config_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seenFailure, seenSuccess := false, false
	for rows.Next() {
		var targetID, state, code, configStatus string
		if err := rows.Scan(&targetID, &state, &code, &configStatus); err != nil {
			t.Fatal(err)
		}
		switch targetID {
		case first.ID:
			seenFailure = state == "failed" && code == codeTargetConfigInvalid && configStatus == "invalid"
		case second.ID:
			seenSuccess = state == "pending" && code == ""
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !seenFailure || !seenSuccess {
		t.Fatalf("delivery evidence missing: failure=%t success=%t", seenFailure, seenSuccess)
	}
	again, err := resolver.SweepOnce(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if again != 0 {
		t.Fatalf("idempotent resweep created=%d, want 0", again)
	}
}

func TestListDeliveriesFiltersAndNeverLeaksSecrets(t *testing.T) {
	db := openTestDB(t)
	service := newTestService(t, db)
	ctx := context.Background()

	createInput := validCreateInput(t)
	createInput.Enabled = true
	createInput.Secret = secretSetInput(t)
	created, err := service.CreateTarget(ctx, createInput, "create-idempotency-key-101")
	if err != nil {
		t.Fatalf("create target: %v", err)
	}

	// Two test deliveries with distinct trigger kinds.
	first, err := service.TestTarget(ctx, created.ID, TargetTestInput{TriggerKind: "download_submitted", ExpectedVersion: 1}, "test-key-101")
	if err != nil {
		t.Fatalf("test delivery 1: %v", err)
	}
	second, err := service.TestTarget(ctx, created.ID, TargetTestInput{TriggerKind: "file_operation_completed", ExpectedVersion: 1}, "test-key-102")
	if err != nil {
		t.Fatalf("test delivery 2: %v", err)
	}

	list, err := service.ListDeliveries(ctx, DeliveryQuery{})
	if err != nil {
		t.Fatalf("list deliveries: %v", err)
	}
	if list.Total != 2 || len(list.Items) != 2 {
		t.Fatalf("expected 2 deliveries, got total=%d items=%d", list.Total, len(list.Items))
	}
	for _, item := range list.Items {
		itemID := deliveryField(t, item, "id")
		if itemID != first.DeliveryID && itemID != second.DeliveryID {
			t.Fatalf("unexpected delivery id %q", itemID)
		}
		raw, err := json.Marshal(item)
		if err != nil {
			t.Fatal(err)
		}
		if containsAny(string(raw), "secret_ref", "bot_token", "123:abc") {
			t.Fatalf("delivery list leaked secret material: %s", raw)
		}
	}

	// Filter by trigger kind.
	onlySubmitted, err := service.ListDeliveries(ctx, DeliveryQuery{TriggerKind: ptrToKind("download_submitted")})
	if err != nil {
		t.Fatalf("filtered list: %v", err)
	}
	if onlySubmitted.Total != 1 || deliveryField(t, onlySubmitted.Items[0], "id") != first.DeliveryID {
		t.Fatalf("trigger filter wrong: %+v", onlySubmitted)
	}

	// Limit + offset.
	paged, err := service.ListDeliveries(ctx, DeliveryQuery{Limit: ptrToInt(1), Offset: ptrToInt(0)})
	if err != nil {
		t.Fatalf("paged list: %v", err)
	}
	if len(paged.Items) != 1 || paged.Total != 2 {
		t.Fatalf("paging wrong: %+v", paged)
	}

	// Detail resolves the delivery (attempts are produced by the async
	// worker lane, which is not running in this service-level test).
	detail, err := service.GetDelivery(ctx, first.DeliveryID)
	if err != nil {
		t.Fatalf("delivery detail: %v", err)
	}
	detailID := deliveryField(t, detail.Delivery, "id")
	state := deliveryField(t, detail.Delivery, "state")
	if detailID != first.DeliveryID || state == "" {
		t.Fatalf("detail incomplete: %+v", detail.Delivery)
	}
}

func ptrToKind(value string) *TriggerKind {
	kind := TriggerKind(value)
	return &kind
}

func ptrToInt(value int) *int { return &value }

func deliveryField(t *testing.T, delivery Delivery, field string) string {
	t.Helper()
	var values map[string]any
	if err := json.Unmarshal(delivery.Raw, &values); err != nil {
		t.Fatalf("decode delivery field %s: %v", field, err)
	}
	value, ok := values[field].(string)
	if !ok {
		t.Fatalf("delivery field %s missing or not string: %s", field, delivery.Raw)
	}
	return value
}

func TestDeleteTargetSoftDeletesAndPreservesDeliveryEvidence(t *testing.T) {
	db := openTestDB(t)
	service := newTestService(t, db)
	ctx := context.Background()
	input := validCreateInput(t)
	input.Name = "delete-me"
	input.Enabled = true
	input.Secret = secretSetInput(t)
	target, err := service.CreateTarget(ctx, input, "delete-key")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	accepted, err := service.TestTarget(ctx, target.ID, TargetTestInput{
		ExpectedVersion: target.Version,
		TriggerKind:     TriggerDownloadSubmitted,
	}, "delete-delivery-key")
	if err != nil {
		t.Fatalf("create test delivery: %v", err)
	}
	if accepted.DeliveryID == "" {
		t.Fatal("expected test delivery evidence before deleting target")
	}
	if err := service.DeleteTarget(ctx, target.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	list, err := service.ListTargets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Targets) != 0 {
		t.Fatalf("soft-deleted target must not list, got %d", len(list.Targets))
	}
	var deletedAt sql.NullTime
	if err := db.QueryRowContext(ctx, `SELECT deleted_at FROM plugin_target_configs WHERE id = $1`, target.ID).Scan(&deletedAt); err != nil {
		t.Fatalf("read deleted_at: %v", err)
	}
	if !deletedAt.Valid {
		t.Fatal("soft delete must record deleted_at")
	}
	var deliveryCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM plugin_deliveries WHERE id = $1`, accepted.DeliveryID).Scan(&deliveryCount); err != nil {
		t.Fatalf("read delivery evidence: %v", err)
	}
	if deliveryCount != 1 {
		t.Fatalf("soft delete must preserve delivery evidence, got %d rows", deliveryCount)
	}
	if _, err := service.GetDelivery(ctx, accepted.DeliveryID); err != nil {
		t.Fatalf("preserved delivery must remain readable: %v", err)
	}
	if err := service.DeleteTarget(ctx, target.ID); err != nil {
		t.Fatalf("second delete: %v", err)
	}
	if err := service.DeleteTarget(ctx, "no-such-target"); err == nil {
		t.Fatal("expected not found for unknown target")
	}
}

// TestBusinessDeliveryRejectsSameIdentityWithDifferentFrozenPayload proves the
// delivery row owns exactly one frozen payload: replaying the same delivery
// identity with the same facts converges, while the same identity with a
// different fact snapshot is a typed conflict instead of a silent second row or
// a rewritten payload.
func TestBusinessDeliveryRejectsSameIdentityWithDifferentFrozenPayload(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	service := newTestService(t, db)

	created, err := service.CreateTarget(ctx, TargetInput{
		AdapterKind:         AdapterTelegram,
		Config:              telegramConfig(t, "12345"),
		ConfigSchemaVersion: 1,
		Enabled:             true,
		Name:                "Frozen Payload Target",
		PluginKind:          PluginKindNotification,
		Secret:              secretSetInput(t),
		TemplateSet:         defaultTemplateSet(t),
		TriggerKinds:        []TriggerKind{TriggerDownloadSubmitted},
	}, "frozen-payload-target-key")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO managed_anime(id, title) VALUES ('anime-frozen-payload', 'Frozen Payload Anime')`); err != nil {
		t.Fatal(err)
	}

	enqueue := func(payload json.RawMessage, key string) error {
		t.Helper()
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		target, err := service.lockTargetTx(ctx, tx, created.ID)
		if err != nil {
			t.Fatal(err)
		}
		request := plugintargetport.BusinessDeliveryRequest{
			SourceOwnerType: plugintargetport.OwnerDownloadTask, SourceOwnerID: "task-frozen-payload",
			SourceOwnerVersion: 1, ManagedAnimeID: "anime-frozen-payload",
			TriggerKind: plugintargetport.TriggerDownloadSubmitted, Payload: payload, ResultKey: "frozen-payload-result",
		}
		if err := service.enqueueForTargetWithKey(ctx, tx, target, request, key); err != nil {
			return err
		}
		return tx.Commit()
	}

	first := json.RawMessage(`{"download_task_id":"task-frozen-payload","anime.title":"Frozen Payload Anime"}`)
	if err := enqueue(first, "frozen-payload-key-1"); err != nil {
		t.Fatalf("first admission: %v", err)
	}

	var storedPayload, storedDigest string
	if err := db.QueryRowContext(ctx, `
SELECT payload::text, payload_digest FROM plugin_deliveries
WHERE plugin_target_config_id = $1 AND source_owner_id = 'task-frozen-payload'`, created.ID).Scan(&storedPayload, &storedDigest); err != nil {
		t.Fatal(err)
	}
	expectedPayload, err := deliverypayload.Encode(string(plugintargetport.TriggerDownloadSubmitted), map[string]any{
		"download_task_id": "task-frozen-payload", "anime.title": "Frozen Payload Anime",
	})
	if err != nil {
		t.Fatal(err)
	}
	if storedDigest != hashBytes(expectedPayload) {
		t.Fatalf("payload_digest = %q, want the digest of the canonical payload", storedDigest)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(storedPayload), &decoded); err != nil {
		t.Fatalf("stored payload must be a JSON object: %v (%s)", err, storedPayload)
	}
	if decoded["schema_version"] != float64(deliverypayload.SchemaVersion) || decoded["trigger_kind"] != string(plugintargetport.TriggerDownloadSubmitted) {
		t.Fatalf("stored payload is not the canonical delivery payload: %s", storedPayload)
	}
	if _, ok := decoded["facts"].(map[string]any); !ok {
		t.Fatalf("stored payload must carry a facts object: %s", storedPayload)
	}
	if _, ok := decoded["expected_version"]; ok {
		t.Fatalf("request identity must not leak into the delivery payload: %s", storedPayload)
	}

	// Same identity, same facts: the replay converges on the existing row.
	if err := enqueue(first, "frozen-payload-key-2"); err != nil {
		t.Fatalf("identical replay must converge, got %v", err)
	}
	var deliveryCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM plugin_deliveries WHERE plugin_target_config_id = $1`, created.ID).Scan(&deliveryCount); err != nil {
		t.Fatal(err)
	}
	if deliveryCount != 1 {
		t.Fatalf("identical replay created %d deliveries, want 1", deliveryCount)
	}

	// Same identity, different facts: typed conflict, and the frozen payload of
	// the existing row is left untouched.
	changed := json.RawMessage(`{"download_task_id":"task-frozen-payload","anime.title":"Different Title"}`)
	err = enqueue(changed, "frozen-payload-key-3")
	var conflict *apperrors.Business
	if !errors.As(err, &conflict) {
		t.Fatalf("expected a typed conflict, got %T: %v", err, err)
	}
	if conflict.Status != http.StatusConflict || conflict.Code != "DELIVERY_PAYLOAD_CONFLICT" {
		t.Fatalf("conflict = %d/%s, want 409/DELIVERY_PAYLOAD_CONFLICT", conflict.Status, conflict.Code)
	}
	var afterPayload, afterDigest string
	if err := db.QueryRowContext(ctx, `
SELECT payload::text, payload_digest FROM plugin_deliveries
WHERE plugin_target_config_id = $1 AND source_owner_id = 'task-frozen-payload'`, created.ID).Scan(&afterPayload, &afterDigest); err != nil {
		t.Fatal(err)
	}
	if afterPayload != storedPayload || afterDigest != storedDigest {
		t.Fatalf("a conflicting replay rewrote the frozen payload: %s / %s", afterPayload, afterDigest)
	}
}
