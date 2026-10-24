package migrations

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
)

// The four inspect/plan budget columns carry a DDL DEFAULT and the BEFORE
// INSERT guard completes any NULL, so a writer that passes no budget still
// satisfies the shape CHECK, while explicit values stay bounded by that CHECK. The numeric source is
// worker.DefaultPolicies() (attempts/timeout) and productionLimits()'
// wall_seconds=86400; the DDL DEFAULTs are compared against the Go policy
// registry here, never against a fixture constant.

// budgetWallSeconds mirrors productionLimits() wall_seconds (production_input.go).
// It is the one literal the SQL DEFAULT and the Go limits share; the matching
// Go-side tripwire is TestProductionLimitsMatchRuntimePolicy.
const budgetWallSeconds = 86400

func stagePolicy(t *testing.T, kind string) worker.KindPolicy {
	t.Helper()
	for _, p := range worker.DefaultPolicies() {
		if p.Kind == kind {
			return p
		}
	}
	t.Fatalf("no KindPolicy for %s", kind)
	return worker.KindPolicy{}
}

// seedBudgetOwners arranges one accepted acquisition with a bare
// (budget-less) artifact INSERT and a queued plan created by the real
// artifact_commit_inspection path. It returns the plan id.
func seedBudgetOwners(t *testing.T, tag string, limitsPatch map[string]any) (*sql.DB, *sql.DB, queuedParams, string) {
	t.Helper()
	owner, db, p := progressProbe(t, tag, limitsPatch, nil)
	ctx := context.Background()
	if _, err := owner.ExecContext(ctx, `UPDATE download_tasks SET state='completed' WHERE id=$1`, p.TaskID); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.ExecContext(ctx, `INSERT INTO artifacts(id,download_task_id,acquisition_id,inventory_snapshot,fingerprint_snapshot,state) VALUES('plan-artifact',$1,$2,'{}','{}','pending')`, p.TaskID, p.AcquisitionID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT public.work_item_create('plan-inspect','artifact','plan-artifact','artifact_inspect','plan-inspect',3,'{}',1,0::double precision,7::smallint)`).Scan(new(string)); err != nil {
		t.Fatal(err)
	}
	store := &workitems.Store{DB: db, RetentionDays: 7}
	c, err := store.ClaimDue(ctx, "plan-inspect", []string{workitems.KindArtifactInspect}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	var manifest []map[string]any
	if err := json.Unmarshal([]byte(p.ManifestFiles), &manifest); err != nil {
		t.Fatal(err)
	}
	files := []map[string]any{}
	for _, f := range manifest {
		files = append(files, map[string]any{"id": "plan-file-" + f["file_ref"].(string), "file_ref": f["file_ref"], "relative_path": f["relative_path"], "source_path": "/downloads/" + f["relative_path"].(string), "size_bytes": f["exact_size"], "media_kind": f["media_kind"], "fingerprint": map[string]any{"algorithm": "sha256", "value": "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"}, "facts": map[string]any{}})
	}
	body, err := json.Marshal(files)
	if err != nil {
		t.Fatal(err)
	}
	inventory, fingerprints := inspectionSnapshotFixture(t, files)
	if err := db.QueryRowContext(ctx, `SELECT public.artifact_commit_inspection($1,$2,$3,$4,$5,$6::jsonb,$9::jsonb,$10::jsonb,$7::jsonb,'budget-plan','budget-plan-work',$8,'','[]')`, c.Owner.ID, c.ID, c.FencingToken, p.AdmissionDigest, p.ManifestDigest, p.ManifestFiles, body, p.AdmissionDigest, inventory, fingerprints).Scan(new([]byte)); err != nil {
		t.Fatal(err)
	}
	var planID string
	if err := owner.QueryRowContext(ctx, `SELECT id FROM postprocessing_plans WHERE acquisition_id=$1`, p.AcquisitionID).Scan(&planID); err != nil {
		t.Fatal(err)
	}
	return owner, db, p, planID
}

type budgetRow struct {
	readyAt, wallUntil   time.Time
	maxAttempts, timeout int
	started              int
	index                string
	ticketNull           bool
}

func readArtifactBudget(t *testing.T, q queryRower, id string) budgetRow {
	t.Helper()
	var b budgetRow
	var ticket sql.NullString
	if err := q.QueryRowContext(context.Background(), `SELECT inspect_ready_at,inspect_wall_until,inspect_max_attempts,inspect_attempt_timeout_seconds,inspect_attempts_started,inspect_attempt_index::text,inspect_attempt_ticket::text FROM artifacts WHERE id=$1`, id).Scan(&b.readyAt, &b.wallUntil, &b.maxAttempts, &b.timeout, &b.started, &b.index, &ticket); err != nil {
		t.Fatal(err)
	}
	b.ticketNull = !ticket.Valid
	return b
}

func readPlanBudget(t *testing.T, q queryRower, id string) budgetRow {
	t.Helper()
	var b budgetRow
	var ticket sql.NullString
	if err := q.QueryRowContext(context.Background(), `SELECT plan_ready_at,plan_wall_until,plan_max_attempts,plan_attempt_timeout_seconds,plan_attempts_started,plan_attempt_index::text,plan_attempt_ticket::text FROM postprocessing_plans WHERE id=$1`, id).Scan(&b.readyAt, &b.wallUntil, &b.maxAttempts, &b.timeout, &b.started, &b.index, &ticket); err != nil {
		t.Fatal(err)
	}
	b.ticketNull = !ticket.Valid
	return b
}

// assertDefaultBudget checks a row that received no explicit budget: the
// policy numbers, a wall exactly wall_seconds after ready, and the initial
// shape (no attempt started, empty index, no ticket).
func assertDefaultBudget(t *testing.T, label string, b budgetRow, p worker.KindPolicy) {
	t.Helper()
	if b.maxAttempts != p.MaxAttempts || b.timeout != int(p.BusinessAttemptTimeout().Seconds()) {
		t.Fatalf("%s: max=%d timeout=%d want policy %d/%d", label, b.maxAttempts, b.timeout, p.MaxAttempts, int(p.BusinessAttemptTimeout().Seconds()))
	}
	if got := b.wallUntil.Sub(b.readyAt); got != budgetWallSeconds*time.Second {
		t.Fatalf("%s: wall-ready=%v want %ds", label, got, budgetWallSeconds)
	}
	if b.started != 0 || b.index != "[]" || !b.ticketNull {
		t.Fatalf("%s: initial shape started=%d index=%s ticketNull=%v", label, b.started, b.index, b.ticketNull)
	}
}

func expectBudgetError(t *testing.T, label string, err error, message string) {
	t.Helper()
	var pg *pgconn.PgError
	if !errors.As(err, &pg) || pg.Code != "23514" || pg.Message != message {
		t.Fatalf("%s: want 23514 %s, got %v", label, message, err)
	}
}

func expectBudgetCheck(t *testing.T, label string, err error, constraint string) {
	t.Helper()
	var pg *pgconn.PgError
	if !errors.As(err, &pg) || pg.Code != "23514" || pg.ConstraintName != constraint {
		t.Fatalf("%s: want CHECK %s, got %v", label, constraint, err)
	}
}

// The DDL DEFAULTs are the SQL-side mirror of productionLimits(); they must
// equal the canonical KindPolicy numbers for each stage.
func TestAttemptBudgetDDLDefaultsMatchKindPolicy(t *testing.T) {
	owner := openDisposableDB(t)
	if err := (Runner{DB: owner, Dir: migrationsDir(t)}).Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	inspect := stagePolicy(t, workitems.KindArtifactInspect)
	plan := stagePolicy(t, workitems.KindPostProcessPlan)
	for _, c := range []struct{ table, column, want string }{
		{"artifacts", "inspect_max_attempts", strconv.Itoa(inspect.MaxAttempts)},
		{"artifacts", "inspect_attempt_timeout_seconds", strconv.Itoa(int(inspect.BusinessAttemptTimeout().Seconds()))},
		{"postprocessing_plans", "plan_max_attempts", strconv.Itoa(plan.MaxAttempts)},
		{"postprocessing_plans", "plan_attempt_timeout_seconds", strconv.Itoa(int(plan.BusinessAttemptTimeout().Seconds()))},
		{"artifacts", "inspect_ready_at", "CURRENT_TIMESTAMP"},
		{"postprocessing_plans", "plan_ready_at", "CURRENT_TIMESTAMP"},
	} {
		var def sql.NullString
		if err := owner.QueryRow(`SELECT column_default FROM information_schema.columns WHERE table_schema='public' AND table_name=$1 AND column_name=$2`, c.table, c.column).Scan(&def); err != nil {
			t.Fatal(err)
		}
		if !def.Valid || def.String != c.want {
			t.Fatalf("%s.%s default=%q want %q", c.table, c.column, def.String, c.want)
		}
	}
	// The wall DEFAULT is evaluated, not string-compared: ready DEFAULT plus
	// wall_seconds, inside one transaction (CURRENT_TIMESTAMP is stable).
	for _, c := range []struct{ table, ready, wall string }{
		{"artifacts", "inspect_ready_at", "inspect_wall_until"},
		{"postprocessing_plans", "plan_ready_at", "plan_wall_until"},
	} {
		var expr sql.NullString
		if err := owner.QueryRow(`SELECT column_default FROM information_schema.columns WHERE table_schema='public' AND table_name=$1 AND column_name=$2`, c.table, c.wall).Scan(&expr); err != nil || !expr.Valid {
			t.Fatalf("%s.%s default missing: %v", c.table, c.wall, err)
		}
		var seconds float64
		if err := owner.QueryRow(`SELECT extract(epoch FROM (` + expr.String + `) - CURRENT_TIMESTAMP)`).Scan(&seconds); err != nil {
			t.Fatal(err)
		}
		if seconds != budgetWallSeconds {
			t.Fatalf("%s.%s default %s spans %vs want %d", c.table, c.wall, expr.String, seconds, budgetWallSeconds)
		}
	}
	if inspect.BusinessAttemptTimeout() == plan.BusinessAttemptTimeout() {
		t.Fatal("inspect and plan deadlines coincide; the split-default proof below would be vacuous")
	}
}

// Inspect side: bare and explicit-NULL INSERTs are completed to the policy
// numbers; INSERT with a started/ticketed/indexed budget is the initial-shape
// refusal; the CHECK still bounds explicit values; UPDATE never rewrites the
// four columns or jumps the clock.
func TestAttemptBudgetInspectBudgetDefaultsAndGuard(t *testing.T) {
	owner, _, p := progressProbe(t, "budget-inspect", nil, nil)
	ctx := context.Background()
	if _, err := owner.ExecContext(ctx, `UPDATE download_tasks SET state='completed' WHERE id=$1`, p.TaskID); err != nil {
		t.Fatal(err)
	}
	policy := stagePolicy(t, workitems.KindArtifactInspect)
	const bare = `INSERT INTO artifacts(id,download_task_id,acquisition_id,inventory_snapshot,fingerprint_snapshot,state) VALUES('budget-artifact',$1,$2,'{}','{}','pending')`
	insertIn := func(label, statement string) {
		tx, err := owner.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if _, err := tx.Exec(statement, p.TaskID, p.AcquisitionID); err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		b := readArtifactBudget(t, tx, "budget-artifact")
		var txNow time.Time
		if err := tx.QueryRow(`SELECT CURRENT_TIMESTAMP`).Scan(&txNow); err != nil {
			t.Fatal(err)
		}
		if !b.readyAt.Equal(txNow) {
			t.Fatalf("%s: ready=%v want transaction timestamp %v", label, b.readyAt, txNow)
		}
		assertDefaultBudget(t, label, b, policy)
	}
	insertIn("bare INSERT (DDL DEFAULT)", bare)
	insertIn("explicit NULL INSERT (guard completion)", `INSERT INTO artifacts(id,download_task_id,acquisition_id,inventory_snapshot,fingerprint_snapshot,state,inspect_ready_at,inspect_wall_until,inspect_max_attempts,inspect_attempt_timeout_seconds) VALUES('budget-artifact',$1,$2,'{}','{}','pending',NULL,NULL,NULL,NULL)`)

	countArtifacts := func() int {
		var n int
		if err := owner.QueryRow(`SELECT count(*) FROM artifacts`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := countArtifacts()
	const cols = `INSERT INTO artifacts(id,download_task_id,acquisition_id,inventory_snapshot,fingerprint_snapshot,state,`
	for _, c := range []struct{ label, statement string }{
		{"attempts_started=1", cols + `inspect_attempts_started) VALUES('budget-artifact',$1,$2,'{}','{}','pending',1)`},
		{"ticket present", cols + `inspect_attempt_ticket) VALUES('budget-artifact',$1,$2,'{}','{}','pending','{}'::jsonb)`},
		{"index non-empty", cols + `inspect_attempt_index) VALUES('budget-artifact',$1,$2,'{}','{}','pending','[{}]'::jsonb)`},
	} {
		_, err := owner.Exec(c.statement, p.TaskID, p.AcquisitionID)
		expectBudgetError(t, c.label, err, "INSPECT_ATTEMPT_INITIAL_SHAPE")
	}
	// Explicit values stay bounded by the CHECK. 200s of wall is below the
	// inspect timeout (300s) and must be refused here, whereas the plan side
	// accepts the same span (its timeout is 120s).
	for _, c := range []struct{ label, statement string }{
		{"wall below ready+timeout", cols + `inspect_ready_at,inspect_wall_until) VALUES('budget-artifact',$1,$2,'{}','{}','pending',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP+interval '200 seconds')`},
		{"max_attempts=0", cols + `inspect_max_attempts) VALUES('budget-artifact',$1,$2,'{}','{}','pending',0)`},
		{"timeout=86401", cols + `inspect_attempt_timeout_seconds,inspect_wall_until) VALUES('budget-artifact',$1,$2,'{}','{}','pending',86401,CURRENT_TIMESTAMP+interval '2 days')`},
	} {
		_, err := owner.Exec(c.statement, p.TaskID, p.AcquisitionID)
		expectBudgetCheck(t, c.label, err, "inspect_attempt_budget_shape")
	}
	if got := countArtifacts(); got != before {
		t.Fatalf("refused INSERTs wrote rows: %d -> %d", before, got)
	}

	if _, err := owner.Exec(bare, p.TaskID, p.AcquisitionID); err != nil {
		t.Fatal(err)
	}
	committed := readArtifactBudget(t, owner, "budget-artifact")
	assertDefaultBudget(t, "committed bare row", committed, policy)
	for _, c := range []struct{ label, statement string }{
		{"ready_at", `UPDATE artifacts SET inspect_ready_at=inspect_ready_at-interval '1 second' WHERE id='budget-artifact'`},
		{"wall_until", `UPDATE artifacts SET inspect_wall_until=inspect_wall_until+interval '1 second' WHERE id='budget-artifact'`},
		{"max_attempts", `UPDATE artifacts SET inspect_max_attempts=inspect_max_attempts+1 WHERE id='budget-artifact'`},
		{"timeout", `UPDATE artifacts SET inspect_attempt_timeout_seconds=inspect_attempt_timeout_seconds-1 WHERE id='budget-artifact'`},
		{"attempts jump +2", `UPDATE artifacts SET inspect_attempts_started=inspect_attempts_started+2 WHERE id='budget-artifact'`},
	} {
		_, err := owner.Exec(c.statement)
		expectBudgetError(t, c.label, err, "INSPECT_ATTEMPT_BUDGET_IMMUTABLE")
	}
	if after := readArtifactBudget(t, owner, "budget-artifact"); after != committed {
		t.Fatalf("refused UPDATEs changed the budget: %+v -> %+v", committed, after)
	}
}

// Plan side: same closure with the plan stage's own policy (timeout 120, not
// the inspect 300). The default row is produced by the real
// artifact_commit_inspection path; explicit-NULL and refusal shapes are
// exercised on a re-insert of that row inside a rolled-back transaction (the
// plan is unique per acquisition).
func TestAttemptBudgetPlanBudgetDefaultsAndGuard(t *testing.T) {
	owner, _, _, planID := seedBudgetOwners(t, "budget-plan", nil)
	ctx := context.Background()
	inspect := stagePolicy(t, workitems.KindArtifactInspect)
	plan := stagePolicy(t, workitems.KindPostProcessPlan)
	artifactRow := readArtifactBudget(t, owner, "plan-artifact")
	assertDefaultBudget(t, "artifact via bare fixture INSERT", artifactRow, inspect)
	planRow := readPlanBudget(t, owner, planID)
	assertDefaultBudget(t, "plan via artifact_commit_inspection", planRow, plan)
	if planRow.timeout == artifactRow.timeout {
		t.Fatalf("plan timeout %d copied the inspect timeout %d", planRow.timeout, artifactRow.timeout)
	}

	var rowJSON string
	if err := owner.QueryRow(`SELECT to_jsonb(p)::text FROM postprocessing_plans p WHERE id=$1`, planID).Scan(&rowJSON); err != nil {
		t.Fatal(err)
	}
	const reinsert = `INSERT INTO postprocessing_plans SELECT (jsonb_populate_record(NULL::public.postprocessing_plans,$1::jsonb||$2::jsonb)).*`
	// reinsertIn deletes the committed plan and re-inserts it with overrides in
	// one transaction that is always rolled back.
	reinsertIn := func(label, overrides string, check func(*sql.Tx, error)) {
		tx, err := owner.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if _, err := tx.Exec(`DELETE FROM postprocessing_plans WHERE id=$1`, planID); err != nil {
			t.Fatalf("%s: arrange delete: %v", label, err)
		}
		_, err = tx.Exec(reinsert, rowJSON, overrides)
		check(tx, err)
	}
	reinsertIn("explicit NULL", `{"plan_ready_at":null,"plan_wall_until":null,"plan_max_attempts":null,"plan_attempt_timeout_seconds":null}`, func(tx *sql.Tx, err error) {
		if err != nil {
			t.Fatalf("explicit NULL plan INSERT: %v", err)
		}
		b := readPlanBudget(t, tx, planID)
		var txNow time.Time
		if err := tx.QueryRow(`SELECT CURRENT_TIMESTAMP`).Scan(&txNow); err != nil {
			t.Fatal(err)
		}
		if !b.readyAt.Equal(txNow) {
			t.Fatalf("explicit NULL plan ready=%v want %v", b.readyAt, txNow)
		}
		assertDefaultBudget(t, "explicit NULL plan INSERT (guard completion)", b, plan)
	})
	for _, c := range []struct{ label, overrides string }{
		{"attempts_started=1", `{"plan_attempts_started":1}`},
		{"ticket present", `{"plan_attempt_ticket":{}}`},
		{"index non-empty", `{"plan_attempt_index":[{}]}`},
	} {
		reinsertIn(c.label, c.overrides, func(_ *sql.Tx, err error) { expectBudgetError(t, c.label, err, "PLAN_ATTEMPT_INITIAL_SHAPE") })
	}
	// The plan CHECK uses the plan timeout: ready+100s is below 120 and is
	// refused, ready+200s (refused on the inspect side) is accepted.
	reinsertIn("wall below ready+120", `{"plan_ready_at":"2030-01-01T00:00:00Z","plan_wall_until":"2030-01-01T00:01:40Z"}`, func(_ *sql.Tx, err error) {
		expectBudgetCheck(t, "wall below ready+120", err, "plan_attempt_budget_shape")
	})
	reinsertIn("max_attempts=101", `{"plan_max_attempts":101}`, func(_ *sql.Tx, err error) {
		expectBudgetCheck(t, "max_attempts=101", err, "plan_attempt_budget_shape")
	})
	reinsertIn("wall ready+200 with plan timeout", `{"plan_ready_at":"2030-01-01T00:00:00Z","plan_wall_until":"2030-01-01T00:03:20Z"}`, func(tx *sql.Tx, err error) {
		if err != nil {
			t.Fatalf("plan wall ready+200s must satisfy the plan CHECK (timeout %ds): %v", int(plan.BusinessAttemptTimeout().Seconds()), err)
		}
		if b := readPlanBudget(t, tx, planID); b.timeout != int(plan.BusinessAttemptTimeout().Seconds()) || b.wallUntil.Sub(b.readyAt) != 200*time.Second {
			t.Fatalf("explicit plan budget %+v", b)
		}
	})
	if after := readPlanBudget(t, owner, planID); after != planRow {
		t.Fatalf("rolled-back plan probes changed the committed plan: %+v -> %+v", planRow, after)
	}

	for _, c := range []struct{ label, statement string }{
		{"ready_at", `UPDATE postprocessing_plans SET plan_ready_at=plan_ready_at-interval '1 second' WHERE id=$1`},
		{"wall_until", `UPDATE postprocessing_plans SET plan_wall_until=plan_wall_until+interval '1 second' WHERE id=$1`},
		{"max_attempts", `UPDATE postprocessing_plans SET plan_max_attempts=plan_max_attempts+1 WHERE id=$1`},
		{"timeout", `UPDATE postprocessing_plans SET plan_attempt_timeout_seconds=plan_attempt_timeout_seconds-1 WHERE id=$1`},
		{"attempts jump +2", `UPDATE postprocessing_plans SET plan_attempts_started=plan_attempts_started+2 WHERE id=$1`},
	} {
		_, err := owner.Exec(c.statement, planID)
		expectBudgetError(t, c.label, err, "PLAN_ATTEMPT_BUDGET_IMMUTABLE")
	}
	if after := readPlanBudget(t, owner, planID); after != planRow {
		t.Fatalf("refused plan UPDATEs changed the budget: %+v -> %+v", planRow, after)
	}
}

// Invariant: in the applied schema, the business budget DDL
// DEFAULT never exceeds the claim max_attempts literal that every live SQL
// work-creation path freezes for inspect/plan, and those literals equal the
// Go claim constants. Recovery judges exhaustion by the claim
// budget, so business > claim would need a forward migration first.
func TestAttemptBudgetBusinessBudgetWithinClaimBudget(t *testing.T) {
	owner := openDisposableDB(t)
	if err := (Runner{DB: owner, Dir: migrationsDir(t)}).Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		table, column string
		literal       *regexp.Regexp
		claimMax      int
	}{
		{"artifacts", "inspect_max_attempts", regexp.MustCompile(`'artifact_inspect:'\|\|artifact,\s*(\d+)`), workitems.ArtifactInspectClaimMaxAttempts},
		{"postprocessing_plans", "plan_max_attempts", regexp.MustCompile(`'plan:'\|\|p_plan,\s*(\d+)`), workitems.PostProcessPlanClaimMaxAttempts},
	} {
		var def string
		if err := owner.QueryRow(`SELECT column_default FROM information_schema.columns WHERE table_schema='public' AND table_name=$1 AND column_name=$2`, c.table, c.column).Scan(&def); err != nil {
			t.Fatal(err)
		}
		business, err := strconv.Atoi(def)
		if err != nil || business < 1 || business > c.claimMax {
			t.Fatalf("%s.%s default %q exceeds claim budget %d", c.table, c.column, def, c.claimMax)
		}
		rows, err := owner.Query(`SELECT p.proname, pg_get_functiondef(p.oid) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='public' AND p.prokind='f'`)
		if err != nil {
			t.Fatal(err)
		}
		found := 0
		for rows.Next() {
			var name, body string
			if err := rows.Scan(&name, &body); err != nil {
				t.Fatal(err)
			}
			for _, m := range c.literal.FindAllStringSubmatch(body, -1) {
				found++
				if n, _ := strconv.Atoi(m[1]); n != c.claimMax {
					t.Fatalf("%s creates %s work with max_attempts=%d, Go claim budget is %d", name, c.column, n, c.claimMax)
				}
			}
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
		if found == 0 {
			t.Fatalf("no live work-creation path found for %s; the tripwire would be vacuous", c.column)
		}
	}
}
