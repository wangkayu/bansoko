package application

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

	"bansoko/internal/templatefacts"
)

// WebhookEnvelopeVersion is the generic webhook document version. There is a
// single version and no history: the frozen facts are carried under the unified
// fact paths, nested by path segment.
const WebhookEnvelopeVersion = "1"

// Webhook delivery headers. They carry no product name.
const (
	// HeaderWebhookID is the delivery_id, stable across every attempt of one
	// delivery; a receiver deduplicates on it.
	HeaderWebhookID = "X-Webhook-Id"
	// HeaderWebhookTimestamp is the attempt time in unix seconds.
	HeaderWebhookTimestamp = "X-Webhook-Timestamp"
	// HeaderWebhookSignature is "sha256=" + hex(HMAC-SHA256(secret,
	// timestamp + "." + raw body)); it is sent only when the target has a
	// signing secret.
	HeaderWebhookSignature = "X-Webhook-Signature"
)

// webhookEnvelope is the version 1 document. Field order is fixed by the
// struct so the signed bytes are deterministic for one attempt.
type webhookEnvelope struct {
	Version    string            `json:"version"`
	DeliveryID string            `json:"delivery_id"`
	Event      string            `json:"event"`
	Timestamp  string            `json:"timestamp"`
	Attempt    int               `json:"attempt"`
	Message    map[string]string `json:"message"`
	Payload    map[string]any    `json:"payload"`
}

// buildWebhookEnvelope returns the version 1 body of one webhook attempt.
// frozenPayload is the delivery's canonical payload; its facts are stored
// under the registry fact paths, and only registry facts and frozen vars are
// kept, so the document never carries a bookkeeping id or a key the registry
// does not describe.
func buildWebhookEnvelope(deliveryID, trigger string, attempt int, at time.Time, title, body string, frozenPayload []byte) ([]byte, error) {
	var canonical struct {
		Facts map[string]any `json:"facts"`
	}
	decoder := json.NewDecoder(bytes.NewReader(frozenPayload))
	decoder.UseNumber()
	if err := decoder.Decode(&canonical); err != nil {
		return nil, errors.New("webhook payload could not be decoded")
	}
	envelope := webhookEnvelope{
		Version: WebhookEnvelopeVersion, DeliveryID: deliveryID, Event: trigger,
		Timestamp: at.UTC().Format(time.RFC3339), Attempt: attempt,
		Message: map[string]string{"title": title, "body": body},
		Payload: nestUnifiedFacts(canonical.Facts),
	}
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(envelope); err != nil {
		return nil, errors.New("webhook document could not be encoded")
	}
	return bytes.TrimRight(out.Bytes(), "\n"), nil
}

// nestUnifiedFacts keeps the registry facts and vars.<group>.<name> values of
// a unified fact map and nests them by path segment: {"anime": {"title": …}}.
func nestUnifiedFacts(facts map[string]any) map[string]any {
	keys := make([]string, 0, len(facts))
	for key := range facts {
		if _, registered := templatefacts.Lookup(key); registered || isVarsPath(key) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	out := map[string]any{}
	for _, key := range keys {
		segments := strings.Split(key, ".")
		node := out
		ok := true
		for _, segment := range segments[:len(segments)-1] {
			child, exists := node[segment]
			if !exists {
				next := map[string]any{}
				node[segment] = next
				node = next
				continue
			}
			next, isObject := child.(map[string]any)
			if !isObject {
				ok = false
				break
			}
			node = next
		}
		if ok {
			if _, taken := node[segments[len(segments)-1]].(map[string]any); !taken {
				node[segments[len(segments)-1]] = facts[key]
			}
		}
	}
	return out
}

func isVarsPath(key string) bool {
	segments := strings.Split(key, ".")
	return len(segments) == 3 && segments[0] == templatefacts.RootVars && segments[1] != "" && segments[2] != ""
}

// SignWebhook returns the X-Webhook-Signature value for one attempt:
// "sha256=" + lowercase hex HMAC-SHA256 over "<timestamp>.<raw body>".
func SignWebhook(secret string, timestamp int64, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(strconv.FormatInt(timestamp, 10)))
	mac.Write([]byte("."))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}
