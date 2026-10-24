package migrations

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"bansoko/internal/runtime/database"
	"bansoko/internal/testutil/disposabledb/guard"
	"bansoko/internal/workitems"
	"os"
)

func TestFileAttemptBudget(t *testing.T) {
	runPlanAtomicCommit(t, false, false, false)
	owner := guard.MustDisposable(t)
	db, err := database.Open(context.Background(), database.Config{DSN: os.Getenv("BANSOKO_TEST_RUNTIME_DATABASE_URL")})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := workitems.NewStore(db, 5, nil)
	if err != nil {
		t.Fatal(err)
	}
	c, err := store.ClaimDue(context.Background(), "file-budget", []string{workitems.KindFileExecute}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err := db.QueryRow(`SELECT public.file_start_attempt($1,$2,$3,'session-one')`, c.Owner.ID, c.ID, c.FencingToken).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var receipt struct {
		Granted bool           `json:"granted"`
		Ticket  map[string]any `json:"ticket"`
	}
	if err := json.Unmarshal(raw, &receipt); err != nil {
		t.Fatal(err)
	}
	if !receipt.Granted || receipt.Ticket["ordinal"] != float64(1) {
		t.Fatalf("first grant %s", raw)
	}
	before := graphSnapshot(t, owner)
	if err := db.QueryRow(`SELECT public.file_start_attempt($1,$2,$3,'different-session')`, c.Owner.ID, c.ID, c.FencingToken).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.Granted || receipt.Ticket["executor_session"] != "session-one" {
		t.Fatalf("repeat granted %s", raw)
	}
	if string(before) != string(graphSnapshot(t, owner)) {
		t.Fatal("repeat changed graph")
	}
	for _, sql := range []string{
		`UPDATE public.file_operations SET file_attempts_started=0,file_attempt_ticket=NULL,file_attempt_index='[]' WHERE id=$1`,
		`UPDATE public.file_operations SET file_wall_until=file_wall_until+interval '1 second' WHERE id=$1`,
		`UPDATE public.file_operations SET file_attempt_ticket=jsonb_set(file_attempt_ticket,'{executor_session}','"other"') WHERE id=$1`,
	} {
		if _, err := owner.Exec(sql, c.Owner.ID); err == nil {
			t.Fatalf("budget mutation accepted: %s", sql)
		}
	}
	// Owner arranges only the relational permit. This is a DB grant test,
	// not proof that production preparation or filesystem execution ran.
	checkpoint := map[string]any{}
	for _, step := range []string{"stage_temp", "reserve_version", "publish_target", "source_cleanup", "temp_cleanup"} {
		state := "not_required"
		if step == "stage_temp" || step == "publish_target" || step == "temp_cleanup" {
			state = "not_started"
		}
		checkpoint[step] = map[string]any{"state": state, "proof": nil, "observed_at": nil, "reason_code": nil}
	}
	cp, _ := json.Marshal(checkpoint)
	if _, err := owner.Exec(`UPDATE public.file_operations o SET
	 publication_permit_id='dispatch-permit',
	 publication_attempt=o.file_attempts_started,
	 publication_evidence_version=1,
	 publication_effect_key=o.idempotency_key,
	 publication_spec_digest=t.spec_digest,
	 publication_plan_digest=p.plan_digest,
	 publication_dependency_digest='sha256:'||repeat('0',64),
	 publication_executor_session='session-one',
	 publication_work_item_id=$2,
	 publication_fencing_token=$3,
	 publication_owner_version=o.version,
	 publication_prepared_at=CURRENT_TIMESTAMP,
	 publication_frozen_objects='{}',
	 publication_step_checkpoint=$4::jsonb,
	 publication_step_proof_bytes='{}',
	 publication_reconcile_wall_seconds=60,
	 publication_max_lookups=2,
	 publication_dispatch_state='prepared',
	 publication_effect_class='unknown'
	FROM public.postprocessing_plans p, public.acquisition_files t
	WHERE o.id=$1 AND p.id=o.postprocessing_plan_id AND t.acquisition_id=o.acquisition_id AND t.file_ref=o.file_ref`, c.Owner.ID, c.ID, c.FencingToken, string(cp)); err != nil {
		t.Fatal(err)
	}
	for ordinal, step := range []string{"dispatch", "stage_temp", "stage_temp", "dispatch"} {
		beforeGrant := graphSnapshot(t, owner)
		query := `SELECT public.file_start_step($1,$2,$3,'dispatch-permit','session-one',$4)`
		args := []any{c.Owner.ID, c.ID, c.FencingToken, step}
		if step == "dispatch" {
			query = `SELECT public.file_start_dispatch($1,$2,$3,'dispatch-permit','session-one')`
			args = args[:3]
		}
		if err := db.QueryRow(query, args...).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var g struct {
			Granted bool `json:"granted"`
		}
		if err := json.Unmarshal(raw, &g); err != nil {
			t.Fatal(err)
		}
		want := ordinal < 2
		if !want && string(beforeGrant) != string(graphSnapshot(t, owner)) {
			t.Fatal("duplicate dispatch/step changed graph")
		}
		if g.Granted != want {
			t.Fatalf("step=%s grant=%s", step, raw)
		}
	}
	if err := db.QueryRow(`SELECT public.file_start_step($1,$2,$3,'dispatch-permit','session-one','publish_target')`, c.Owner.ID, c.ID, c.FencingToken).Scan(&raw); err == nil {
		t.Fatal("publication before staging proof accepted")
	}
	if _, err := owner.Exec(`UPDATE public.work_items SET cancel_requested=true WHERE id=$1`, c.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT public.file_start_attempt($1,$2,$3,'session-one')`, c.Owner.ID, c.ID, c.FencingToken).Scan(&raw); err == nil {
		t.Fatal("cancelled gate accepted")
	}
	var consumed int
	if err := owner.QueryRow(`SELECT file_attempts_started FROM public.file_operations WHERE id=$1`, c.Owner.ID).Scan(&consumed); err != nil {
		t.Fatal(err)
	}
	if consumed != 1 {
		t.Fatalf("consumed=%d", consumed)
	}
}
