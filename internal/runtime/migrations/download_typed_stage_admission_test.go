package migrations

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"bansoko/internal/persistence/txscope"
	"bansoko/internal/runtime/database"
	"bansoko/internal/workitems"
)

func TestDownloadTypedStageAdmissionAtomicity(t *testing.T) {
	for _, name := range []string{"rollback", "commit", "removed_reference", "changed_epoch"} {
		rollback := name == "rollback"
		changed := name == "removed_reference" || name == "changed_epoch"
		t.Run(name, func(t *testing.T) {
			owner := installedDB(t)
			ctx := context.Background()
			seedAcceptableRelease(t, owner, "typed-admission")
			p := validQueued(t, "typed-admission", "typed-admission-key", "typed-admission")
			if _, err := callQueuedAccept(t, owner, p); err != nil {
				t.Fatal(err)
			}
			marker := "typed-marker"
			if _, err := owner.ExecContext(ctx, `INSERT INTO one_shot_acquisitions(id,managed_anime_id,subscription_id,rss_source_id,release_id,download_task_id,correlation_id,request_idempotency_key,request_hash,result_snapshot,state) SELECT $1,s.managed_anime_id,s.id,r.rss_source_id,r.id,$2,'typed-corr','typed-request',$3,'{}','running' FROM releases r JOIN rss_sources src ON src.id=r.rss_source_id JOIN subscriptions s ON s.id=src.subscription_id WHERE r.download_task_id=$2`, marker, p.TaskID, acqDigest("a")); err != nil {
				t.Fatal(err)
			}
			ref := workitems.OneShotRef{MarkerID: marker, CorrelationID: "typed-corr", AdmissionEpoch: 1}
			input, err := workitems.MergeOneShotInput(json.RawMessage(`{"trigger":"typed-test"}`), ref)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := owner.ExecContext(ctx, `UPDATE work_items SET input_snapshot=$2 WHERE id=$1`, p.SubmitWorkID, []byte(input)); err != nil {
				t.Fatal(err)
			}
			password := randomTestPassword(t)
			if err := database.ProvisionRuntimeRole(ctx, owner, database.RuntimeRoleConfig{Role: runtimeRoleTestName, Password: password}); err != nil {
				t.Fatal(err)
			}
			runtimeDB := openRuntimeConnection(t, password)
			defer runtimeDB.Close()
			store, err := workitems.NewStore(runtimeDB, 0, nil)
			if err != nil {
				t.Fatal(err)
			}
			claim, err := store.ClaimDue(ctx, "typed-stage", []string{workitems.KindQBSubmit}, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.AdmitOneShotEffect(ctx, claim); err != nil {
				t.Fatal(err)
			}
			if changed {
				patch := `input_snapshot-'one_shot'`
				if name == "changed_epoch" {
					patch = `jsonb_set(input_snapshot,'{one_shot,admission_epoch}','2')`
				}
				if _, err := owner.ExecContext(ctx, `UPDATE work_items SET input_snapshot=`+patch+` WHERE id=$1`, claim.ID); err != nil {
					t.Fatal(err)
				}
			}
			sentinel := errors.New("rollback typed stage")
			err = store.WithTypedStageTransaction(ctx, claim, func(scope txscope.Tx) (workitems.EffectDisposition, error) {
				if _, err := scope.SQLTx().ExecContext(ctx, `SELECT download_start_submit_attempt($1,$2,$3,'typed-admission-session')`, p.TaskID, claim.ID, claim.FencingToken); err != nil {
					return "", err
				}
				if _, err := scope.SQLTx().ExecContext(ctx, `SELECT work_item_finish('succeeded',1,$1)`, claim.ID); err != nil {
					return "", err
				}
				if rollback {
					return workitems.DispositionConfirmed, sentinel
				}
				return workitems.DispositionConfirmed, nil
			})
			if rollback && !errors.Is(err, sentinel) {
				t.Fatal(err)
			}
			if changed && !errors.Is(err, workitems.ErrOneShotReclaiming) {
				t.Fatalf("changed persisted one-shot reference error=%v", err)
			}
			if !changed && !rollback && err != nil {
				t.Fatal(err)
			}
			var state string
			var count int
			if err := runtimeDB.QueryRowContext(ctx, `SELECT w.state,m.active_effect_count FROM work_item_history w CROSS JOIN one_shot_acquisitions m WHERE w.id=$1 AND m.id=$2`, claim.ID, marker).Scan(&state, &count); err != nil {
				t.Fatal(err)
			}
			if changed || rollback {
				if state != "leased" || count != 1 {
					t.Fatalf("rejected transaction state=%s count=%d", state, count)
				}
			} else if state != "succeeded" || count != 0 {
				t.Fatalf("committed transaction state=%s count=%d", state, count)
			}
		})
	}
}
