package migrations

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	animepersistence "bansoko/internal/anime/adapter/persistence"
	ownership "bansoko/internal/anime/ownership/application"
	observability "bansoko/internal/observability/application"
	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/workitems"
)

// Exercise the existing retirement body: lifecycle first, then unadmitted
// WorkItems, then lifecycle advancement. Never substitute a lifecycle-only
// update, which would omit the competing WorkItem edge.
func TestAnimeRetirementVersusInspectionActivityBothOrders(t *testing.T) {
	for _, first := range []string{"retirement", "inspection"} {
		t.Run(first, func(t *testing.T) {
			for round := 0; round < 50; round++ {
				t.Run(fmt.Sprintf("round-%02d", round), func(t *testing.T) {
					owner, db, p := progressProbe(t, fmt.Sprintf("inspection-race-%s-%d", first, round), nil, nil)
					ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
					defer cancel()
					if _, err := owner.ExecContext(ctx, `UPDATE download_tasks SET state='completed' WHERE id=$1`, p.TaskID); err != nil {
						t.Fatal(err)
					}
					if _, err := owner.ExecContext(ctx, `INSERT INTO artifacts(id,download_task_id,acquisition_id,inventory_snapshot,fingerprint_snapshot,state) VALUES('race-artifact',$1,$2,'{}','{}','pending')`, p.TaskID, p.AcquisitionID); err != nil {
						t.Fatal(err)
					}
					var id string
					if err := db.QueryRowContext(ctx, `SELECT public.work_item_create('race-inspect','artifact','race-artifact','artifact_inspect','race-inspect',3,'{}',1,0::double precision,7::smallint)`).Scan(&id); err != nil {
						t.Fatal(err)
					}
					store := &workitems.Store{DB: db, RetentionDays: 7}
					claim, err := store.ClaimDue(ctx, "race", []string{workitems.KindArtifactInspect}, time.Minute)
					if err != nil {
						t.Fatal(err)
					}
					var anime, subscription string
					if err := owner.QueryRowContext(ctx, `SELECT sub.managed_anime_id,sub.id FROM subscriptions sub JOIN rss_sources src ON src.subscription_id=sub.id JOIN releases r ON r.rss_source_id=src.id WHERE r.id=$1`, p.SourceReleaseID).Scan(&anime, &subscription); err != nil {
						t.Fatal(err)
					}
					if _, err := owner.ExecContext(ctx, `INSERT INTO anime_config_candidates(id,origin,origin_entry_ref,title,instance_label,draft,activated_managed_anime_id,status) VALUES('race-candidate','mikan','race-entry','race','race','{"origin":"mikan","origin_entry_ref":"race-entry","title":"race","instance_label":"race"}',$1,'dormant')`, anime); err != nil {
						t.Fatal(err)
					}
					if _, err := owner.ExecContext(ctx, `INSERT INTO media_fact_snapshots(id,release_id,schema_version,resolver_version,parser_kind,input_digest,release_identity,release_facts) VALUES('race-media',$1,'media-facts-v1','test','regex','sha256:input','{}','{"media_kind":"movie","season":0}')`, p.SourceReleaseID); err != nil {
						t.Fatal(err)
					}
					var mediaBinding json.RawMessage
					if err := owner.QueryRowContext(ctx, `SELECT jsonb_build_object('release_facts',release_facts,'release_identity',release_identity,'parser_kind',parser_kind,'parser_config',parser_config,'parser_config_fingerprint',parser_config_fingerprint) FROM media_fact_snapshots WHERE id='race-media'`).Scan(&mediaBinding); err != nil {
						t.Fatal(err)
					}
					var subscriptionVersion int64
					if err := owner.QueryRowContext(ctx, `SELECT version FROM subscriptions WHERE id=$1`, subscription).Scan(&subscriptionVersion); err != nil {
						t.Fatal(err)
					}
					retirementService, err := ownership.NewService(db, ownership.Dependencies{WorkItems: store, OneShotMarkers: animepersistence.NewRepository(db)})
					if err != nil {
						t.Fatal(err)
					}
					retireApplication := func() error {
						return retirementService.DeleteAnimeSubscription(ctx, anime, ownership.SubscriptionDeleteInput{ExpectedVersion: subscriptionVersion})
					}
					var manifest []map[string]any
					if err := json.Unmarshal([]byte(p.ManifestFiles), &manifest); err != nil {
						t.Fatal(err)
					}
					files := make([]map[string]any, 0, len(manifest))
					for i, f := range manifest {
						files = append(files, map[string]any{"id": fmt.Sprintf("race-file-%d", i), "file_ref": f["file_ref"], "relative_path": f["relative_path"], "source_path": "/downloads/" + f["relative_path"].(string), "size_bytes": f["exact_size"], "media_kind": f["media_kind"], "fingerprint": map[string]any{"algorithm": "sha256", "value": "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"}, "facts": map[string]any{}})
					}
					body, err := json.Marshal(files)
					if err != nil {
						t.Fatal(err)
					}
					activity, err := observability.NewService(db)
					if err != nil {
						t.Fatal(err)
					}
					inspectTx, err := db.BeginTx(ctx, nil)
					if err != nil {
						t.Fatal(err)
					}
					defer inspectTx.Rollback()
					retireTx, err := db.BeginTx(ctx, nil)
					if err != nil {
						t.Fatal(err)
					}
					defer retireTx.Rollback()
					var inspectPID, retirePID int
					if err := inspectTx.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&inspectPID); err != nil {
						t.Fatal(err)
					}
					if err := retireTx.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&retirePID); err != nil {
						t.Fatal(err)
					}
					inventory, fingerprints := inspectionSnapshotFixture(t, files)
					mediaFiles := make([]map[string]any, 0, len(files))
					for _, f := range files {
						mediaFiles = append(mediaFiles, map[string]any{"file_ref": f["file_ref"], "media_kind": f["media_kind"], "size_bytes": f["size_bytes"], "source_fingerprint": "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", "file_evidence": map[string]any{}, "effective_facts": map[string]any{}, "snapshot_binding": mediaBinding})
					}
					mediaRaw, err := json.Marshal(mediaFiles)
					if err != nil {
						t.Fatal(err)
					}
					inspect := func() error {
						var raw []byte
						if err := inspectTx.QueryRowContext(ctx, `SELECT public.artifact_commit_inspection($1,$2,$3,$4,$5,$6::jsonb,$9::jsonb,$10::jsonb,$7::jsonb,'race-plan','race-plan-work',$8,'race-media',$11::jsonb)`, claim.Owner.ID, claim.ID, claim.FencingToken, p.AdmissionDigest, p.ManifestDigest, p.ManifestFiles, body, p.AdmissionDigest, inventory, fingerprints, mediaRaw).Scan(&raw); err != nil {
							return err
						}
						// Row locks are usually represented in the tuple's MultiXact rather than
						// pg_locks. A competing FOR UPDATE must block and pg_locks must expose its
						// ungranted transactionid lock before AppendTx is invoked.
						probe, err := owner.BeginTx(ctx, nil)
						if err != nil {
							return err
						}
						defer probe.Rollback()
						var probePID int
						if err := probe.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&probePID); err != nil {
							return err
						}
						probeResult := make(chan error, 1)
						go func() {
							_, err := probe.ExecContext(ctx, `SELECT 1 FROM managed_anime WHERE id=$1 FOR UPDATE`, anime)
							probeResult <- err
						}()
						if err := waitInspectionBlocked(ctx, owner, probePID, inspectPID); err != nil {
							_ = probe.Rollback()
							<-probeResult
							return err
						}
						// Release the probe by cancelling just this backend's statement.
						if _, err := owner.ExecContext(ctx, `SELECT pg_cancel_backend($1)`, probePID); err != nil {
							return err
						}
						<-probeResult
						return activity.AppendTx(ctx, txscope.Wrap(inspectTx), observabilityport.ActivityRecord{SubscriptionID: subscription, ManagedAnimeID: anime, Kind: "artifact_verified", Outcome: "success", ReasonCode: "verified", SubjectType: "artifact", SubjectID: claim.Owner.ID, SubjectVersion: 2, StableResultKey: "verified", Title: "媒体文件核验完成", Summary: "媒体文件核验完成", CorrelationID: "corr-" + p.AcquisitionID, CausationType: "download_task", CausationID: p.TaskID})
					}
					lockRetire := func() (int64, error) {
						var epoch int64
						err := retireTx.QueryRowContext(ctx, `SELECT fence_epoch FROM managed_anime WHERE id=$1 FOR UPDATE`, anime).Scan(&epoch)
						return epoch, err
					}
					retire := func(epoch int64) error {
						if err := workitems.RetireUnadmittedTx(ctx, retireTx, anime, epoch); err != nil {
							return err
						}
						_, err := retireTx.ExecContext(ctx, `UPDATE managed_anime SET lifecycle_state='retired',fence_epoch=fence_epoch+1,retired_at=CURRENT_TIMESTAMP,retired_subscription_version=1,lifecycle_version=lifecycle_version+1 WHERE id=$1`, anime)
						return err
					}
					if first == "retirement" {
						epoch, err := lockRetire()
						if err != nil {
							t.Fatal(err)
						}
						done := make(chan error, 1)
						go func() { done <- inspect() }()
						if err := waitInspectionBlocked(ctx, owner, inspectPID, retirePID); err != nil {
							_ = retireTx.Rollback()
							<-done
							t.Fatal(err)
						}
						retireErr := retire(epoch)
						if !errors.Is(retireErr, workitems.ErrRetirementContention) {
							_ = retireTx.Rollback()
							<-done
							t.Fatalf("retirement expected NOWAIT contention: %v", retireErr)
						}
						if err := retireTx.Rollback(); err != nil {
							t.Fatal(err)
						}
						if err := <-done; err != nil {
							t.Fatalf("inspection after retirement rollback: %v", err)
						}
						if err := inspectTx.Commit(); err != nil {
							t.Fatal(err)
						}
						if err := retireApplication(); err != nil {
							t.Fatal(err)
						}
					} else {
						if err := inspect(); err != nil {
							t.Fatal(err)
						}
						done := make(chan error, 1)
						go func() { done <- retireApplication() }()
						if err := waitRetirementApplicationBlocked(ctx, owner, inspectPID); err != nil {
							_ = inspectTx.Rollback()
							<-done
							t.Fatal(err)
						}
						if err := inspectTx.Commit(); err != nil {
							t.Fatal(err)
						}
						if err := <-done; err != nil {
							t.Fatal(err)
						}
					}
					if err := retireApplication(); err != nil {
						t.Fatalf("retirement idempotence: %v", err)
					}
					var live int
					if err := owner.QueryRowContext(ctx, `SELECT count(*) FROM work_items WHERE managed_anime_id=$1 AND state IN ('available','leased') AND external_effect_admitted_at IS NULL`, anime).Scan(&live); err != nil {
						t.Fatal(err)
					}
					if live != 0 {
						t.Fatalf("retirement missed work=%d", live)
					}
					var archived bool
					var mediaCount, plans int
					if err := owner.QueryRowContext(ctx, `SELECT status='archived' AND activated_managed_anime_id IS NULL AND cleanup_reason='provider_owner_retired' FROM anime_config_candidates WHERE id='race-candidate'`).Scan(&archived); err != nil {
						t.Fatal(err)
					}
					if err := owner.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM media_fact_files WHERE snapshot_id='race-media'),(SELECT count(*) FROM postprocessing_plans WHERE acquisition_id=$1)`, p.AcquisitionID).Scan(&mediaCount, &plans); err != nil {
						t.Fatal(err)
					}
					if !archived || mediaCount != len(files) || plans != 1 {
						t.Fatalf("incomplete retirement/inspection graph archived=%t media=%d plans=%d", archived, mediaCount, plans)
					}
					var verified, logged bool
					if err := owner.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM artifacts WHERE id='race-artifact' AND state='verified'),EXISTS(SELECT 1 FROM subscription_log_entries WHERE subject_id='race-artifact')`).Scan(&verified, &logged); err != nil {
						t.Fatal(err)
					}
					if verified != logged || !verified {
						t.Fatalf("partial commit verified=%t activity=%t", verified, logged)
					}
				})
				if t.Failed() {
					return
				}
			}
		})
		if t.Failed() {
			return
		}
	}
}

func waitRetirementApplicationBlocked(ctx context.Context, db *sql.DB, blocker int) error {
	for {
		var blocked bool
		err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity a WHERE $1=ANY(pg_blocking_pids(a.pid)) AND a.query LIKE '%FROM managed_anime%FOR UPDATE%' AND EXISTS(SELECT 1 FROM pg_locks l WHERE l.pid=a.pid AND NOT l.granted AND l.locktype='transactionid'))`, blocker).Scan(&blocked)
		if err != nil {
			return err
		}
		if blocked {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func waitInspectionBlocked(ctx context.Context, db *sql.DB, waiter, blocker int) error {
	for {
		var blocked bool
		err := db.QueryRowContext(ctx, `SELECT $2=ANY(pg_blocking_pids($1)) AND EXISTS(SELECT 1 FROM pg_locks WHERE pid=$1 AND NOT granted AND locktype='transactionid')`, waiter, blocker).Scan(&blocked)
		if err != nil {
			return err
		}
		if blocked {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func inspectionSQLState(err error) string {
	type sqlStater interface{ SQLState() string }
	if e, ok := err.(sqlStater); ok {
		return e.SQLState()
	}
	return ""
}
