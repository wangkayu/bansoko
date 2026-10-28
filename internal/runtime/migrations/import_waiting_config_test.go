package migrations

import (
	"context"
	"testing"
)

// 097 lets an import wait for configuration. A database upgraded with plans
// in every state keeps them as they were: no hold, both revisions zero, the
// state untouched. The new hold check then refuses a waiting plan without a
// hold.
func TestImportWaitingConfigMigrationKeepsPlansOfEveryState(t *testing.T) {
	ctx := context.Background()
	owner := openDisposableDB(t)
	installMigrationsThrough(t, owner, "096_seasonless_key_rewrite.sql")

	outcomes := map[string][2]any{
		"queued":    {nil, nil},
		"succeeded": {"plan_committed", "the plan committed its operations"},
		"failed":    {"plan_failed", "naming failed"},
	}
	for _, state := range []string{"queued", "succeeded", "failed"} {
		tag := "wait-" + state
		seedAcceptableRelease(t, owner, tag)
		p := validQueued(t, tag, tag+"-key", tag)
		if _, err := callQueuedAccept(t, owner, p); err != nil {
			t.Fatal(err)
		}
		for _, statement := range []struct {
			query string
			args  []any
		}{
			{`UPDATE download_tasks SET state = 'completed' WHERE id = $1`, []any{p.TaskID}},
			{`INSERT INTO artifacts(id, download_task_id, acquisition_id, inventory_snapshot, fingerprint_snapshot, state) VALUES ($1, $2, $3, '{}', '{}', 'pending')`,
				[]any{"artifact-" + tag, p.TaskID, p.AcquisitionID}},
			{`INSERT INTO postprocessing_plans(id, artifact_id, acquisition_id, artifact_version, input_digest, state, outcome_code, outcome_reason) VALUES ($1, $2, $3, 1, $4, $5, $6, $7)`,
				[]any{"plan-" + tag, "artifact-" + tag, p.AcquisitionID, p.AdmissionDigest, state, outcomes[state][0], outcomes[state][1]}},
		} {
			if _, err := owner.ExecContext(ctx, statement.query, statement.args...); err != nil {
				t.Fatalf("seed %s plan: %v", state, err)
			}
		}
	}

	if err := (Runner{DB: owner, Dir: migrationsDir(t)}).Apply(ctx); err != nil {
		t.Fatalf("upgrade: %v", err)
	}

	rows, err := owner.QueryContext(ctx, `
SELECT id, state, config_hold IS NULL AND config_hold_files IS NULL AND config_hold_since IS NULL
       AND config_hold_revision = 0 AND config_hold_logged_revision = 0
FROM postprocessing_plans ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := map[string]string{}
	for rows.Next() {
		var id, state string
		var untouched bool
		if err := rows.Scan(&id, &state, &untouched); err != nil {
			t.Fatal(err)
		}
		if !untouched {
			t.Errorf("plan %s carries a hold after the upgrade", id)
		}
		seen[id] = state
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for state := range outcomes {
		if seen["plan-wait-"+state] != state {
			t.Errorf("plan-wait-%s is %q after the upgrade", state, seen["plan-wait-"+state])
		}
	}

	_, err = owner.ExecContext(ctx, `UPDATE postprocessing_plans SET state = 'waiting_config' WHERE id = 'plan-wait-queued'`)
	expectBudgetCheck(t, "a waiting plan without a hold", err, "postprocessing_plans_config_hold_check")
}
