package application

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	observabilityapplication "bansoko/internal/observability/application"
	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/persistence/txscope"
	plugintargetport "bansoko/internal/plugintarget/port"
	runtimeworker "bansoko/internal/runtime/worker"
	templateapplication "bansoko/internal/templatecatalog/application"
	"bansoko/internal/testutil/disposabledb"
	"bansoko/internal/workitems"
)

// NF2 delivery: a business delivery is at-least-once. An unknown outcome or a
// retryable status retries on the bounded backoff and then settles visibly;
// every attempt carries the same delivery_id; a definite failure never
// retries; the retry state lives in the database and survives a restart.

const workflowSigningSecret = "whsec_workflow_probe_0123456789"

// webhookCaptureClient records every webhook attempt and answers with a fixed
// status or transport error.
type webhookCaptureClient struct {
	mu       sync.Mutex
	status   int
	body     string
	err      error
	requests []capturedWebhook
}

type capturedWebhook struct {
	header http.Header
	body   []byte
}

func (c *webhookCaptureClient) Do(req *http.Request) (*http.Response, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	body, _ := io.ReadAll(req.Body)
	c.requests = append(c.requests, capturedWebhook{header: req.Header.Clone(), body: body})
	if c.err != nil {
		return nil, c.err
	}
	text := c.body
	if text == "" {
		text = `{"ok":true}`
	}
	return &http.Response{StatusCode: c.status, Body: io.NopCloser(strings.NewReader(text)), Header: make(http.Header), Request: req}, nil
}

// seedWorkflowWebhookDelivery creates a webhook target subscribed to
// subscription_changed, admits one retirement notice and resolves it into a
// pending business delivery.
func seedWorkflowWebhookDelivery(t *testing.T, secret string) (*Service, string) {
	t.Helper()
	db := openTestDB(t)
	service := newTestService(t, db)
	ctx := context.Background()
	input := validCreateInput(t)
	input.Name = "workflow-webhook"
	input.AdapterKind = AdapterWebhook
	input.Config = webhookConfig(t, "none")
	input.Enabled = true
	input.TriggerKinds = []TriggerKind{TriggerSubscriptionChanged}
	input.Secret = SecretInput{Mode: "set", Value: json.RawMessage(secret)}
	if _, err := service.CreateTarget(ctx, input, "workflow-webhook-create-key"); err != nil {
		t.Fatal(err)
	}
	request, err := plugintargetport.SubscriptionChangedRequest("sub-workflow", 2, "anime-workflow", "Workflow Show", plugintargetport.SubscriptionRetired)
	if err != nil {
		t.Fatal(err)
	}
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
	resolver := &AdmissionResolver{Service: service, Logger: observabilityport.NopRuntimeLogger{}, SweepInterval: time.Second}
	if swept, err := resolver.SweepOnce(ctx); err != nil || swept != 1 {
		t.Fatalf("resolver swept=%d err=%v, want one delivery", swept, err)
	}
	var id string
	if err := db.QueryRowContext(ctx, `SELECT id FROM plugin_deliveries WHERE delivery_kind='business' AND trigger_kind='subscription_changed'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return service, id
}

func workflowWebhookSecret(signing bool) string {
	if signing {
		return `{"adapter_kind":"webhook","endpoint_url":"https://receiver.example.test/hook","signing_secret":"` + workflowSigningSecret + `"}`
	}
	return `{"adapter_kind":"webhook","endpoint_url":"https://receiver.example.test/hook"}`
}

func claimWorkflowDelivery(t *testing.T, service *Service, worker string) workitems.Claim {
	t.Helper()
	claim, err := service.workItems.ClaimDue(context.Background(), worker, []string{workitems.KindPluginDeliver}, time.Minute)
	if err != nil {
		t.Fatalf("claim delivery: %v", err)
	}
	return claim
}

// makeWorkflowDeliveryDue moves the scheduled retry to now, standing in for
// the passage of the backoff.
func makeWorkflowDeliveryDue(t *testing.T, deliveryID string) {
	t.Helper()
	if _, err := disposabledb.Owner(t).ExecContext(context.Background(), `UPDATE work_items SET available_at=CURRENT_TIMESTAMP-INTERVAL '1 second' WHERE owner_type='plugin_delivery' AND owner_id=$1 AND state='available'`, deliveryID); err != nil {
		t.Fatal(err)
	}
}

type workflowDeliveryRow struct {
	state       string
	attempts    int
	nextAttempt bool
	retryInSecs float64
	lastCode    string
}

func readWorkflowDelivery(t *testing.T, service *Service, deliveryID string) workflowDeliveryRow {
	t.Helper()
	var row workflowDeliveryRow
	if err := service.db.QueryRowContext(context.Background(), `
SELECT state, attempt_count, next_attempt_at IS NOT NULL,
       COALESCE(EXTRACT(EPOCH FROM next_attempt_at-CURRENT_TIMESTAMP),0)::float8, COALESCE(last_error_code,'')
FROM plugin_deliveries WHERE id=$1`, deliveryID).Scan(&row.state, &row.attempts, &row.nextAttempt, &row.retryInSecs, &row.lastCode); err != nil {
		t.Fatal(err)
	}
	return row
}

func webhookDocument(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("webhook body is not JSON: %v", err)
	}
	return doc
}

func TestBusinessDeliveryRetriesOnBackoffToVisibleTerminal(t *testing.T) {
	for _, tc := range []struct {
		name     string
		client   *webhookCaptureClient
		terminal string
	}{
		{name: "unknown outcome stays unknown", client: &webhookCaptureClient{err: errors.New("connection reset by peer")}, terminal: "unknown"},
		{name: "retryable status ends failed", client: &webhookCaptureClient{status: http.StatusServiceUnavailable}, terminal: "failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service, id := seedWorkflowWebhookDelivery(t, workflowWebhookSecret(true))
			handler := NewPluginDeliverHandler(service.db, service.secrets, service.workItems, tc.client, templateapplication.NewService())
			for attempt := 1; attempt <= MaxDeliveryAttempts; attempt++ {
				claim := claimWorkflowDelivery(t, service, "workflow-retry-worker")
				if claim.Owner.ID != id {
					t.Fatalf("claimed %s, want delivery %s", claim.Owner.ID, id)
				}
				out, err := handler.Handle(context.Background(), claim)
				if err != nil {
					t.Fatal(err)
				}
				if attempt < MaxDeliveryAttempts {
					want := deliveryRetryDelays[attempt-1]
					if out.Kind != runtimeworker.OutcomeRetry || out.RetryAfter != want {
						t.Fatalf("attempt %d outcome=%+v, want retry after %s", attempt, out, want)
					}
					finishHandlerOutcome(t, service, claim, out)
					row := readWorkflowDelivery(t, service, id)
					if row.state != "waiting_retry" || row.attempts != attempt || !row.nextAttempt {
						t.Fatalf("attempt %d delivery=%+v, want waiting_retry with a next attempt", attempt, row)
					}
					if row.retryInSecs < want.Seconds()-30 || row.retryInSecs > want.Seconds()+30 {
						t.Fatalf("attempt %d next attempt in %.0fs, want about %s", attempt, row.retryInSecs, want)
					}
					// Not due before the backoff passes.
					if _, err := service.workItems.ClaimDue(context.Background(), "early-worker", []string{workitems.KindPluginDeliver}, time.Minute); !errors.Is(err, workitems.ErrNoDueWork) {
						t.Fatalf("attempt %d: delivery was claimable before its backoff: %v", attempt, err)
					}
					makeWorkflowDeliveryDue(t, id)
					continue
				}
				if out.Kind != runtimeworker.OutcomeFinish {
					t.Fatalf("final attempt outcome=%+v, want finish", out)
				}
				finishHandlerOutcome(t, service, claim, out)
			}
			row := readWorkflowDelivery(t, service, id)
			if row.state != tc.terminal || row.attempts != MaxDeliveryAttempts || row.nextAttempt || row.lastCode == "" {
				t.Fatalf("terminal delivery=%+v, want %s after %d attempts with a last error", row, tc.terminal, MaxDeliveryAttempts)
			}
			var workState string
			if err := service.db.QueryRowContext(context.Background(), `SELECT state FROM work_item_history WHERE owner_type='plugin_delivery' AND owner_id=$1`, id).Scan(&workState); err != nil {
				t.Fatal(err)
			}
			if workState != workitems.StateFailed {
				t.Fatalf("work item state=%s, want failed", workState)
			}
			if _, err := service.workItems.ClaimDue(context.Background(), "after-terminal", []string{workitems.KindPluginDeliver}, time.Minute); !errors.Is(err, workitems.ErrNoDueWork) {
				t.Fatalf("a terminal delivery must not be claimed again: %v", err)
			}
			var attemptRows int
			if err := service.db.QueryRowContext(context.Background(), `SELECT COUNT(DISTINCT attempt_number) FROM plugin_delivery_attempts WHERE plugin_delivery_id=$1`, id).Scan(&attemptRows); err != nil {
				t.Fatal(err)
			}
			if attemptRows != MaxDeliveryAttempts {
				t.Fatalf("attempt rows=%d, want %d", attemptRows, MaxDeliveryAttempts)
			}
			// Every attempt carries the same delivery_id, in the header and the
			// body; the attempt number counts up.
			if len(tc.client.requests) != MaxDeliveryAttempts {
				t.Fatalf("requests=%d, want %d", len(tc.client.requests), MaxDeliveryAttempts)
			}
			for index, request := range tc.client.requests {
				doc := webhookDocument(t, request.body)
				if request.header.Get(HeaderWebhookID) != id || doc["delivery_id"] != id {
					t.Fatalf("attempt %d delivery_id header=%q body=%v, want %s", index+1, request.header.Get(HeaderWebhookID), doc["delivery_id"], id)
				}
				if doc["attempt"] != float64(index+1) || doc["version"] != WebhookEnvelopeVersion || doc["event"] != "subscription_changed" {
					t.Fatalf("attempt %d document=%v", index+1, doc)
				}
				if request.header.Get(HeaderWebhookSignature) == "" {
					t.Fatalf("attempt %d was not signed", index+1)
				}
			}
		})
	}
}

func TestBusinessDeliveryDefiniteFailureDoesNotRetry(t *testing.T) {
	service, id := seedWorkflowWebhookDelivery(t, workflowWebhookSecret(false))
	client := &webhookCaptureClient{status: http.StatusBadRequest}
	handler := NewPluginDeliverHandler(service.db, service.secrets, service.workItems, client, templateapplication.NewService())
	claim := claimWorkflowDelivery(t, service, "workflow-4xx-worker")
	out, err := handler.Handle(context.Background(), claim)
	if err != nil {
		t.Fatal(err)
	}
	if out.Kind != runtimeworker.OutcomeFinish {
		t.Fatalf("4xx outcome=%+v, want finish", out)
	}
	finishHandlerOutcome(t, service, claim, out)
	row := readWorkflowDelivery(t, service, id)
	if row.state != "failed" || row.attempts != 1 || row.nextAttempt {
		t.Fatalf("delivery=%+v, want failed after one attempt", row)
	}
	makeWorkflowDeliveryDue(t, id)
	if _, err := service.workItems.ClaimDue(context.Background(), "after-4xx", []string{workitems.KindPluginDeliver}, time.Minute); !errors.Is(err, workitems.ErrNoDueWork) {
		t.Fatalf("a definite failure must not be retried: %v", err)
	}
	if len(client.requests) != 1 || client.requests[0].header.Get(HeaderWebhookSignature) != "" {
		t.Fatalf("requests=%d; an unsigned target must not send a signature", len(client.requests))
	}
}

// A crash after the request started leaves the attempt with request-start
// evidence. After the lease expires, the reclaim settles that attempt unknown
// without sending again and schedules the next attempt from the database; the
// next attempt sends the same delivery_id.
func TestBusinessDeliveryRetryStateSurvivesRestart(t *testing.T) {
	service, id := seedWorkflowWebhookDelivery(t, workflowWebhookSecret(true))
	client := &webhookCaptureClient{status: http.StatusNoContent}
	crashed := NewPluginDeliverHandler(service.db, service.secrets, service.workItems, client, templateapplication.NewService())
	crashed.hooks.afterRequestStartCommit = func() error { return errors.New("process stopped") }
	claim := claimWorkflowDelivery(t, service, "workflow-crash-worker")
	if _, err := crashed.Handle(context.Background(), claim); err == nil {
		t.Fatal("expected the injected crash")
	}

	// A new process: fresh handler, lease expired, reclaim from the database.
	restarted := NewPluginDeliverHandler(service.db, service.secrets, service.workItems, client, templateapplication.NewService())
	reclaim := expireAndReclaimDelivery(t, service, id)
	out, err := restarted.Handle(context.Background(), reclaim)
	if err != nil {
		t.Fatal(err)
	}
	if out.Kind != runtimeworker.OutcomeRetry || out.RetryAfter != deliveryRetryDelays[0] {
		t.Fatalf("reclaimed outcome=%+v, want retry on the first backoff", out)
	}
	if len(client.requests) != 0 {
		t.Fatal("the reclaim must not resend the started request")
	}
	finishHandlerOutcome(t, service, reclaim, out)
	row := readWorkflowDelivery(t, service, id)
	if row.state != "waiting_retry" || row.attempts != 1 || !row.nextAttempt || row.lastCode != "request_outcome_unknown" {
		t.Fatalf("delivery after reclaim=%+v", row)
	}

	makeWorkflowDeliveryDue(t, id)
	next := claimWorkflowDelivery(t, service, "workflow-next-worker")
	out, err = restarted.Handle(context.Background(), next)
	if err != nil {
		t.Fatal(err)
	}
	if out.Kind != runtimeworker.OutcomeFinish {
		t.Fatalf("second attempt outcome=%+v, want finish", out)
	}
	finishHandlerOutcome(t, service, next, out)
	row = readWorkflowDelivery(t, service, id)
	if row.state != "delivered" || row.attempts != 2 || row.nextAttempt {
		t.Fatalf("delivery=%+v, want delivered on attempt 2", row)
	}
	if len(client.requests) != 1 || client.requests[0].header.Get(HeaderWebhookID) != id {
		t.Fatalf("requests=%d, want one request carrying delivery_id %s", len(client.requests), id)
	}
	if doc := webhookDocument(t, client.requests[0].body); doc["attempt"] != float64(2) || doc["delivery_id"] != id {
		t.Fatalf("second attempt document=%v", doc)
	}
}

// capturingRuntimeLogger and capturingRecorder collect everything the
// handler logs or records as a diagnostic, as text.
type capturingRuntimeLogger struct {
	mu    sync.Mutex
	lines []string
}

func (l *capturingRuntimeLogger) record(event observabilityport.RuntimeEvent) {
	l.mu.Lock()
	defer l.mu.Unlock()
	raw, _ := json.Marshal(event)
	l.lines = append(l.lines, string(raw))
}
func (l *capturingRuntimeLogger) Debug(_ context.Context, e observabilityport.RuntimeEvent) {
	l.record(e)
}
func (l *capturingRuntimeLogger) Info(_ context.Context, e observabilityport.RuntimeEvent) {
	l.record(e)
}
func (l *capturingRuntimeLogger) Warn(_ context.Context, e observabilityport.RuntimeEvent) {
	l.record(e)
}
func (l *capturingRuntimeLogger) Error(_ context.Context, e observabilityport.RuntimeEvent) {
	l.record(e)
}

type capturingRecorder struct {
	observabilityport.Recorder
	mu    sync.Mutex
	lines []string
}

func (r *capturingRecorder) note(value any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	raw, _ := json.Marshal(value)
	r.lines = append(r.lines, string(raw))
}
func (r *capturingRecorder) RecordDiagnosticFailure(ctx context.Context, req observabilityport.DiagnosticFailureRequest) error {
	r.note(req)
	return r.Recorder.RecordDiagnosticFailure(ctx, req)
}
func (r *capturingRecorder) RecordDiagnosticSuccess(ctx context.Context, req observabilityport.DiagnosticSuccessRequest) error {
	r.note(req)
	return r.Recorder.RecordDiagnosticSuccess(ctx, req)
}
func (r *capturingRecorder) RecordDiagnosticFailureTx(ctx context.Context, tx txscope.Tx, req observabilityport.DiagnosticFailureRequest) error {
	r.note(req)
	return r.Recorder.RecordDiagnosticFailureTx(ctx, tx, req)
}

// The signing secret is write-only: reads, delivery views, attempt evidence,
// work item errors, diagnostics and logs never carry it, even when the
// receiver or the transport echoes it back.
func TestWebhookSigningSecretNeverLeaks(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name   string
		client *webhookCaptureClient
	}{
		{name: "receiver echoes the secret", client: &webhookCaptureClient{status: http.StatusBadRequest, body: `{"error":"bad signature for ` + workflowSigningSecret + `"}`}},
		{name: "transport error carries the secret", client: &webhookCaptureClient{err: errors.New("dial failed: " + workflowSigningSecret)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service, id := seedWorkflowWebhookDelivery(t, workflowWebhookSecret(true))
			writer, err := observabilityapplication.NewService(service.db)
			if err != nil {
				t.Fatal(err)
			}
			recorder := &capturingRecorder{Recorder: writer}
			logger := &capturingRuntimeLogger{}
			handler := NewPluginDeliverHandler(service.db, service.secrets, service.workItems, tc.client, templateapplication.NewService())
			handler.SetDiagnosticRecorder(recorder, logger)
			claim := claimWorkflowDelivery(t, service, "workflow-leak-worker")
			out, err := handler.Handle(ctx, claim)
			if err != nil {
				t.Fatal(err)
			}
			finishHandlerOutcome(t, service, claim, out)
			if out.AfterCommit != nil {
				out.AfterCommit(ctx)
			}

			if len(tc.client.requests) != 1 || strings.Contains(string(tc.client.requests[0].body), workflowSigningSecret) {
				t.Fatal("the request body carried the signing secret")
			}
			page, err := service.ListTargets(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if len(page.Targets) != 1 || page.Targets[0].HasSigningSecret == nil || !*page.Targets[0].HasSigningSecret {
				t.Fatalf("target read must report has_signing_secret: %+v", page.Targets)
			}
			deliveries, err := service.ListDeliveries(ctx, DeliveryQuery{})
			if err != nil {
				t.Fatal(err)
			}
			detail, err := service.GetDelivery(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			exposed := []any{page, deliveries, detail}
			for _, value := range exposed {
				raw, err := json.Marshal(value)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(raw), workflowSigningSecret) || strings.Contains(string(raw), "signing_secret\":\"") {
					t.Fatalf("a read leaked the signing secret: %s", raw)
				}
			}
			for _, query := range []string{
				`SELECT row_to_json(d)::text FROM plugin_deliveries d`,
				`SELECT row_to_json(a)::text FROM plugin_delivery_attempts a`,
				`SELECT row_to_json(w)::text FROM work_item_history w`,
				`SELECT row_to_json(e)::text FROM diagnostic_episodes e`,
				`SELECT row_to_json(r)::text FROM routine_activities r`,
				`SELECT row_to_json(c)::text FROM plugin_target_configs c`,
			} {
				rows, err := disposabledb.Owner(t).QueryContext(ctx, query)
				if err != nil {
					t.Fatal(err)
				}
				for rows.Next() {
					var text string
					if err := rows.Scan(&text); err != nil {
						t.Fatal(err)
					}
					if strings.Contains(text, workflowSigningSecret) {
						rows.Close()
						t.Fatalf("%s leaked the signing secret: %s", query, text)
					}
				}
				rows.Close()
			}
			for _, line := range append(append([]string{}, logger.lines...), recorder.lines...) {
				if strings.Contains(line, workflowSigningSecret) {
					t.Fatalf("a log or diagnostic carried the signing secret: %s", line)
				}
			}
		})
	}
}

// The signing secret follows the write-only bundle rules: keep leaves it,
// replace sets or drops it, and only webhook targets report it.
func TestWebhookSigningSecretKeepReplaceRemove(t *testing.T) {
	db := openTestDB(t)
	service := newTestService(t, db)
	ctx := context.Background()
	input := validCreateInput(t)
	input.Name = "signing-target"
	input.AdapterKind = AdapterWebhook
	input.Config = webhookConfig(t, "none")
	input.Enabled = true
	input.Secret = SecretInput{Mode: "set", Value: json.RawMessage(workflowWebhookSecret(false))}
	created, err := service.CreateTarget(ctx, input, "signing-create-key")
	if err != nil {
		t.Fatal(err)
	}
	signed := func(target TargetConfig) bool {
		return target.HasSigningSecret != nil && *target.HasSigningSecret
	}
	if created.HasSigningSecret == nil || signed(created) {
		t.Fatalf("created without a signing secret: %+v", created.HasSigningSecret)
	}
	update := func(version int64, secret SecretInput) TargetConfig {
		t.Helper()
		next := TargetUpdateInput{TargetInput: input}
		next.Secret = secret
		target, err := service.UpdateTarget(ctx, created.ID, version, next)
		if err != nil {
			t.Fatal(err)
		}
		return target
	}
	withSigning := update(created.Version, SecretInput{Mode: "replace", Value: json.RawMessage(workflowWebhookSecret(true))})
	if !signed(withSigning) {
		t.Fatal("replace with a signing secret must report it")
	}
	kept := update(withSigning.Version, SecretInput{Mode: "keep"})
	if !signed(kept) {
		t.Fatal("keep must keep the signing secret")
	}
	dropped := update(kept.Version, SecretInput{Mode: "replace", Value: json.RawMessage(workflowWebhookSecret(false))})
	if signed(dropped) {
		t.Fatal("replace without a signing secret must drop it")
	}
	short := TargetUpdateInput{TargetInput: input}
	short.Secret = SecretInput{Mode: "replace", Value: json.RawMessage(`{"adapter_kind":"webhook","endpoint_url":"https://receiver.example.test/hook","signing_secret":"short"}`)}
	if _, err := service.UpdateTarget(ctx, created.ID, dropped.Version, short); err == nil {
		t.Fatal("a signing secret under 16 characters must be rejected")
	}

	telegram := validCreateInput(t)
	telegram.Enabled = true
	telegram.Secret = secretSetInput(t)
	other, err := service.CreateTarget(ctx, telegram, "signing-telegram-key")
	if err != nil {
		t.Fatal(err)
	}
	if other.HasSigningSecret != nil {
		t.Fatal("has_signing_secret is reported for webhook targets only")
	}
}
