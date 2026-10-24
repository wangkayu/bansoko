package application

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/persistence/txscope"
	adapterconfig "bansoko/internal/plugintarget/config"
	deliverypayload "bansoko/internal/plugintarget/payload"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
)

const (
	handlerRequestTimeout = 30 * time.Second
	maxResponseBody       = 64 * 1024
	// testDeliveryRetryDelay spaces the claim-budgeted retries of a test
	// delivery that met a retryable HTTP status. A test delivery never resends
	// after an unknown outcome: the operator sees it and can test again.
	testDeliveryRetryDelay = 2 * time.Minute
	// deliveryWorkItemMaxAttempts is the claim budget of a business delivery's
	// WorkItem: every delivery attempt plus slack for claims lost to a crash
	// before an attempt was recorded. The delivery's own attempt count, not
	// this budget, decides when automatic retries stop.
	deliveryWorkItemMaxAttempts = 10
)

// deliveryRetryDelays is the bounded backoff of a business delivery whose
// outcome is unknown (timeout, connection lost before a response, a crash
// after the request started) or retryable (429, 5xx). Entry n is the wait
// after attempt n+1; once it is spent the delivery settles visibly. Every
// attempt carries the same delivery_id, so a receiver that saw an earlier
// attempt can drop the repeat.
var deliveryRetryDelays = []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute, time.Hour, time.Hour}

// MaxDeliveryAttempts is the number of attempts one business delivery makes
// at most: the first send plus one per backoff step.
var MaxDeliveryAttempts = len(deliveryRetryDelays) + 1

// deliveryRetryDelay returns the wait before the attempt after attemptNumber,
// or false when no automatic attempt is left.
func deliveryRetryDelay(attemptNumber int) (time.Duration, bool) {
	if attemptNumber < 1 || attemptNumber > len(deliveryRetryDelays) {
		return 0, false
	}
	return deliveryRetryDelays[attemptNumber-1], true
}

type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}
type TemplateRenderer interface {
	// RenderNotification renders a test delivery's frozen title/body for its
	// trigger against the sample facts of its frozen catalog version.
	RenderNotification(title, body, trigger, catalogVersion string) (string, string, error)
}

type deliveryHooks struct {
	beforeAttemptWrite       func() error
	beforeAttemptCommit      func() error
	afterAttemptCommit       func() error
	beforeRequestStartWrite  func() error
	afterRequestStartCommit  func() error
	afterResponseBeforeFinal func() error
}

type PluginDeliverHandler struct {
	db            *sql.DB
	secrets       SecretResolver
	workItems     *workitems.Store
	client        HTTPDoer
	renderer      TemplateRenderer
	observability observabilityport.Recorder
	logger        observabilityport.RuntimeLogger
	hooks         deliveryHooks
	// now is the attempt clock of the webhook timestamp; nil means time.Now.
	now func() time.Time
}

func NewPluginDeliverHandler(db *sql.DB, secrets SecretResolver, workItems *workitems.Store, client HTTPDoer, renderer TemplateRenderer) *PluginDeliverHandler {
	if client == nil {
		client = newSafeHTTPClient(safeHTTPHooks{})
	}
	return &PluginDeliverHandler{db: db, secrets: secrets, workItems: workItems, client: client, renderer: renderer}
}

func (h *PluginDeliverHandler) SetDiagnosticRecorder(recorder observabilityport.Recorder, logger observabilityport.RuntimeLogger) {
	if h == nil {
		return
	}
	h.observability = recorder
	h.logger = logger
}

var _ worker.Handler = (*PluginDeliverHandler)(nil)

type deliveryRow struct {
	id                  string
	adapterKind         string
	adapterConfigJSON   []byte
	adapterConfigStatus string
	configSchemaVersion int
	secretRef           sql.NullString
	deliveryKind        string
	triggerKind         string
	state               string
	version             int64
	attemptCount        int
	payload             []byte
	payloadDigest       string
	targetDisplay       string
	templateCatalog     string
	titleTemplate       string
	bodyTemplate        string
	renderedTitle       sql.NullString
	renderedBody        sql.NullString
}

type durableAttempt struct {
	id             string
	number         int
	requestStarted bool
	startedAt      time.Time
}

type attemptResult struct {
	responseStatus *int
	responseBytes  int64
	providerAck    json.RawMessage
	errorCode      string
	err            string
	state          string
	// retryAfter is the wait before the next automatic attempt; it is set
	// only when the attempt settles waiting_retry.
	retryAfter time.Duration
}

func (h *PluginDeliverHandler) Handle(ctx context.Context, claim workitems.Claim) (worker.Outcome, error) {
	if claim.Kind != workitems.KindPluginDeliver {
		return worker.Outcome{}, fmt.Errorf("plugin deliver handler received kind %q", claim.Kind)
	}
	if h == nil || h.db == nil || h.workItems == nil {
		return worker.Outcome{}, errors.New("plugin deliver handler is not configured")
	}
	row, err := h.loadDelivery(ctx, claim.Owner.ID)
	if err != nil {
		return worker.Outcome{}, err
	}
	if terminalDelivery(row.state) {
		return worker.Finish(workitems.StateSucceeded, workitems.OwnerResult{State: row.state, ReasonCode: "already_terminal", Reason: "delivery was already terminal"}, h.terminalReplayWriter(claim)), nil
	}

	// The dispatch reads the frozen payload the row persisted. A payload that is
	// not the canonical shape cannot be sent or replayed honestly, so the
	// delivery fails closed before any attempt exists — the stored bytes are
	// never repaired, defaulted or re-derived here.
	frozenPayload, payloadStatus := deliverypayload.Normalize(row.payload)
	if payloadStatus != deliverypayload.StatusAvailable {
		code := "delivery_payload_" + payloadStatus
		reason := "the frozen delivery payload is unavailable"
		if payloadStatus == deliverypayload.StatusInvalid {
			reason = "the frozen delivery payload is not a canonical delivery payload"
		}
		outcome := worker.Finish(workitems.StateFailed, workitems.OwnerResult{State: "failed", ReasonCode: code, Reason: reason}, h.payloadFailureWriter(claim, code, reason))
		return outcome, nil
	}
	// Downstream builders use the validated canonical bytes, never the raw column.
	row.payload = frozenPayload

	// The frozen adapter config is validated the same way: a snapshot that is
	// not a canonical allowlisted document for this adapter kind cannot be
	// executed, so the delivery fails closed before any attempt exists.
	canonicalConfig, configStatus := adapterconfig.Normalize(row.adapterKind, row.configSchemaVersion, row.adapterConfigJSON)
	if row.adapterConfigStatus != adapterconfig.StatusAvailable || configStatus != adapterconfig.StatusAvailable {
		code := "delivery_config_" + configStatus
		reason := "the frozen adapter config is unavailable"
		if configStatus == adapterconfig.StatusInvalid {
			reason = "the frozen adapter config is not a canonical allowlisted document"
		}
		outcome := worker.Finish(workitems.StateFailed, workitems.OwnerResult{State: "failed", ReasonCode: code, Reason: reason}, h.payloadFailureWriter(claim, code, reason))
		return outcome, nil
	}
	row.adapterConfigJSON = canonicalConfig

	var attempt durableAttempt
	claim, err = h.workItems.MutateClaim(ctx, claim, h.beginAttemptWriter(claim, row, &attempt))
	if err != nil {
		return worker.Outcome{}, err
	}
	if h.hooks.afterAttemptCommit != nil {
		if err := h.hooks.afterAttemptCommit(); err != nil {
			return worker.Outcome{}, err
		}
	}
	if attempt.requestStarted {
		// The previous claim started this attempt's request and was lost
		// before it recorded a response (a crash or a lease expiry). The
		// attempt settles as unknown without dialling again; the retry policy
		// then decides whether a new attempt follows.
		result := attemptResult{state: "unknown", errorCode: "request_outcome_unknown", err: "the request may have been sent; no response was recorded"}
		return h.settleAttempt(claim, row, attempt, result, newSecretRedactor("")), nil
	}

	title, body, err := h.resolveMessage(row)
	if err != nil {
		return h.prepareFailure(claim, attempt, "notification_template_invalid", "notification template is invalid", newSecretRedactor("")), nil
	}
	secret, err := h.resolveSecret(row)
	if err != nil {
		return h.prepareFailure(claim, attempt, "secret_resolve_failed", err.Error(), newSecretRedactor("")), nil
	}
	redactor := newSecretRedactor(secret)
	req, err := h.buildRequest(ctx, row, title, body, secret, claim.Owner.ID, attempt.number)
	if err != nil {
		return h.prepareFailure(claim, attempt, "request_build_failed", err.Error(), redactor), nil
	}

	summary := requestSummary(req)
	claim, err = h.workItems.MutateClaim(ctx, claim, h.requestStartWriter(claim, attempt, summary))
	if err != nil {
		return worker.Outcome{}, err
	}
	if h.hooks.afterRequestStartCommit != nil {
		if err := h.hooks.afterRequestStartCommit(); err != nil {
			return worker.Outcome{}, err
		}
	}

	resp, doErr := h.client.Do(req)
	result := attemptResult{}
	if doErr != nil {
		result = attemptResult{state: "unknown", errorCode: "transport_unknown", err: redactor.text(safeTransportError(doErr).Error(), 4096)}
	} else {
		result = readResponseResult(resp, row.adapterKind, redactor)
	}
	if h.hooks.afterResponseBeforeFinal != nil {
		if err := h.hooks.afterResponseBeforeFinal(); err != nil {
			return worker.Outcome{}, err
		}
	}

	if result.state == "delivered" {
		owner := workitems.OwnerResult{State: result.state, ReasonCode: "delivered", Reason: "notification delivered successfully"}
		outcome := worker.Finish(workitems.StateSucceeded, owner, h.finalWriter(claim, attempt, result, redactor))
		return worker.WithAfterCommit(outcome, h.prepareDiagnosticRecoveryAfterCommit(claim.Owner.ID)), nil
	}
	return h.settleAttempt(claim, row, attempt, result, redactor), nil
}

// settleAttempt records a non-delivered attempt and decides what follows. An
// unknown or retryable outcome of a business delivery schedules the next
// attempt on the bounded backoff (deliveryRetryDelays) while attempts remain
// and the WorkItem's claim budget allows another claim; otherwise the
// delivery settles visibly: unknown stays unknown, a retryable status that
// never cleared becomes failed. A definite failure (4xx, a rejected provider
// reply) never retries. A test delivery is interactive feedback: an unknown
// outcome settles at once and a retryable status retries only within the
// WorkItem's claim budget.
func (h *PluginDeliverHandler) settleAttempt(claim workitems.Claim, row deliveryRow, attempt durableAttempt, result attemptResult, redactor secretRedactor) worker.Outcome {
	retry := func(delay time.Duration) worker.Outcome {
		result.state = "waiting_retry"
		result.retryAfter = delay
		return worker.Retry(result.errorCode, redactor.text(result.err, 2048), delay, h.finalWriter(claim, attempt, result, redactor))
	}
	retryable := result.state == "unknown" || result.state == "waiting_retry"
	if retryable && row.deliveryKind == "business" {
		if delay, ok := deliveryRetryDelay(attempt.number); ok && claim.Attempt < claim.MaxAttempts {
			return retry(delay)
		}
	}
	if result.state == "waiting_retry" && row.deliveryKind != "business" && claim.Attempt < claim.MaxAttempts {
		return retry(testDeliveryRetryDelay)
	}
	if result.state == "waiting_retry" {
		result.state = "failed"
	}
	owner := workitems.OwnerResult{State: result.state, ReasonCode: result.errorCode, Reason: redactor.text(result.err, 2048)}
	return worker.Finish(workitems.StateFailed, owner, h.finalWriter(claim, attempt, result, redactor))
}

func (h *PluginDeliverHandler) prepareDiagnosticRecoveryAfterCommit(deliveryID string) func(context.Context) {
	return func(ctx context.Context) {
		// Error code participates in the episode identity, so close each prepare
		// failure that may have opened for this delivery.
		for _, code := range [...]observabilityport.DiagnosticErrorCode{
			observabilityport.ErrorNotificationTemplateInvalid,
			observabilityport.ErrorSecretResolveFailed,
			observabilityport.ErrorRequestBuildFailed,
		} {
			if closeDiagnostic := observabilityport.DiagnosticSuccessAfterCommit(h.observability, h.logger, observabilityport.DiagnosticSuccessRequest{
				Identity: observabilityport.PluginPrepareFailure(code), ScopeID: deliveryID,
			}); closeDiagnostic != nil {
				closeDiagnostic(ctx)
			}
		}
	}
}

func (h *PluginDeliverHandler) prepareFailure(claim workitems.Claim, attempt durableAttempt, code, reason string, redactor secretRedactor) worker.Outcome {
	redactedReason := redactor.text(reason, 4096)
	result := attemptResult{state: "failed", errorCode: code, err: redactedReason}
	outcome := worker.Finish(workitems.StateFailed, workitems.OwnerResult{State: result.state, ReasonCode: code, Reason: redactedReason}, h.finalWriter(claim, attempt, result, redactor))
	return worker.WithAfterCommit(outcome, h.diagnosticAfterCommit(claim.Owner.ID, code, redactedReason))
}

func (h *PluginDeliverHandler) diagnosticAfterCommit(deliveryID, code, reason string) func(context.Context) {
	identity := observabilityport.PluginPrepareFailure(observabilityport.DiagnosticErrorCode(code))
	return observabilityport.DiagnosticFailureAfterCommit(h.observability, h.logger, observabilityport.DiagnosticFailureRequest{
		Identity: identity, Severity: observabilityport.SeverityError, ScopeID: deliveryID, LastError: reason,
	})
}

func (h *PluginDeliverHandler) loadDelivery(ctx context.Context, id string) (deliveryRow, error) {
	var row deliveryRow
	err := h.db.QueryRowContext(ctx, `SELECT id, adapter_kind, adapter_config_snapshot, config_status, config_schema_version, secret_ref, delivery_kind, trigger_kind, state, version, attempt_count, payload, payload_digest, target_display, template_catalog_version, title_template_snapshot, body_template_snapshot, rendered_title, rendered_body FROM plugin_deliveries WHERE id=$1`, id).Scan(&row.id, &row.adapterKind, &row.adapterConfigJSON, &row.adapterConfigStatus, &row.configSchemaVersion, &row.secretRef, &row.deliveryKind, &row.triggerKind, &row.state, &row.version, &row.attemptCount, &row.payload, &row.payloadDigest, &row.targetDisplay, &row.templateCatalog, &row.titleTemplate, &row.bodyTemplate, &row.renderedTitle, &row.renderedBody)
	if errors.Is(err, sql.ErrNoRows) {
		return row, errors.New("plugin delivery not found")
	}
	if err != nil {
		return row, fmt.Errorf("read plugin delivery: %w", err)
	}
	return row, nil
}

func (h *PluginDeliverHandler) resolveMessage(row deliveryRow) (string, string, error) {
	if row.renderedTitle.Valid && row.renderedBody.Valid {
		return row.renderedTitle.String, row.renderedBody.String, nil
	}
	if !row.renderedTitle.Valid && !row.renderedBody.Valid && h.renderer != nil {
		return h.renderer.RenderNotification(row.titleTemplate, row.bodyTemplate, row.triggerKind, row.templateCatalog)
	}
	return "", "", errors.New("delivery rendered state is invalid")
}

func (h *PluginDeliverHandler) resolveSecret(row deliveryRow) (string, error) {
	if !row.secretRef.Valid || row.secretRef.String == "" {
		return "", nil
	}
	if h.secrets == nil {
		return "", errors.New("secret store is not configured")
	}
	value, ok := h.secrets.Resolve(row.secretRef.String)
	if !ok {
		return "", errors.New("secret reference could not be resolved")
	}
	return value, nil
}

func (h *PluginDeliverHandler) buildRequest(ctx context.Context, row deliveryRow, title, body, secretValue, deliveryID string, attemptNumber int) (*http.Request, error) {
	switch row.adapterKind {
	case "telegram":
		return h.buildTelegramRequest(ctx, row, title+"\n\n"+body, secretValue)
	case "feishu":
		return h.buildFeishuRequest(ctx, title+"\n\n"+body, secretValue)
	case "webhook":
		return h.buildWebhookRequest(ctx, row, title, body, secretValue, deliveryID, attemptNumber)
	default:
		return nil, errors.New("unsupported adapter kind")
	}
}

func (h *PluginDeliverHandler) buildTelegramRequest(ctx context.Context, row deliveryRow, message, secretValue string) (*http.Request, error) {
	var config struct {
		ChatID string `json:"chat_id"`
	}
	if json.Unmarshal(row.adapterConfigJSON, &config) != nil || config.ChatID == "" {
		return nil, errors.New("telegram config missing chat_id")
	}
	var secret struct {
		BotToken *string `json:"bot_token"`
	}
	if json.Unmarshal([]byte(secretValue), &secret) != nil || secret.BotToken == nil || *secret.BotToken == "" {
		return nil, errors.New("telegram secret missing bot_token")
	}
	payload, _ := json.Marshal(map[string]string{"chat_id": config.ChatID, "text": message, "parse_mode": ""})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.telegram.org/bot"+*secret.BotToken+"/sendMessage", bytes.NewReader(payload))
	if err != nil {
		return nil, errors.New("telegram request is invalid")
	}
	req.Header.Set("Content-Type", "application/json")
	return req, nil
}

func (h *PluginDeliverHandler) buildFeishuRequest(ctx context.Context, message, secretValue string) (*http.Request, error) {
	var secret struct {
		WebhookURL *string `json:"webhook_url"`
	}
	if json.Unmarshal([]byte(secretValue), &secret) != nil || secret.WebhookURL == nil {
		return nil, errors.New("feishu secret missing webhook_url")
	}
	endpoint, err := parseHTTPSURL(*secret.WebhookURL)
	if err != nil {
		return nil, err
	}
	payload, _ := json.Marshal(map[string]any{"msg_type": "text", "content": map[string]string{"text": message}})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(payload))
	if err != nil {
		return nil, errors.New("feishu request is invalid")
	}
	req.Header.Set("Content-Type", "application/json")
	return req, nil
}

func (h *PluginDeliverHandler) buildWebhookRequest(ctx context.Context, row deliveryRow, title, body, secretValue, deliveryID string, attemptNumber int) (*http.Request, error) {
	var secret struct {
		EndpointURL   *string `json:"endpoint_url"`
		AuthValue     *string `json:"auth_value"`
		SigningSecret *string `json:"signing_secret"`
	}
	if json.Unmarshal([]byte(secretValue), &secret) != nil || secret.EndpointURL == nil {
		return nil, errors.New("webhook secret missing endpoint_url")
	}
	endpoint, err := parseHTTPSURL(*secret.EndpointURL)
	if err != nil {
		return nil, err
	}
	var config struct {
		AuthKind       string  `json:"auth_kind"`
		AuthHeaderName *string `json:"auth_header_name"`
	}
	if json.Unmarshal(row.adapterConfigJSON, &config) != nil {
		return nil, errors.New("webhook config is invalid")
	}
	// The version 2 document carries the frozen facts under the unified fact
	// paths. Every attempt of one delivery sends the same delivery_id, in the
	// body and in X-Webhook-Id, so a receiver can drop a repeat. Vendor-shaped
	// adapters (telegram, feishu) keep their fixed text formats.
	now := time.Now()
	if h.now != nil {
		now = h.now()
	}
	payload, err := buildWebhookEnvelope(deliveryID, row.triggerKind, attemptNumber, now, title, body, row.payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(payload))
	if err != nil {
		return nil, errors.New("webhook request is invalid")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(HeaderWebhookID, deliveryID)
	req.Header.Set(HeaderWebhookTimestamp, strconv.FormatInt(now.Unix(), 10))
	if secret.SigningSecret != nil && *secret.SigningSecret != "" {
		req.Header.Set(HeaderWebhookSignature, SignWebhook(*secret.SigningSecret, now.Unix(), payload))
	}
	value := ""
	if secret.AuthValue != nil {
		value = *secret.AuthValue
	}
	switch config.AuthKind {
	case "none":
	case "bearer":
		if value == "" {
			return nil, errors.New("webhook bearer value is missing")
		}
		req.Header.Set("Authorization", "Bearer "+value)
	case "header":
		if config.AuthHeaderName == nil || !safeAuthHeaderName(*config.AuthHeaderName) || value == "" {
			return nil, errors.New("webhook auth header is invalid")
		}
		req.Header.Set(*config.AuthHeaderName, value)
	default:
		return nil, errors.New("webhook auth kind is invalid")
	}
	return req, nil
}

func requestSummary(req *http.Request) json.RawMessage {
	raw, _ := json.Marshal(map[string]any{"method": req.Method, "content_type": req.Header.Get("Content-Type")})
	return raw
}

func readResponseResult(resp *http.Response, adapterKind string, redactor secretRedactor) attemptResult {
	if resp == nil {
		return attemptResult{state: "unknown", errorCode: "transport_unknown", err: "transport returned no response"}
	}
	defer resp.Body.Close()
	result := attemptResult{responseStatus: &resp.StatusCode}
	limited := &io.LimitedReader{R: resp.Body, N: maxResponseBody + 1}
	body, err := io.ReadAll(limited)
	result.responseBytes = int64(len(body))
	if err != nil {
		result.state = "unknown"
		result.errorCode = "response_read_failed"
		result.err = "response body could not be read"
		return result
	}
	if len(body) > maxResponseBody {
		result.state = "unknown"
		result.errorCode = "response_too_large"
		result.err = "response body exceeded the safe limit"
		return result
	}
	result.providerAck = allowlistedProviderAck(adapterKind, body)
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return classifySuccessfulResponse(result, adapterKind, body)
	case resp.StatusCode == 429 || resp.StatusCode >= 500:
		result.state = "waiting_retry"
		result.errorCode = fmt.Sprintf("http_%d", resp.StatusCode)
		result.err = "provider returned a retryable HTTP status"
	default:
		result.state = "failed"
		result.errorCode = fmt.Sprintf("http_%d", resp.StatusCode)
		result.err = "provider returned a permanent HTTP status"
	}
	return result
}

func classifySuccessfulResponse(result attemptResult, adapterKind string, body []byte) attemptResult {
	switch adapterKind {
	case "telegram":
		var response struct {
			OK *bool `json:"ok"`
		}
		if json.Unmarshal(body, &response) != nil || response.OK == nil || !*response.OK {
			result.state = "failed"
			result.errorCode = "telegram_rejected"
			result.err = "Telegram response did not confirm delivery"
			return result
		}
	case "feishu":
		var response struct {
			Code json.RawMessage `json:"code"`
		}
		if json.Unmarshal(body, &response) != nil {
			result.state = "failed"
			result.errorCode = "feishu_rejected"
			result.err = "Feishu response did not confirm delivery"
			return result
		}
		code, ok := strictJSONInteger(response.Code)
		if !ok || code != 0 {
			result.state = "failed"
			result.errorCode = "feishu_rejected"
			result.err = "Feishu response did not confirm delivery"
			return result
		}
	}
	result.state = "delivered"
	return result
}

func allowlistedProviderAck(adapterKind string, body []byte) json.RawMessage {
	ack := map[string]any{}
	switch adapterKind {
	case "telegram":
		var response struct {
			OK     *bool `json:"ok"`
			Result *struct {
				MessageID json.RawMessage `json:"message_id"`
			} `json:"result"`
		}
		if json.Unmarshal(body, &response) != nil {
			return nil
		}
		if response.OK != nil {
			ack["ok"] = *response.OK
		}
		if response.Result != nil {
			if messageID, ok := strictJSONInteger(response.Result.MessageID); ok && messageID >= 0 {
				ack["message_id"] = messageID
			}
		}
	case "feishu":
		var response struct {
			Code json.RawMessage `json:"code"`
		}
		if json.Unmarshal(body, &response) != nil {
			return nil
		}
		if code, ok := strictJSONInteger(response.Code); ok {
			ack["code"] = code
		}
	case "webhook":
		return nil
	default:
		return nil
	}
	if len(ack) == 0 {
		return nil
	}
	raw, _ := json.Marshal(ack)
	return raw
}

func strictJSONInteger(raw json.RawMessage) (int64, bool) {
	if len(raw) == 0 || raw[0] == '"' {
		return 0, false
	}
	value, err := json.Number(string(raw)).Int64()
	return value, err == nil
}

func (h *PluginDeliverHandler) beginAttemptWriter(claim workitems.Claim, snapshot deliveryRow, out *durableAttempt) workitems.OwnerWriter {
	return func(ctx context.Context, scope txscope.Tx, _ workitems.OwnerResult) (int64, error) {
		tx := scope.SQLTx()
		if tx == nil {
			return 0, errors.New("plugin delivery owner writer transaction is nil")
		}
		state, version, count, err := lockDelivery(ctx, tx, claim.Owner.ID)
		if err != nil {
			return 0, err
		}
		if version != claim.ExpectedOwnerVersion {
			return 0, errors.New("plugin delivery version conflict")
		}
		if terminalDelivery(state) {
			return version, nil
		}
		if h.hooks.beforeAttemptWrite != nil {
			if err := h.hooks.beforeAttemptWrite(); err != nil {
				return 0, err
			}
		}
		var requestStarted sql.NullTime
		err = tx.QueryRowContext(ctx, `SELECT id, attempt_number, started_at, request_started_at FROM plugin_delivery_attempts WHERE plugin_delivery_id=$1 AND response_received_at IS NULL AND response_status IS NULL AND error_code IS NULL ORDER BY attempt_number DESC LIMIT 1 FOR UPDATE`, claim.Owner.ID).Scan(&out.id, &out.number, &out.startedAt, &requestStarted)
		if err == nil {
			out.requestStarted = requestStarted.Valid
			return version, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return 0, err
		}
		out.id = uuid.NewString()
		out.number = count + 1
		if _, err = tx.ExecContext(ctx, `INSERT INTO plugin_delivery_attempts(id,plugin_delivery_id,attempt_number,started_at,target_display,payload_digest) VALUES($1,$2,$3,CURRENT_TIMESTAMP,$4,$5)`, out.id, claim.Owner.ID, out.number, snapshot.targetDisplay, snapshot.payloadDigest); err != nil {
			return 0, err
		}
		if err = tx.QueryRowContext(ctx, `UPDATE plugin_deliveries SET state='delivering',attempt_count=$2,next_attempt_at=NULL,version=version+1,updated_at=CURRENT_TIMESTAMP WHERE id=$1 AND version=$3 RETURNING version`, claim.Owner.ID, out.number, version).Scan(&version); err != nil {
			return 0, err
		}
		if h.hooks.beforeAttemptCommit != nil {
			if err := h.hooks.beforeAttemptCommit(); err != nil {
				return 0, err
			}
		}
		return version, nil
	}
}

func (h *PluginDeliverHandler) requestStartWriter(claim workitems.Claim, attempt durableAttempt, summary json.RawMessage) workitems.OwnerWriter {
	return func(ctx context.Context, scope txscope.Tx, _ workitems.OwnerResult) (int64, error) {
		tx := scope.SQLTx()
		if tx == nil {
			return 0, errors.New("plugin delivery owner writer transaction is nil")
		}
		_, version, _, err := lockDelivery(ctx, tx, claim.Owner.ID)
		if err != nil {
			return 0, err
		}
		if version != claim.ExpectedOwnerVersion {
			return 0, errors.New("plugin delivery version conflict")
		}
		if h.hooks.beforeRequestStartWrite != nil {
			if err := h.hooks.beforeRequestStartWrite(); err != nil {
				return 0, err
			}
		}
		result, err := tx.ExecContext(ctx, `UPDATE plugin_delivery_attempts SET request_started_at=CURRENT_TIMESTAMP,request_summary=$3 WHERE id=$1 AND plugin_delivery_id=$2 AND request_started_at IS NULL AND response_received_at IS NULL AND error_code IS NULL`, attempt.id, claim.Owner.ID, summary)
		if err != nil {
			return 0, err
		}
		n, _ := result.RowsAffected()
		if n != 1 {
			return 0, errors.New("plugin delivery attempt is no longer callable")
		}
		if err = tx.QueryRowContext(ctx, `UPDATE plugin_deliveries SET version=version+1,updated_at=CURRENT_TIMESTAMP WHERE id=$1 AND version=$2 RETURNING version`, claim.Owner.ID, version).Scan(&version); err != nil {
			return 0, err
		}
		return version, nil
	}
}

func (h *PluginDeliverHandler) finalWriter(claim workitems.Claim, attempt durableAttempt, result attemptResult, redactor secretRedactor) workitems.OwnerWriter {
	return func(ctx context.Context, scope txscope.Tx, owner workitems.OwnerResult) (int64, error) {
		tx := scope.SQLTx()
		if tx == nil {
			return 0, errors.New("plugin delivery owner writer transaction is nil")
		}
		state, version, _, err := lockDelivery(ctx, tx, claim.Owner.ID)
		if err != nil {
			return 0, err
		}
		if version != claim.ExpectedOwnerVersion {
			return 0, errors.New("plugin delivery version conflict")
		}
		if terminalDelivery(state) {
			return version, nil
		}
		preview := fmt.Sprintf("status=%s body_bytes=%d", statusSummary(result.responseStatus), result.responseBytes)
		var ack any = nil
		if len(result.providerAck) > 0 {
			ack = result.providerAck
		}
		redactedErr := redactor.text(result.err, 4096)
		updated, err := tx.ExecContext(ctx, `UPDATE plugin_delivery_attempts SET response_received_at=CASE WHEN $3::integer IS NULL THEN NULL ELSE CURRENT_TIMESTAMP END,duration_ms=CASE WHEN $3::integer IS NULL THEN NULL ELSE GREATEST(0,(EXTRACT(EPOCH FROM (CURRENT_TIMESTAMP-started_at))*1000)::integer) END,response_status=$3,response_preview=$4,provider_ack=$5,error_code=NULLIF($6,''),error=NULLIF($7,'') WHERE id=$1 AND plugin_delivery_id=$2 AND response_received_at IS NULL AND error_code IS NULL`, attempt.id, claim.Owner.ID, result.responseStatus, preview, ack, result.errorCode, redactedErr)
		if err != nil {
			return 0, err
		}
		n, _ := updated.RowsAffected()
		if n != 1 {
			return 0, errors.New("plugin delivery attempt final evidence already exists")
		}
		var deliveredAt any = nil
		if owner.State == "delivered" {
			deliveredAt = time.Now().UTC()
		}
		// next_attempt_at mirrors the WorkItem's available_at for a scheduled
		// retry, so the delivery history can show when the next attempt runs.
		var retrySeconds any = nil
		if owner.State == "waiting_retry" {
			retrySeconds = result.retryAfter.Seconds()
		}
		if err = tx.QueryRowContext(ctx, `UPDATE plugin_deliveries SET state=$2,last_error_code=NULLIF($3,''),last_error=NULLIF($4,''),delivered_at=$5,next_attempt_at=CASE WHEN $7::double precision IS NULL THEN NULL ELSE CURRENT_TIMESTAMP + make_interval(secs => $7::double precision) END,version=version+1,updated_at=CURRENT_TIMESTAMP WHERE id=$1 AND version=$6 RETURNING version`, claim.Owner.ID, owner.State, result.errorCode, redactedErr, deliveredAt, version, retrySeconds).Scan(&version); err != nil {
			return 0, err
		}
		return version, nil
	}
}

// payloadFailureWriter fails a delivery whose frozen payload cannot be sent.
// Nothing was transmitted, so no attempt row is written: only the delivery row
// records the terminal failure and its bounded reason.
func (h *PluginDeliverHandler) payloadFailureWriter(claim workitems.Claim, code, reason string) workitems.OwnerWriter {
	return func(ctx context.Context, scope txscope.Tx, _ workitems.OwnerResult) (int64, error) {
		tx := scope.SQLTx()
		if tx == nil {
			return 0, errors.New("plugin delivery owner writer transaction is nil")
		}
		state, version, _, err := lockDelivery(ctx, tx, claim.Owner.ID)
		if err != nil {
			return 0, err
		}
		if version != claim.ExpectedOwnerVersion {
			return 0, errors.New("plugin delivery version conflict")
		}
		if terminalDelivery(state) {
			return version, nil
		}
		if err := tx.QueryRowContext(ctx, `UPDATE plugin_deliveries SET state='failed',last_error_code=$2,last_error=$3,next_attempt_at=NULL,version=version+1,updated_at=CURRENT_TIMESTAMP WHERE id=$1 AND version=$4 RETURNING version`, claim.Owner.ID, code, truncate(reason, 4096), version).Scan(&version); err != nil {
			return 0, err
		}
		return version, nil
	}
}

func (h *PluginDeliverHandler) terminalReplayWriter(claim workitems.Claim) workitems.OwnerWriter {
	return func(ctx context.Context, scope txscope.Tx, _ workitems.OwnerResult) (int64, error) {
		tx := scope.SQLTx()
		if tx == nil {
			return 0, errors.New("plugin delivery owner writer transaction is nil")
		}
		state, v, _, e := lockDelivery(ctx, tx, claim.Owner.ID)
		if e != nil {
			return 0, e
		}
		if !terminalDelivery(state) {
			return 0, errors.New("delivery is not terminal")
		}
		return v, nil
	}
}
func lockDelivery(ctx context.Context, tx *sql.Tx, id string) (string, int64, int, error) {
	var s string
	var v int64
	var c int
	e := tx.QueryRowContext(ctx, `SELECT state,version,attempt_count FROM plugin_deliveries WHERE id=$1 FOR UPDATE`, id).Scan(&s, &v, &c)
	return s, v, c, e
}
func terminalDelivery(state string) bool {
	return state == "delivered" || state == "failed" || state == "unknown"
}
func statusSummary(status *int) string {
	if status == nil {
		return "none"
	}
	return fmt.Sprintf("%d", *status)
}
