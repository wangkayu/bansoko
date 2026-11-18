package application

import (
	"context"
	"encoding/json"
	"testing"

	plugintargetport "bansoko/internal/plugintarget/port"
	"bansoko/internal/testutil/acquisitionfixture"
	"bansoko/internal/testutil/disposabledb"
)

func TestNotificationSourceEvidenceFrozenAtOwnerBoundary(t *testing.T) {
	for _, trigger := range []string{"download_failed", "release_discovered", "replacement_applied"} {
		t.Run(trigger, func(t *testing.T) {
			db := openTestDB(t)
			service := newTestService(t, db)
			ctx := context.Background()
			createWorkflowTarget(t, service, "source", TriggerKind(trigger))
			owner := disposabledb.Owner(t)
			tx, err := owner.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			animeID, releaseID := seedWorkflowOwner(t, tx, "source")
			original := `{"channel":"jackett","provider":"jackett","guid":"entry-1","details_url":"https://tracker.example.test/details/1","download_url":"https://download.example.test/1.torrent","source_site":{"id":"sample","name":"Sample Tracker","type":"private"}}`
			if _, err := tx.ExecContext(ctx, `UPDATE releases SET internal_facts=$2::jsonb WHERE id=$1`, releaseID, original); err != nil {
				t.Fatal(err)
			}
			if trigger == "download_failed" {
				acquisitionfixture.SeedObservedDownload(t, tx, "root-source", "task-source", releaseID, "source-task", `{}`, `{}`, "downloading", `null`)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			mutate := func() {
				t.Helper()
				if _, err := owner.ExecContext(ctx, `UPDATE releases SET internal_facts='{"channel":"nyaa","details_url":"https://nyaa.si/view/2"}' WHERE id=$1`, releaseID); err != nil {
					t.Fatal(err)
				}
			}
			var admissionID string
			if trigger == "download_failed" {
				// Mutate before the event: the accepted acquisition, not the release, owns it.
				mutate()
				if _, err := owner.ExecContext(ctx, `UPDATE download_tasks SET state='failed',failure_code='DOWNLOAD_STALLED',version=version+1 WHERE id='task-source'`); err != nil {
					t.Fatal(err)
				}
				if err := db.QueryRowContext(ctx, `SELECT id FROM notification_admissions WHERE trigger_kind=$1`, trigger).Scan(&admissionID); err != nil {
					t.Fatal(err)
				}
			} else {
				request := plugintargetport.BusinessDeliveryRequest{
					SourceOwnerType: plugintargetport.OwnerRelease, SourceOwnerID: releaseID, SourceOwnerVersion: 1,
					ManagedAnimeID: animeID, TriggerKind: trigger, ResultKey: "source",
					Payload:        json.RawMessage(`{"release.id":"` + releaseID + `","anime.title":"Sample"}`),
					SourceEvidence: json.RawMessage(`{"provider":"custom"}`), // Never trusted from the caller.
				}
				if trigger == "replacement_applied" {
					request, err = plugintargetport.ReplacementAppliedRequest("decision-source", 1, animeID, "Sample", "Sample Group", "Sample Release", releaseID, []plugintargetport.DisplacedTarget{{File: "old.mkv"}}, plugintargetport.OriginalSource{New: plugintargetport.DisplacedTarget{File: "Sample.mkv"}})
					if err != nil {
						t.Fatal(err)
					}
				}
				admissionID = appendPendingAdmission(t, ctx, db, service, request)
				mutate()
				// Re-enqueue cannot refresh the evidence from the now changed release.
				if again := appendPendingAdmission(t, ctx, db, service, request); again != admissionID {
					t.Fatalf("replay admission changed: %s", again)
				}
			}
			var provider string
			if err := db.QueryRowContext(ctx, `SELECT source_evidence->>'provider' FROM notification_admissions WHERE id=$1`, admissionID).Scan(&provider); err != nil {
				t.Fatal(err)
			}
			if provider != "jackett" {
				t.Fatalf("frozen provider=%q", provider)
			}
			if _, err := owner.ExecContext(ctx, `UPDATE notification_admissions SET source_evidence='{}' WHERE id=$1`, admissionID); err == nil {
				t.Fatal("source evidence was mutable")
			}
			if deliveries := resolveWorkflowAdmissions(t, service, trigger); deliveries != 1 {
				t.Fatalf("deliveries=%d", deliveries)
			}
			var raw []byte
			var digest string
			if err := db.QueryRowContext(ctx, `SELECT payload,payload_digest FROM plugin_deliveries WHERE trigger_kind=$1`, trigger).Scan(&raw, &digest); err != nil {
				t.Fatal(err)
			}
			var payload struct {
				Facts map[string]any `json:"facts"`
			}
			if err := json.Unmarshal(raw, &payload); err != nil {
				t.Fatal(err)
			}
			facts := payload.Facts
			if facts["release.src"] != "jackett" || facts["release.site"] != "Sample Tracker" || facts["release.link"] != "https://tracker.example.test/details/1" || facts["download.link"] != "https://download.example.test/1.torrent" {
				t.Fatalf("delivery source=%v", facts)
			}
			if digest == "" {
				t.Fatal("missing frozen digest")
			}
			if deliveries := resolveWorkflowAdmissions(t, service, trigger); deliveries != 1 {
				t.Fatalf("resolver replay deliveries=%d", deliveries)
			}
			var replayRaw []byte
			var replayDigest string
			if err := db.QueryRowContext(ctx, `SELECT payload,payload_digest FROM plugin_deliveries WHERE trigger_kind=$1`, trigger).Scan(&replayRaw, &replayDigest); err != nil {
				t.Fatal(err)
			}
			if replayDigest != digest || string(replayRaw) != string(raw) {
				t.Fatal("resolver replay changed frozen delivery")
			}
		})
	}
}
