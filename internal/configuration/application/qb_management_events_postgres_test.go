package application

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"bansoko/internal/apperrors"
	observabilityapplication "bansoko/internal/observability/application"
)

func TestQBManagementDeleteOperationEventsPostgres(t *testing.T) {
	ctx := context.Background()
	const hash = "1234567890abcdef1234567890abcdef12345678"
	for _, tc := range []struct {
		name, errorCode, message, outcome string
		removeData                        bool
	}{
		{"confirmed", "", "qb_task_deleted", "success", false},
		{"unconfirmed", "QB_DELETION_UNCONFIRMED", "qb_deletion_unconfirmed", "failure", true},
		{"delete-response-lost", "QB_DELETION_UNCONFIRMED", "qb_deletion_unconfirmed", "failure", true},
		{"tags-failed", "QB_DELETE_TAG_CLEANUP_FAILED", "qb_delete_tag_cleanup_failed", "failure", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openConnectionTestDB(t)
			writer, err := observabilityapplication.NewService(db)
			if err != nil {
				t.Fatal(err)
			}
			management := NewQBManagementService(NewService(db, newConnectionTestSecretStore(t, db)), nil).WithOperationEvents(writer, nil, nil)
			var deletes atomic.Int32
			qb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v2/auth/login": // qBittorrent WebUI login
					_, _ = w.Write([]byte("Ok."))
				case "/api/v2/torrents/info":
					if deletes.Load() == 0 {
						_, _ = w.Write([]byte(`[{"hash":"` + hash + `","name":"Example task","tags":"bansoko-qb:release-example"}]`))
					} else if tc.name == "unconfirmed" {
						http.Error(w, "unavailable", http.StatusServiceUnavailable)
					} else {
						_, _ = w.Write([]byte(`[]`))
					}
				case "/api/v2/torrents/delete":
					deletes.Add(1)
					if tc.name == "delete-response-lost" {
						panic(http.ErrAbortHandler)
					}
				case "/api/v2/torrents/deleteTags":
					if tc.name == "tags-failed" {
						http.Error(w, "unavailable", http.StatusServiceUnavailable)
					}
				default:
					http.NotFound(w, r)
				}
			}))
			defer qb.Close()
			id := "connection-test-qb-event-" + tc.name
			newQBManagementTorrentConnection(t, ctx, db, id, qb.URL)
			err = management.DeleteTorrent(ctx, id, hash, tc.removeData)
			var refusal *apperrors.BadRequest
			if tc.errorCode == "" && err != nil || tc.errorCode != "" && (!errors.As(err, &refusal) || refusal.Code != tc.errorCode) {
				t.Fatalf("delete returned %v, want %q", err, tc.errorCode)
			}
			if deletes.Load() != 1 {
				t.Fatalf("expected one delete request, got %d", deletes.Load())
			}
			if tc.name == "confirmed" {
				if err := management.DeleteTorrent(ctx, id, hash, tc.removeData); err != nil || deletes.Load() != 1 {
					t.Fatalf("already absent repeated the deletion: sends=%d, err=%v", deletes.Load(), err)
				}
			}
			var message, outcome, display, subject string
			var requested bool
			var count int
			if err := db.QueryRowContext(ctx, `
SELECT count(*) OVER (), message_code, outcome, subject_display, subject_id, (details->>'data_delete_requested')::boolean
FROM subscription_log_entries WHERE kind = 'qb_task_deleted' AND causation_id = $1`, id).
				Scan(&count, &message, &outcome, &display, &subject, &requested); err != nil {
				t.Fatal(err)
			}
			if count != 1 || message != tc.message || outcome != tc.outcome || display != "Example task" || subject != hash || requested != tc.removeData {
				t.Fatalf("event count=%d message=%q outcome=%q display=%q subject=%q requested=%v", count, message, outcome, display, subject, requested)
			}
		})
	}
}
