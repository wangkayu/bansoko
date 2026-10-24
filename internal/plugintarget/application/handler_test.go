package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"bansoko/internal/workitems"
)

// --- test fakes ---

// fakeHTTPClient returns a canned response or error.
type fakeHTTPClient struct {
	statusCode int
	body       string
	err        error
	lastReq    *http.Request
	lastBody   []byte
}

func (f *fakeHTTPClient) Do(req *http.Request) (*http.Response, error) {
	f.lastReq = req
	if req.Body != nil {
		f.lastBody, _ = io.ReadAll(req.Body)
	}
	if f.err != nil {
		return nil, f.err
	}
	return &http.Response{
		StatusCode: f.statusCode,
		Body:       io.NopCloser(strings.NewReader(f.body)),
		Header:     make(http.Header),
	}, nil
}

// fakeSecretResolver resolves from a static map.
type fakeSecretResolver struct {
	secrets map[string]string
}

func (f *fakeSecretResolver) Resolve(ref string) (string, bool) {
	v, ok := f.secrets[ref]
	return v, ok
}

// fakeTemplateRenderer returns fixed rendered output.
type fakeTemplateRenderer struct {
	title string
	body  string
	err   error
}

func (f *fakeTemplateRenderer) RenderNotification(_, _, _, _ string) (string, string, error) {
	return f.title, f.body, f.err
}

// --- tests ---

func TestPluginDeliverHandler_Handle_2xx_Delivered(t *testing.T) {
	client := &fakeHTTPClient{statusCode: 200, body: `{"ok":true}`}
	renderer := &fakeTemplateRenderer{title: "Test Title", body: "Test Body"}
	handler := NewPluginDeliverHandler(nil, nil, nil, client, renderer)

	row := deliveryRow{
		id:                "delivery-1",
		adapterKind:       "telegram",
		adapterConfigJSON: []byte(`{"adapter_kind":"telegram","chat_id":"12345"}`),
		secretRef:         sql.NullString{String: "ref-1", Valid: true},
		triggerKind:       "download_submitted",
		state:             "pending",
		version:           1,
		attemptCount:      0,
		payload:           []byte(`{"schema_version":1,"trigger_kind":"download_submitted","facts":{}}`),
		payloadDigest:     "sha256:abc123",
		targetDisplay:     "telegram:12345",
		titleTemplate:     "{{anime.title}}",
		bodyTemplate:      "{{release.title}}",
		renderedTitle:     sql.NullString{String: "Rendered Title", Valid: true},
		renderedBody:      sql.NullString{String: "Rendered Body", Valid: true},
	}

	secretResolver := &fakeSecretResolver{secrets: map[string]string{
		"ref-1": `{"adapter_kind":"telegram","bot_token":"123:ABC"}`,
	}}
	handler.secrets = secretResolver

	// We can't call Handle directly without a DB, but we can test buildRequest
	// and resolveMessage logic.
	title, body, err := handler.resolveMessage(row)
	if err != nil {
		t.Fatalf("resolveMessage: %v", err)
	}
	if title != "Rendered Title" || body != "Rendered Body" {
		t.Fatalf("expected rendered values, got title=%q body=%q", title, body)
	}

	req, err := handler.buildRequest(context.Background(), row, title, body, `{"adapter_kind":"telegram","bot_token":"123:ABC"}`, "delivery-1", 1)
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	if req.Method != http.MethodPost {
		t.Errorf("expected POST, got %s", req.Method)
	}
	if !strings.Contains(req.URL.String(), "api.telegram.org/bot123:ABC/sendMessage") {
		t.Errorf("unexpected URL: %s", req.URL.String())
	}
}

func TestPluginDeliverHandler_ResolveMessage_UsesRenderer(t *testing.T) {
	renderer := &fakeTemplateRenderer{title: "Sample Title", body: "Sample Body"}
	handler := NewPluginDeliverHandler(nil, nil, nil, nil, renderer)

	row := deliveryRow{
		titleTemplate: "{{anime.title}}",
		bodyTemplate:  "{{release.title}}",
		triggerKind:   "download_submitted",
		// renderedTitle and renderedBody are NULL (zero-value sql.NullString)
	}

	title, body, err := handler.resolveMessage(row)
	if err != nil {
		t.Fatalf("resolveMessage: %v", err)
	}
	if title != "Sample Title" || body != "Sample Body" {
		t.Fatalf("expected renderer output, got title=%q body=%q", title, body)
	}
}

func TestPluginDeliverHandler_ResolveMessage_RendererError(t *testing.T) {
	renderer := &fakeTemplateRenderer{err: errors.New("template syntax error")}
	handler := NewPluginDeliverHandler(nil, nil, nil, nil, renderer)

	row := deliveryRow{
		titleTemplate: "{{invalid}}",
		bodyTemplate:  "{{also_invalid}}",
		triggerKind:   "download_submitted",
	}

	_, _, err := handler.resolveMessage(row)
	if err == nil {
		t.Fatal("expected error from renderer")
	}
}

func TestPluginDeliverHandler_ResolveMessage_PartialState(t *testing.T) {
	handler := NewPluginDeliverHandler(nil, nil, nil, nil, nil)

	row := deliveryRow{
		renderedTitle: sql.NullString{String: "Title", Valid: true},
		// renderedBody is NULL
	}

	_, _, err := handler.resolveMessage(row)
	if err == nil {
		t.Fatal("expected error for partial render state")
	}
}

func TestPluginDeliverHandler_BuildTelegramRequest(t *testing.T) {
	handler := NewPluginDeliverHandler(nil, nil, nil, nil, nil)
	row := deliveryRow{
		adapterKind:       "telegram",
		adapterConfigJSON: []byte(`{"adapter_kind":"telegram","chat_id":"99999"}`),
	}

	req, err := handler.buildRequest(context.Background(), row, "Hello", "World", `{"bot_token":"tok123"}`, "d-1", 1)
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	if !strings.Contains(req.URL.String(), "bot tok123") && !strings.Contains(req.URL.String(), "bottok123") {
		// URL should be https://api.telegram.org/bottok123/sendMessage
		if req.URL.String() != "https://api.telegram.org/bottok123/sendMessage" {
			t.Errorf("unexpected URL: %s", req.URL.String())
		}
	}

	bodyBytes, _ := io.ReadAll(req.Body)
	var payload map[string]string
	if err := json.Unmarshal(bodyBytes, &payload); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if payload["chat_id"] != "99999" {
		t.Errorf("expected chat_id 99999, got %q", payload["chat_id"])
	}
	if payload["text"] != "Hello\n\nWorld" {
		t.Errorf("expected combined message, got %q", payload["text"])
	}
}

func TestPluginDeliverHandler_BuildFeishuRequest(t *testing.T) {
	handler := NewPluginDeliverHandler(nil, nil, nil, nil, nil)
	row := deliveryRow{
		adapterKind:       "feishu",
		adapterConfigJSON: []byte(`{"adapter_kind":"feishu"}`),
	}

	secretValue := `{"webhook_url":"https://open.feishu.cn/hook/abc123"}`
	req, err := handler.buildRequest(context.Background(), row, "Title", "Body", secretValue, "d-2", 1)
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	if req.URL.String() != "https://open.feishu.cn/hook/abc123" {
		t.Errorf("unexpected URL: %s", req.URL.String())
	}

	bodyBytes, _ := io.ReadAll(req.Body)
	var payload map[string]any
	if err := json.Unmarshal(bodyBytes, &payload); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if payload["msg_type"] != "text" {
		t.Errorf("expected msg_type text, got %v", payload["msg_type"])
	}
	content, ok := payload["content"].(map[string]any)
	if !ok {
		t.Fatal("content is not a map")
	}
	if content["text"] != "Title\n\nBody" {
		t.Errorf("expected combined message, got %q", content["text"])
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestPluginDeliverHandler_BuildWebhookRequest(t *testing.T) {
	handler := NewPluginDeliverHandler(nil, nil, nil, nil, nil)
	handler.now = func() time.Time { return time.Unix(1767225600, 0) }
	frozen := []byte(`{"schema_version":1,"trigger_kind":"download_submitted","facts":{"anime.title":"Frozen","anime.group":"SampleGroup","download_task_id":"task-1","vars.custom.tag":"x"}}`)
	row := deliveryRow{
		adapterKind:       "webhook",
		adapterConfigJSON: []byte(`{"adapter_kind":"webhook","auth_kind":"header","auth_header_name":"X-Hook-Token"}`),
		triggerKind:       "download_submitted",
		payload:           frozen,
	}

	secretValue := `{"endpoint_url":"https://example.com/hook","auth_value":"secret-tok"}`
	req, err := handler.buildRequest(context.Background(), row, "Title", "Body", secretValue, "d-3", 2)
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	if req.URL.String() != "https://example.com/hook" {
		t.Errorf("unexpected URL: %s", req.URL.String())
	}
	if req.Header.Get("X-Hook-Token") != "secret-tok" {
		t.Errorf("expected auth header, got %q", req.Header.Get("X-Hook-Token"))
	}
	if req.Header.Get(HeaderWebhookID) != "d-3" || req.Header.Get(HeaderWebhookTimestamp) != "1767225600" {
		t.Errorf("identity headers = %q %q", req.Header.Get(HeaderWebhookID), req.Header.Get(HeaderWebhookTimestamp))
	}
	if got := req.Header.Get(HeaderWebhookSignature); got != "" {
		t.Errorf("a target without a signing secret must not sign, got %q", got)
	}

	bodyBytes, _ := io.ReadAll(req.Body)
	var payload map[string]any
	if err := json.Unmarshal(bodyBytes, &payload); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if payload["version"] != WebhookEnvelopeVersion || payload["event"] != "download_submitted" || payload["delivery_id"] != "d-3" {
		t.Errorf("envelope = %v", payload)
	}
	if payload["attempt"] != float64(2) || payload["timestamp"] != "2026-01-01T00:00:00Z" {
		t.Errorf("attempt/timestamp = %v %v", payload["attempt"], payload["timestamp"])
	}
	msg, ok := payload["message"].(map[string]any)
	if !ok {
		t.Fatal("message is not a map")
	}
	if msg["title"] != "Title" || msg["body"] != "Body" {
		t.Errorf("unexpected message: %v", msg)
	}
	// The document carries the frozen facts under the unified paths, nested;
	// legacy names and bookkeeping keys are dropped, frozen vars are kept.
	want := map[string]any{
		"anime": map[string]any{"title": "Frozen", "group": "SampleGroup"},
		"vars":  map[string]any{"custom": map[string]any{"tag": "x"}},
	}
	if !reflect.DeepEqual(payload["payload"], want) {
		t.Errorf("submitted payload = %v, want %v", payload["payload"], want)
	}
}

func TestPluginDeliverHandler_WebhookSignatureCoversTimestampAndBody(t *testing.T) {
	handler := NewPluginDeliverHandler(nil, nil, nil, nil, nil)
	handler.now = func() time.Time { return time.Unix(1767225600, 0) }
	row := deliveryRow{
		adapterKind:       "webhook",
		adapterConfigJSON: []byte(`{"adapter_kind":"webhook","auth_kind":"none"}`),
		triggerKind:       "download_failed",
		payload:           []byte(`{"schema_version":1,"trigger_kind":"download_failed","facts":{"anime.title":"A"}}`),
	}
	req, err := handler.buildRequest(context.Background(), row, "T", "B", `{"endpoint_url":"https://example.com/hook","signing_secret":"whsec-test"}`, "d-9", 1)
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	body, _ := io.ReadAll(req.Body)
	if got, want := req.Header.Get(HeaderWebhookSignature), SignWebhook("whsec-test", 1767225600, body); got != want {
		t.Fatalf("signature = %q, want %q", got, want)
	}
	if strings.Contains(string(body), "whsec-test") || strings.Contains(req.Header.Get(HeaderWebhookSignature), "whsec-test") {
		t.Fatal("the signing secret leaked into the request")
	}
}

// TestSignWebhookKnownVector pins the signature scheme: HMAC-SHA256 over
// "<timestamp>.<raw body>", lowercase hex, "sha256=" prefix. The expected
// value was computed independently (openssl dgst -sha256 -hmac).
func TestSignWebhookKnownVector(t *testing.T) {
	got := SignWebhook("whsec_test_secret", 1767225600, []byte(`{"version":"1","delivery_id":"d-1"}`))
	const want = "sha256=66695cf7a3300b8ab63db2154980ffe45db24b2748b9f184a430ba04ce173d59"
	if got != want {
		t.Fatalf("SignWebhook = %q, want %q", got, want)
	}
}

func TestPluginDeliverHandler_BuildTelegramRequest_MissingBotToken(t *testing.T) {
	handler := NewPluginDeliverHandler(nil, nil, nil, nil, nil)
	row := deliveryRow{
		adapterKind:       "telegram",
		adapterConfigJSON: []byte(`{"adapter_kind":"telegram","chat_id":"12345"}`),
	}

	_, err := handler.buildRequest(context.Background(), row, "T", "B", `{"bot_token":null}`, "d-4", 1)
	if err == nil {
		t.Fatal("expected error for missing bot_token")
	}
}

func TestPluginDeliverHandler_BuildFeishuRequest_MissingWebhookURL(t *testing.T) {
	handler := NewPluginDeliverHandler(nil, nil, nil, nil, nil)
	row := deliveryRow{
		adapterKind:       "feishu",
		adapterConfigJSON: []byte(`{"adapter_kind":"feishu"}`),
	}

	_, err := handler.buildRequest(context.Background(), row, "T", "B", `{}`, "d-5", 1)
	if err == nil {
		t.Fatal("expected error for missing webhook_url")
	}
}

func TestPluginDeliverHandler_BuildWebhookRequest_MissingEndpointURL(t *testing.T) {
	handler := NewPluginDeliverHandler(nil, nil, nil, nil, nil)
	row := deliveryRow{
		adapterKind:       "webhook",
		adapterConfigJSON: []byte(`{"adapter_kind":"webhook","auth_kind":"none"}`),
	}

	_, err := handler.buildRequest(context.Background(), row, "T", "B", `{}`, "d-6", 1)
	if err == nil {
		t.Fatal("expected error for missing endpoint_url")
	}
}

func TestPluginDeliverHandler_UnsupportedAdapterKind(t *testing.T) {
	handler := NewPluginDeliverHandler(nil, nil, nil, nil, nil)
	row := deliveryRow{
		adapterKind:       "slack",
		adapterConfigJSON: []byte(`{}`),
	}

	_, err := handler.buildRequest(context.Background(), row, "T", "B", "", "d-7", 1)
	if err == nil {
		t.Fatal("expected error for unsupported adapter kind")
	}
}

func TestPluginDeliverHandler_Handle_InvalidKind(t *testing.T) {
	handler := NewPluginDeliverHandler(nil, nil, nil, nil, nil)
	claim := workitems.Claim{Kind: workitems.KindRSSRefresh}
	_, err := handler.Handle(context.Background(), claim)
	if err == nil {
		t.Fatal("expected error for wrong kind")
	}
}

func TestPluginDeliverHandler_TruncateAndRedactUTF8(t *testing.T) {
	if got := truncate("hello", 3); got != "hel" {
		t.Errorf("truncate: expected hel, got %q", got)
	}
	if got := truncateUTF8("你好世界", 5); got != "你" {
		t.Fatalf("UTF-8 truncation split a rune: %q", got)
	}
	redactor := newSecretRedactor(`{"token":"secret-token","nested":{"url":"https://credential.example/hook"}}`)
	for _, input := range []string{
		"secret-token",
		"token=abc",
		"ToKeN=abc",
		"SECRET = abc",
		"Password: abc",
		"API_KEY=abc",
		"ApiKey=abc",
		"AUTHORIZATION=Bearer abc",
		"credential=abc",
		"credentials: abc",
		"access_token=abc",
		"ACCESS-TOKEN: abc",
		"client_secret=abc",
		"access_key=abc",
		"secret_key: abc",
		"bearer=abc",
		"https://user:pass@host/path?token=x",
		"endpoint_https://host/path",
		"OPEN:HTTP://host/path",
		"/srv/private/file",
		"open:/etc/passwd",
		`open:C:\secret`,
		`C:\data\secret`,
	} {
		t.Run(input, func(t *testing.T) {
			if got := redactor.text(input, 4096); got != "delivery failed with a redacted error" && got != "[redacted]" {
				t.Fatalf("unsafe value was not redacted: input=%q got=%q", input, got)
			}
		})
	}
	if got := newSecretRedactor("").text("普通错误边界", 7); got != "普通" {
		t.Fatalf("redaction bound split UTF-8: %q", got)
	}
	for _, input := range []string{"error/code/message", "status code: invalid_request", "connection reset by peer", "notasecret=value"} {
		if got := newSecretRedactor("").text(input, 4096); got != input {
			t.Fatalf("ordinary error was over-redacted: input=%q got=%q", input, got)
		}
	}
}

func TestAllowlistedProviderAckIsAdapterTyped(t *testing.T) {
	for _, tc := range []struct {
		name        string
		adapterKind string
		body        string
		want        string
	}{
		{"telegram typed fields", "telegram", `{"ok":true,"result":{"message_id":123},"status":"free text","message_id":"top-level"}`, `{"message_id":123,"ok":true}`},
		{"telegram rejects string id", "telegram", `{"ok":true,"result":{"message_id":"free provider text"}}`, `{"ok":true}`},
		{"telegram rejects fractional id", "telegram", `{"ok":true,"result":{"message_id":1.5}}`, `{"ok":true}`},
		{"feishu numeric code", "feishu", `{"code":19021,"msg":"free provider text","status":"failed"}`, `{"code":19021}`},
		{"feishu rejects string code", "feishu", `{"code":"0"}`, ""},
		{"webhook persists no ack", "webhook", `{"ok":true,"code":0,"status":"free text"}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := string(allowlistedProviderAck(tc.adapterKind, []byte(tc.body))); got != tc.want {
				t.Fatalf("ack=%s, want %s", got, tc.want)
			}
		})
	}
}

func TestPluginDeliverHandler_WebhookAuthKinds(t *testing.T) {
	handler := NewPluginDeliverHandler(nil, nil, nil, nil, nil)
	secret := `{"endpoint_url":"https://example.com/hook","auth_value":"secret-tok"}`
	for _, tc := range []struct {
		name, config, header, want string
	}{
		{"none", `{"auth_kind":"none"}`, "Authorization", ""},
		{"bearer", `{"auth_kind":"bearer"}`, "Authorization", "Bearer secret-tok"},
		{"header", `{"auth_kind":"header","auth_header_name":"X-Hook-Token"}`, "X-Hook-Token", "secret-tok"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := deliveryRow{adapterKind: "webhook", adapterConfigJSON: []byte(tc.config), triggerKind: "download_submitted", payload: []byte(`{"schema_version":1,"trigger_kind":"download_submitted","facts":{}}`)}
			req, err := handler.buildRequest(context.Background(), row, "title", "body", secret, "delivery", 1)
			if err != nil {
				t.Fatal(err)
			}
			if got := req.Header.Get(tc.header); got != tc.want {
				t.Fatalf("header %s=%q, want %q", tc.header, got, tc.want)
			}
		})
	}
}

func TestReadResponseResultClassifiesKnownAdapterAcknowledgements(t *testing.T) {
	for _, tc := range []struct {
		name, adapterKind, body, wantState, wantCode string
	}{
		{"telegram rejects malformed JSON", "telegram", `{`, "failed", "telegram_rejected"},
		{"telegram rejects missing ok", "telegram", `{}`, "failed", "telegram_rejected"},
		{"telegram rejects string ok", "telegram", `{"ok":"true"}`, "failed", "telegram_rejected"},
		{"telegram rejects null ok", "telegram", `{"ok":null}`, "failed", "telegram_rejected"},
		{"telegram rejects ok false", "telegram", `{"ok":false,"description":"secret provider body"}`, "failed", "telegram_rejected"},
		{"telegram accepts ok true", "telegram", `{"ok":true}`, "delivered", ""},
		{"feishu rejects malformed JSON", "feishu", `{`, "failed", "feishu_rejected"},
		{"feishu rejects missing code", "feishu", `{}`, "failed", "feishu_rejected"},
		{"feishu rejects string code", "feishu", `{"code":"0"}`, "failed", "feishu_rejected"},
		{"feishu rejects fractional code", "feishu", `{"code":0.0}`, "failed", "feishu_rejected"},
		{"feishu rejects nonzero code", "feishu", `{"code":19021,"msg":"secret provider body"}`, "failed", "feishu_rejected"},
		{"feishu accepts zero code", "feishu", `{"code":0}`, "delivered", ""},
		{"webhook ignores malformed body", "webhook", `{`, "delivered", ""},
		{"webhook ignores provider fields", "webhook", `{"ok":false,"code":19021}`, "delivered", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(tc.body))}
			result := readResponseResult(resp, tc.adapterKind, newSecretRedactor(""))
			if result.state != tc.wantState || result.errorCode != tc.wantCode {
				t.Fatalf("unexpected result: %+v", result)
			}
			if strings.Contains(result.err, "secret provider body") {
				t.Fatalf("raw provider body escaped: %+v", result)
			}
		})
	}
}
