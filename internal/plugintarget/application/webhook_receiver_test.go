package application

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// referenceReceiver is the receiver the spec documents: it verifies the
// signature over "<timestamp>.<raw body>" with a constant-time compare,
// rejects a timestamp outside a five-minute window, and processes each
// delivery_id once, answering 2xx to a repeat.
type referenceReceiver struct {
	secret string
	now    func() time.Time

	mu        sync.Mutex
	processed map[string]int
	rejected  int
}

func (r *referenceReceiver) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	timestamp, err := strconv.ParseInt(req.Header.Get("X-Webhook-Timestamp"), 10, 64)
	if err != nil || r.now().Sub(time.Unix(timestamp, 0)).Abs() > 5*time.Minute {
		r.reject(w)
		return
	}
	mac := hmac.New(sha256.New, []byte(r.secret))
	mac.Write([]byte(strconv.FormatInt(timestamp, 10) + "."))
	mac.Write(body)
	expected := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(expected), []byte(req.Header.Get("X-Webhook-Signature"))) {
		r.reject(w)
		return
	}
	var doc struct {
		DeliveryID string `json:"delivery_id"`
	}
	if json.Unmarshal(body, &doc) != nil || doc.DeliveryID == "" || doc.DeliveryID != req.Header.Get("X-Webhook-Id") {
		r.reject(w)
		return
	}
	r.mu.Lock()
	r.processed[doc.DeliveryID]++
	r.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func (r *referenceReceiver) reject(w http.ResponseWriter) {
	r.mu.Lock()
	r.rejected++
	r.mu.Unlock()
	w.WriteHeader(http.StatusUnauthorized)
}

// receiverClient sends every request to the local TLS receiver whatever host
// the URL names, as a public endpoint would be reached.
func receiverClient(server *httptest.Server) *http.Client {
	client := server.Client()
	transport := client.Transport.(*http.Transport).Clone()
	address := server.Listener.Addr().String()
	transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}
	transport.TLSClientConfig.ServerName = "example.com"
	client.Transport = transport
	return client
}

func TestWebhookReachesAVerifyingReceiver(t *testing.T) {
	const secret = "whsec_receiver_0123456789"
	at := time.Unix(1767225600, 0)
	receiver := &referenceReceiver{secret: secret, now: func() time.Time { return at }, processed: map[string]int{}}
	server := httptest.NewUnstartedServer(receiver)
	server.StartTLS()
	defer server.Close()
	client := receiverClient(server)

	handler := NewPluginDeliverHandler(nil, nil, nil, nil, nil)
	handler.now = func() time.Time { return at }
	row := deliveryRow{
		adapterKind:       "webhook",
		adapterConfigJSON: []byte(`{"adapter_kind":"webhook","auth_kind":"none"}`),
		triggerKind:       "unknown_files_imported",
		payload:           []byte(`{"schema_version":1,"trigger_kind":"unknown_files_imported","facts":{"anime.title":"A","out.count":1,"out.files":[{"path":"A/extra.mkv","size":10}],"event.link":"#/?record=acq-1"}}`),
	}
	bundle := `{"endpoint_url":"https://example.com/hook","signing_secret":"` + secret + `"}`
	send := func(secretValue string, attempt int, tamper bool) int {
		t.Helper()
		req, err := handler.buildRequest(context.Background(), row, "T", "B", secretValue, "delivery-receiver-1", attempt)
		if err != nil {
			t.Fatalf("buildRequest: %v", err)
		}
		if tamper {
			body, _ := io.ReadAll(req.Body)
			changed := strings.Replace(string(body), `"A"`, `"B"`, 1)
			req.Body = io.NopCloser(strings.NewReader(changed))
			req.ContentLength = int64(len(changed))
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("send: %v", err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}

	if status := send(bundle, 1, false); status != http.StatusNoContent {
		t.Fatalf("first attempt status=%d, want 204", status)
	}
	// A retry of the same delivery verifies and is recognized as a repeat.
	if status := send(bundle, 2, false); status != http.StatusNoContent {
		t.Fatalf("retry status=%d, want 204", status)
	}
	if got := receiver.processed["delivery-receiver-1"]; got != 2 || len(receiver.processed) != 1 {
		t.Fatalf("receiver saw %v, want one delivery_id seen twice", receiver.processed)
	}
	// A changed body or a missing signature fails verification.
	if status := send(bundle, 3, true); status != http.StatusUnauthorized {
		t.Fatalf("tampered status=%d, want 401", status)
	}
	if status := send(`{"endpoint_url":"https://example.com/hook"}`, 4, false); status != http.StatusUnauthorized {
		t.Fatalf("unsigned status=%d, want 401", status)
	}
	// An old timestamp is rejected.
	handler.now = func() time.Time { return at.Add(-10 * time.Minute) }
	if status := send(bundle, 5, false); status != http.StatusUnauthorized {
		t.Fatalf("stale status=%d, want 401", status)
	}
	if receiver.rejected != 3 {
		t.Fatalf("rejected=%d, want 3", receiver.rejected)
	}
}
