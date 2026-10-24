package application

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// hashTestRequest produces the stable request hash stored on the test
// idempotency row and the test delivery. It is derived from the canonical
// request body so a repeated identical request replays the original delivery.
func hashTestRequest(body json.RawMessage) string {
	return hashBytes(body)
}

func hashBytes(raw []byte) string {
	digest := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(digest[:])
}
