package application

import (
	"bansoko/internal/deployment"
	"bansoko/internal/download/domain"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
	"context"
	"testing"
	"time"
)

// All classifications use the runtime connection; snapshots detect any owner,
// Release, WorkItem or evidence write. Provider objects are deliberately present
// even when there is no dispatch evidence: they are not deployment authority.
func TestReconcileSubmissionEvidenceReadOnly(t *testing.T) {
	for _, branch := range []string{"queued", "unexplained", "unknown", "confirmed"} {
		t.Run(branch, func(t *testing.T) {
			db := openDownloadDB(t)
			seedDownloadTask(t, db, "reconcile-task", "queued", "null")
			provider := &fakeProvider{lookupFound: true, lookupTask: domain.ProviderTask{ID: "object", State: domain.TaskDownloading}}
			service, store := newDownloadService(t, db, provider, &fakeRuntimeConfig{})
			permit := "reconcile-permit"
			var raw []byte
			var originalClaim workitems.Claim
			if branch == "unknown" || branch == "confirmed" {
				claim := createClaimedQBWorkItem(t, db, store, "reconcile-task", workitems.KindQBSubmit)
				originalClaim = claim
				if err := db.QueryRow(`SELECT download_start_submit_attempt($1,$2,$3,'reconcile-session')`, claim.Owner.ID, claim.ID, claim.FencingToken).Scan(&raw); err != nil {
					t.Fatal(err)
				}
				if err := db.QueryRow(`SELECT download_start_dispatch($1,$2,$3,'reconcile-session',$4,$5,'connection',$5,'{"key":"reconcile"}','{"remote_root":"/downloads","application_root":"/downloads","path_mappings":[]}','{"hash":"a"}')`, claim.Owner.ID, claim.ID, claim.FencingToken, permit, "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa").Scan(&raw); err != nil {
					t.Fatal(err)
				}
				if branch == "confirmed" {
					var root string
					if err := db.QueryRow(`SELECT acquisition_id FROM download_tasks WHERE id=$1`, claim.Owner.ID).Scan(&root); err != nil {
						t.Fatal(err)
					}
					if err := db.QueryRow(`SELECT download_record_submission_receipt($1,$2,'reconcile-session',$3,'confirmed','{"kind":"channel_closed"}','{"kind":"same_key_found"}','{"found":true}',NULL,$4,$5,NULL)`, root, permit, "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", claim.ID, claim.FencingToken).Scan(&raw); err != nil {
						t.Fatal(err)
					}
				}
			} else if branch == "unexplained" {
				if _, err := db.Exec(`UPDATE download_tasks SET state='submitting' WHERE id='reconcile-task'`); err != nil {
					t.Fatal(err)
				}
			}
			snapshot := func() string {
				var s string
				if err := db.QueryRow(`SELECT jsonb_build_array((SELECT jsonb_agg(to_jsonb(t) ORDER BY id) FROM download_tasks t),(SELECT jsonb_agg(to_jsonb(r) ORDER BY id) FROM releases r),(SELECT jsonb_agg(to_jsonb(w) ORDER BY id) FROM work_item_history w))::text`).Scan(&s); err != nil {
					t.Fatal(err)
				}
				return s
			}
			before := snapshot()
			result, err := service.ReconcileUnknownEffect(context.Background(), deployment.ReconcileRequest{})
			if err != nil {
				t.Fatal(err)
			}
			state, effect := deployment.StateCompleted, deployment.EffectNone
			if branch == "unexplained" {
				state, effect = deployment.StateBlocked, deployment.EffectUnknown
			}
			if branch == "unknown" {
				effect = deployment.EffectUnknown
				if result.EvidenceRef != permit {
					t.Fatalf("permit=%+v", result)
				}
			}
			if branch == "confirmed" {
				effect = deployment.EffectPresent
			}
			if result.State != state || result.Effect != effect {
				t.Fatalf("result=%+v", result)
			}
			if before != snapshot() {
				t.Fatal("deployment classification mutated persisted state")
			}
			if provider.lookupCalls != 0 || provider.syncCalls != 0 || provider.fileSubmitCalls != 0 {
				t.Fatalf("deployment queried provider: %+v", provider)
			}
			if branch == "unknown" {
				// Reclaim the original dispatched work, not the terminal acceptance fixture.
				if _, err := ownerTestDB(t).Exec(`UPDATE work_items SET recovery_blocked=true,recovery_reason='effect_unknown',lease_until=clock_timestamp()-INTERVAL '1 second' WHERE id=$1`, originalClaim.ID); err != nil {
					t.Fatal(err)
				}
				c, err := store.ClaimRecovery(context.Background(), []string{workitems.KindQBSubmit}, time.Minute)
				if err != nil {
					t.Fatal(err)
				}
				if c.ID != originalClaim.ID || c.FencingToken <= originalClaim.FencingToken {
					t.Fatalf("recovery did not reclaim dispatched work: original=%+v recovery=%+v", originalClaim, c)
				}
				provider.lookupTask = domain.ProviderTask{ID: "tag:object", Hash: "abc", State: domain.TaskDownloading, PathSnapshot: &domain.PathSnapshot{RemoteRoot: "/downloads", ApplicationRoot: "/downloads"}}
				outcome, err := (QBSubmitHandler{service: service}).ObserveRecovery(context.Background(), c)
				if err != nil || outcome.Kind != worker.OutcomeCommitted {
					t.Fatalf("worker recovery=%+v err=%v", outcome, err)
				}
				var effect string
				var lookups int
				if err := db.QueryRow(`SELECT submit_effect_class,submit_lookup_count FROM download_tasks WHERE submit_permit_id=$1`, permit).Scan(&effect, &lookups); err != nil {
					t.Fatal(err)
				}
				if effect != "confirmed" || lookups != 1 || provider.lookupCalls != 1 {
					t.Fatalf("T11 evidence=%s lookups=%d provider=%d", effect, lookups, provider.lookupCalls)
				}
			}
		})
	}
}
