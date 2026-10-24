package migrations

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	protocol "bansoko/internal/acquisition/terminal/domain"
	"bansoko/internal/runtime/database"
	"bansoko/internal/testutil/disposabledb/guard"
	"bansoko/internal/workitems"
)

func TestFilePreparePermitRuntime(t *testing.T) {
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
	c, err := store.ClaimDue(context.Background(), "file-prepare", []string{workitems.KindFileExecute}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err := db.QueryRow(`SELECT public.file_start_attempt($1,$2,$3,'prepare-session')`, c.Owner.ID, c.ID, c.FencingToken).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	// Owner supplies read-only path-level facts (PathObjectV1) for this
	// relational negative matrix. Real adapter observation is verified
	// separately; these are not execution proof.
	if err := owner.QueryRow(`SELECT jsonb_build_object('source',jsonb_build_object('location',jsonb_build_object('canonical_path',o.source_path),'file_type','regular','size',o.expected_size_bytes,'identity',i.identity),
 'source_parent',i.identity,'target_parent',i.identity,'target',jsonb_build_object('canonical_path',o.target_path),'temp',jsonb_build_object('canonical_path',regexp_replace(o.target_path,'[^/]+$','')||'.bansoko-permit-'||encode(sha256(convert_to('prepare-permit','UTF8')),'hex')))
 FROM public.file_operations o CROSS JOIN LATERAL (SELECT jsonb_build_object('kind','local_fs','device','1','inode','2') identity) i WHERE o.id=$1`, c.Owner.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var objectsMap map[string]any
	if err := json.Unmarshal(raw, &objectsMap); err != nil {
		t.Fatal(err)
	}
	source := objectsMap["source"].(map[string]any)
	source["observed_at"] = protocol.FormatTime(time.Now().UTC())
	for _, loc := range []map[string]any{source["location"].(map[string]any), objectsMap["target"].(map[string]any), objectsMap["temp"].(map[string]any)} {
		loc["root"] = "/"
		loc["relative_path"] = loc["canonical_path"].(string)[1:]
		loc["namespace_digest"] = protocol.Digest("local-filesystem-namespace:v1", []byte(`{"adapter_version":"local-stable-read-v1","root":"/"}`))
	}
	raw, err = json.Marshal(objectsMap)
	if err != nil {
		t.Fatal(err)
	}
	v, err := protocol.ParseStrict(raw)
	if err != nil {
		t.Fatal(err)
	}
	objects := string(protocol.Canonical(v))
	var response []byte
	prepareEntryNegative := func(name string, mutate func(v protocol.Value)) {
		bad, err := protocol.ParseStrict([]byte(objects))
		if err != nil {
			t.Fatal(err)
		}
		mutate(bad)
		beforeEntry := graphSnapshot(t, owner)
		if err := db.QueryRow(`SELECT public.file_prepare_permit($1,$2,$3,'prepare-session','prepare-permit',$4)`, c.Owner.ID, c.ID, c.FencingToken, string(protocol.Canonical(bad))).Scan(&response); err == nil || !strings.Contains(err.Error(), "FILE_PREPARE_OBJECT_BINDING") {
			t.Fatalf("%s admitted at the prepare write entry: %v", name, err)
		}
		if string(beforeEntry) != string(graphSnapshot(t, owner)) {
			t.Fatalf("%s changed graph", name)
		}
	}
	// The retired evidence shapes are refused: a content proof, an
	// observation source, FV-wrapped or extended identities and the cleanup
	// capture locators (§13.16.48).
	prepareEntryNegative("retired_content_proof", func(v protocol.Value) {
		v.Object["source"].Object["content"] = protocol.Value{Kind: protocol.KindNull}
	})
	prepareEntryNegative("retired_observation_source", func(v protocol.Value) {
		v.Object["source"].Object["observation_source"] = protocol.Value{Kind: protocol.KindString, String: "local-stable-read-v1"}
	})
	prepareEntryNegative("retired_capture_locator", func(v protocol.Value) {
		v.Object["source_capture"] = v.Object["temp"]
	})
	for _, parent := range []string{"source_parent", "target_parent"} {
		prepareEntryNegative(parent+"_mount_ref", func(v protocol.Value) {
			v.Object[parent].Object["mount_ref"] = protocol.Value{Kind: protocol.KindNull}
		})
		prepareEntryNegative(parent+"_inode_zero", func(v protocol.Value) {
			v.Object[parent].Object["inode"] = protocol.Value{Kind: protocol.KindString, String: "0"}
		})
	}
	prepareEntryNegative("source_identity_wrapped", func(v protocol.Value) {
		identity := v.Object["source"].Object["identity"]
		v.Object["source"].Object["identity"] = protocol.Value{Kind: protocol.KindObject, Object: map[string]protocol.Value{"state": {Kind: protocol.KindString, String: "known"}, "value": identity, "reason_code": {Kind: protocol.KindNull}}}
	})
	prepareEntryNegative("source_size_mismatch", func(v protocol.Value) {
		v.Object["source"].Object["size"] = protocol.Value{Kind: protocol.KindInteger, Int: v.Object["source"].Object["size"].Int + 1}
	})
	prepareEntryNegative("source_not_regular", func(v protocol.Value) {
		v.Object["source"].Object["file_type"] = protocol.Value{Kind: protocol.KindString, String: "directory"}
	})
	prepareEntryNegative("source_observed_future", func(v protocol.Value) {
		v.Object["source"].Object["observed_at"] = protocol.Value{Kind: protocol.KindString, String: protocol.FormatTime(time.Now().Add(time.Hour).UTC().Truncate(time.Microsecond))}
	})
	// Location at the producer write entry: every consumed location slot
	// (source/target/temp) independently passes file_location_valid
	// (canonical relation, traversal, namespace digest recompute).
	prepareEntryNegative("source_location_digest", func(v protocol.Value) {
		loc := v.Object["source"].Object["location"]
		loc.Object["namespace_digest"] = protocol.Value{Kind: protocol.KindString, String: "sha256:" + strings.Repeat("0", 64)}
		v.Object["source"].Object["location"] = loc
	})
	prepareEntryNegative("source_location_traversal", func(v protocol.Value) {
		loc := v.Object["source"].Object["location"]
		loc.Object["relative_path"] = protocol.Value{Kind: protocol.KindString, String: "lib/../escape"}
		v.Object["source"].Object["location"] = loc
	})
	prepareEntryNegative("source_location_empty_rel", func(v protocol.Value) {
		loc := v.Object["source"].Object["location"]
		loc.Object["relative_path"] = protocol.Value{Kind: protocol.KindString, String: ""}
		v.Object["source"].Object["location"] = loc
	})
	prepareEntryNegative("target_location_digest", func(v protocol.Value) {
		loc := v.Object["target"]
		loc.Object["namespace_digest"] = protocol.Value{Kind: protocol.KindString, String: "sha256:" + strings.Repeat("0", 64)}
		v.Object["target"] = loc
	})
	prepareEntryNegative("temp_location_digest", func(v protocol.Value) {
		loc := v.Object["temp"]
		loc.Object["namespace_digest"] = protocol.Value{Kind: protocol.KindString, String: "sha256:" + strings.Repeat("0", 64)}
		v.Object["temp"] = loc
	})
	var leaked int
	if err := owner.QueryRow(`SELECT count(*) FROM public.file_operations WHERE publication_permit_id='prepare-permit'`).Scan(&leaked); err != nil {
		t.Fatal(err)
	}
	if leaked != 0 {
		t.Fatalf("rejected prepare leaked %d evidence/binding rows", leaked)
	}
	if err := db.QueryRow(`SELECT public.file_prepare_permit($1,$2,$3,'prepare-session','prepare-permit',$4)`, c.Owner.ID, c.ID, c.FencingToken, objects).Scan(&response); err != nil {
		t.Fatal(err)
	}
	var reply struct {
		Created bool `json:"created"`
	}
	if err := json.Unmarshal(response, &reply); err != nil || !reply.Created {
		t.Fatalf("response=%s err=%v", response, err)
	}
	var frozenKeys string
	if err := owner.QueryRow(`SELECT string_agg(k,',' ORDER BY k) FROM public.file_operations o CROSS JOIN LATERAL jsonb_object_keys(o.publication_frozen_objects) k WHERE o.publication_permit_id='prepare-permit'`).Scan(&frozenKeys); err != nil {
		t.Fatal(err)
	}
	if frozenKeys != "source,source_parent,target,target_parent,temp" {
		t.Fatalf("frozen publication objects=%s", frozenKeys)
	}
	before := graphSnapshot(t, owner)
	if err := db.QueryRow(`SELECT public.file_prepare_permit($1,$2,$3,'prepare-session','another-permit',$4)`, c.Owner.ID, c.ID, c.FencingToken, objects).Scan(&response); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(response, &reply); err != nil || reply.Created {
		t.Fatalf("repeat response=%s err=%v", response, err)
	}
	if string(before) != string(graphSnapshot(t, owner)) {
		t.Fatal("repeat prepare wrote graph")
	}
	var state string
	if err := owner.QueryRow(`SELECT publication_dispatch_state FROM public.file_operations WHERE publication_permit_id='prepare-permit'`).Scan(&state); err != nil || state != "prepared" {
		t.Fatalf("state=%s err=%v", state, err)
	}
	clockTx, err := owner.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := clockTx.Exec(`UPDATE public.file_operations SET budget_clock_at=file_wall_until WHERE id=$1`, c.Owner.ID); err != nil {
		clockTx.Rollback()
		t.Fatal(err)
	}
	if _, err := clockTx.Exec(`SET LOCAL ROLE bansoko_runtime`); err != nil {
		clockTx.Rollback()
		t.Fatal(err)
	}
	if err := clockTx.QueryRow(`SELECT public.file_start_dispatch($1,$2,$3,'prepare-permit','prepare-session')`, c.Owner.ID, c.ID, c.FencingToken).Scan(&response); err == nil || !strings.Contains(err.Error(), "FILE_DISPATCH_NOT_QUALIFIED") {
		clockTx.Rollback()
		t.Fatalf("expired high-water grant: %v", err)
	}
	clockTx.Rollback()
	if err := db.QueryRow(`SELECT public.file_start_dispatch($1,$2,$3,'prepare-permit','prepare-session')`, c.Owner.ID, c.ID, c.FencingToken).Scan(&response); err != nil {
		t.Fatal(err)
	}
	assertFileGrantClock(t, response)
	if err := db.QueryRow(`SELECT public.file_start_step($1,$2,$3,'prepare-permit','prepare-session','stage_temp')`, c.Owner.ID, c.ID, c.FencingToken).Scan(&response); err != nil {
		t.Fatal(err)
	}
	assertFileGrantClock(t, response)
	var valid bool
	if err := owner.QueryRow(`SELECT publication_stage_temp_started_at IS NOT NULL AND publication_stage_temp_started_at= (publication_step_checkpoint->'stage_temp'->>'observed_at')::timestamptz AND publication_stage_temp_started_at>=publication_dispatch_started_at AND publication_reserve_version_started_at IS NULL AND publication_publish_target_started_at IS NULL AND publication_source_cleanup_started_at IS NULL AND publication_temp_cleanup_started_at IS NULL FROM file_operations WHERE publication_permit_id='prepare-permit'`).Scan(&valid); err != nil || !valid {
		t.Fatalf("authorization CAS=%v err=%v", valid, err)
	}
	grantBefore := graphSnapshot(t, owner)
	if err := db.QueryRow(`SELECT file_start_step($1,$2,$3,'prepare-permit','prepare-session','stage_temp')`, c.Owner.ID, c.ID, c.FencingToken).Scan(&response); err != nil {
		t.Fatal(err)
	}
	if string(grantBefore) != string(graphSnapshot(t, owner)) {
		t.Fatal("repeat grant changed graph")
	}
	for _, assignment := range []string{`publication_stage_temp_started_at=NULL`, `publication_stage_temp_started_at=publication_stage_temp_started_at+interval '1 microsecond'`, `publication_publish_target_started_at=clock_timestamp()`, `publication_reserve_version_started_at=clock_timestamp()`} {
		_, err := owner.Exec(`UPDATE file_operations SET ` + assignment + ` WHERE publication_permit_id='prepare-permit'`)
		if err == nil || !strings.Contains(err.Error(), "FILE_STEP_AUTHORIZATION_INVALID") {
			t.Fatalf("immutable %s=%v", assignment, err)
		}
		if string(grantBefore) != string(graphSnapshot(t, owner)) {
			t.Fatal("rejected authorization changed graph")
		}
	}
	if err := owner.QueryRow(`WITH observed AS MATERIALIZED (SELECT to_char(clock_timestamp() AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"') at) SELECT jsonb_build_object('identity',jsonb_build_object('schema_version',1,'permit_id',e.publication_permit_id,'root_id',e.acquisition_id,'original_work_id',e.publication_work_item_id,'fencing_token',e.publication_fencing_token,'executor_session',e.publication_executor_session,'spec_digest',e.publication_spec_digest),'emitter',jsonb_build_object('mode','dispatch_session','session',e.publication_executor_session),'observed_at',observed.at,'payload',jsonb_build_object('checkpoint',jsonb_set(e.publication_step_checkpoint,'{stage_temp}',jsonb_build_object('state','unknown','proof',NULL,'observed_at',observed.at,'reason_code','PROOF_UNAVAILABLE')),'closure',jsonb_build_object('state','unavailable','value',NULL,'reason_code','PROOF_UNAVAILABLE'))) FROM public.file_operations e CROSS JOIN observed WHERE e.publication_permit_id='prepare-permit'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	v, err = protocol.ParseStrict(raw)
	if err != nil {
		t.Fatal(err)
	}
	// PostgreSQL US formatting always emits six digits, including forbidden
	// fractional trailing zeroes. Normalize the fixture's observed DB instant
	// through the existing wire formatter, before hashing the receipt.
	observedText := v.Object["observed_at"].String
	observedAt, err := time.Parse(time.RFC3339Nano, observedText)
	if err != nil {
		t.Fatal(err)
	}
	wireAt := protocol.FormatTime(observedAt)
	v.Object["observed_at"] = protocol.Value{Kind: protocol.KindString, String: wireAt}
	v.Object["payload"].Object["checkpoint"].Object["stage_temp"].Object["observed_at"] = protocol.Value{Kind: protocol.KindString, String: wireAt}
	if err := protocol.Validate("FileReceiptEnvelopeV1", v); err != nil {
		t.Fatalf("fixture receipt invalid original_time=%q: %v", observedText, err)
	}
	receiptRaw := string(protocol.Canonical(v))
	if err := db.QueryRow(`SELECT public.file_record_receipt('prepare-permit',$1)`, receiptRaw).Scan(&response); err != nil {
		t.Fatal(err)
	}
	before = graphSnapshot(t, owner)
	if err := db.QueryRow(`SELECT public.file_record_receipt('prepare-permit',$1)`, receiptRaw).Scan(&response); err != nil {
		t.Fatal(err)
	}
	if string(before) != string(graphSnapshot(t, owner)) {
		t.Fatal("repeat receipt wrote graph")
	}
	v.Object["identity"].Object["executor_session"] = protocol.Value{Kind: protocol.KindString, String: "wrong-session"}
	if err := db.QueryRow(`SELECT public.file_record_receipt('prepare-permit',$1)`, string(protocol.Canonical(v))).Scan(&response); err == nil {
		t.Fatal("wrong receipt session accepted")
	}
}

func assertFileGrantClock(t *testing.T, raw []byte) {
	t.Helper()
	var grant struct {
		Granted bool      `json:"granted"`
		Now     time.Time `json:"gate_db_now"`
		Until   time.Time `json:"attempt_until"`
	}
	if err := json.Unmarshal(raw, &grant); err != nil {
		t.Fatal(err)
	}
	if !grant.Granted || grant.Now.IsZero() || !grant.Until.After(grant.Now) {
		t.Fatalf("invalid grant clock: %s", raw)
	}
}
