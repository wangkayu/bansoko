package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"bansoko/internal/transport/httpapi/generated"
)

// An error body never carries a feed or torrent key, whichever owner built the
// message (a fetch error names its request URL). Synthetic key.
func TestWriteErrorRedactsFeedURLKeys(t *testing.T) {
	const key = "0123456789abcdef0123456789abcdef"
	recorder := httptest.NewRecorder()
	writeError(recorder, httptest.NewRequest(http.MethodPost, "/api/v1/catalog/custom/feed-snapshots:fetch", nil), &APIError{
		Status: http.StatusBadGateway, Code: "CUSTOM_FEED_FETCH_FAILED",
		Message: `fetch RSS source: Get "https://pt.example.test/torrentrss.php?passkey=` + key + `": timeout`,
		Details: []generated.ErrorDetail{{Code: "detail", Message: "https://t.example.test/torrents/download/7." + key}},
	})
	body := recorder.Body.String()
	if strings.Contains(body, key) || !strings.Contains(body, "passkey=REDACTED") || !strings.Contains(body, "/torrents/download/7.REDACTED") {
		t.Fatalf("error body keeps a key: %s", body)
	}
}
