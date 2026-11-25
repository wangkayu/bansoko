package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"strings"
	"testing"

	protocol "bansoko/internal/acquisition/terminal/domain"
	"bansoko/internal/acquisition/terminal/settlement"
	animedomain "bansoko/internal/anime/domain"
	filesystem "bansoko/internal/library/adapter/filesystem"
	"bansoko/internal/workitems"
)

// rewriteAcceptedInputAsPreVarsRetirement turns the fixture's accepted input
// into the shape a 0.2.x acquisition froze: no media.year / media.season, the
// owner year/season kept in the private config.vars block, and the Vars naming
// choice on the delivery. Migration 163 leaves such bytes untouched, and the
// plan/execution path must still resolve the owner values through the legacy
// read fallback. The row is rewritten as the schema owner after disabling the
// immutability triggers, exactly like a row that predates the upgrade.
func rewriteAcceptedInputAsPreVarsRetirement(t *testing.T, db *sql.DB, root string) {
	t.Helper()
	var canonical string
	if err := db.QueryRow(`SELECT input_canonical_utf8 FROM acquisitions WHERE id=$1`, root).Scan(&canonical); err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(strings.NewReader(canonical))
	decoder.UseNumber()
	var body map[string]any
	if err := decoder.Decode(&body); err != nil {
		t.Fatal(err)
	}
	media, ok := body["media"].(map[string]any)
	if !ok {
		t.Fatal("fixture input has no media object")
	}
	for _, key := range []string{"year", "season"} {
		if _, found := media[key]; !found {
			t.Fatalf("fixture input has no media.%s to replace", key)
		}
		delete(media, key)
	}
	config, ok := body["config"].(map[string]any)
	if !ok {
		t.Fatal("fixture input has no config object")
	}
	config["vars"] = map[string]any{
		"mode": "frozen", "status": "ok", "reason_code": nil,
		"known_inputs": map[string]any{"facts": map[string]any{"anime": map[string]any{
			"year": "2025", "season": json.Number("1"),
		}}},
	}
	delivery, ok := body["delivery"].(map[string]any)
	if !ok {
		t.Fatal("fixture input has no delivery object")
	}
	naming, ok := delivery["naming"].(map[string]any)
	if !ok {
		t.Fatal("fixture input has no delivery.naming object")
	}
	naming["vars_choice"] = "frozen"
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	value, err := protocol.ParseStrict(raw)
	if err != nil {
		t.Fatal(err)
	}
	canonicalBytes := protocol.Canonical(value)
	digest := protocol.Digest("acquisition-input:v1", canonicalBytes)

	owner := ownerTestDB(t)
	if _, err := owner.Exec(`ALTER TABLE acquisitions DISABLE TRIGGER USER`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := owner.Exec(`ALTER TABLE acquisitions ENABLE TRIGGER USER`); err != nil {
			t.Fatalf("re-enable acquisition triggers: %v", err)
		}
	}()
	// acquisitions.input is generated from the canonical bytes, so the rewrite
	// only supplies the text and the digest bound to it.
	if _, err := owner.Exec(`UPDATE acquisitions SET input_canonical_utf8=$2::text,admission_digest=$3 WHERE id=$1`,
		root, string(canonicalBytes), digest); err != nil {
		t.Fatalf("store the pre-0.3 accepted input: %v", err)
	}
}

// A pre-Vars-retirement accepted acquisition keeps working after 0.3: the
// planner resolves the owner year/season through the legacy fallback, the file
// executes through the fixed finish entry, and the root settles. The fixture's
// owner values are frozen in config.vars.known_inputs.facts.anime as the old
// decimal year and integer season, and must still name the rendered target.
func TestPreVarsRetirementInputCompletesExecutionAndSettlement(t *testing.T) {
	ctx := context.Background()
	db, f := openFileOperationDBWithPolicy(t, func(p map[string]any) {
		p["effective_sections"].(map[string]any)[animedomain.SectionFileEffect] = map[string]any{"operation": "copy", "collision_policy": "fail"}
	})
	rewriteAcceptedInputAsPreVarsRetirement(t, db, "file-fixture-root")

	s := newFixtureServiceWithPorts(t, db, f, filesystem.OSFileExecutor{}, filesystem.OSFileObserver{})
	x := typedRuntimeFixture{
		db: db, f: f, s: s,
		store:  &workitems.Store{DB: db, RetentionDays: workitems.DefaultRetentionDays},
		source: f.sourcePath,
	}
	x.planID = seedA11Plan(t, f.artifactID, nil)
	runA11PlanWorker(t, db, s)
	if err := db.QueryRow(`SELECT o.id,o.target_path,w.id FROM file_operations o
JOIN work_item_history w ON w.owner_type='file_operation' AND w.owner_id=o.id
WHERE o.artifact_id=$1 AND o.file_ref='primary'`, f.artifactID).Scan(&x.opID, &x.target, &x.workID); err != nil {
		t.Fatalf("plan output for a pre-0.3 input: %v", err)
	}
	if !strings.Contains(x.target, "Fixture Anime (2025)") || !strings.Contains(x.target, "Season 01") {
		t.Fatalf("legacy owner year/season did not reach the render: %s", x.target)
	}

	if handled, err := x.runFileWorker(t); !handled || err != nil {
		t.Fatalf("typed copy run: handled=%v err=%v", handled, err)
	}
	if got := x.state(t, x.opID); got.opState != ownerStateCompleted || got.workState != workitems.StateSucceeded {
		t.Fatalf("operation over a pre-0.3 input not completed: %+v", got)
	}
	raw, err := os.ReadFile(x.target)
	if err != nil || string(raw) != "source-content" {
		t.Fatalf("target content=%q err=%v", raw, err)
	}

	asSubscriptionRoot(t, x)
	root := acquisitionOf(t, db, x.opID)
	if _, err := activityConsumer(t, x).ConsumeOnce(ctx); err != nil {
		t.Fatal(err)
	}
	settler, err := settlement.NewSettlementService(db)
	if err != nil {
		t.Fatal(err)
	}
	status, err := settler.SettleRoot(ctx, root)
	if err != nil || status != "settled" {
		t.Fatalf("settle the pre-0.3 root: status=%q err=%v", status, err)
	}
	var state string
	if err := db.QueryRow(`SELECT state FROM acquisitions WHERE id=$1`, root).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "succeeded" {
		t.Fatalf("settled root state=%q, want succeeded", state)
	}
}
