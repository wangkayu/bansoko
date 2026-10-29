package migrations

import (
	"bansoko/internal/library/domain"
	"bansoko/internal/workitems"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// noEffectPlanPorts fails the test if a recovery observer reaches the
// filesystem: observing a committed Plan must be a pure database read.
type noEffectPlanPorts struct{ t *testing.T }

func (p noEffectPlanPorts) PreparePublication(context.Context, domain.FileOperation, string) (domain.PublicationPreparation, error) {
	p.t.Fatal("recovery observer prepared a file effect")
	return domain.PublicationPreparation{}, errors.New("unexpected effect")
}

func (p noEffectPlanPorts) Observe(context.Context, string) (string, error) {
	p.t.Fatal("recovery observer read the filesystem")
	return "", errors.New("unexpected observation")
}

// graphSnapshot captures every public table, the column catalog, relation ACLs,
// function definitions and ACLs, and triggers, so a test can prove a rejected or
// observation-only call changed nothing at all.
func graphSnapshot(t *testing.T, db *sql.DB) []byte {
	t.Helper()
	var tables []string
	rows, err := db.Query(`SELECT tablename FROM pg_tables WHERE schemaname='public' ORDER BY tablename COLLATE "C"`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, table)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	var result bytes.Buffer
	for _, table := range tables {
		var raw []byte
		query := `SELECT COALESCE(jsonb_agg(row ORDER BY row::text),'[]'::jsonb) FROM (SELECT to_jsonb(t) AS row FROM public."` + table + `" t) q`
		if err := db.QueryRow(query).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		result.WriteString(table)
		result.Write(raw)
	}
	for _, query := range []string{
		`SELECT COALESCE(jsonb_agg(to_jsonb(c) ORDER BY table_name,ordinal_position),'[]') FROM information_schema.columns c WHERE table_schema='public'`,
		`SELECT COALESCE(jsonb_agg(jsonb_build_object('name',c.relname,'acl',c.relacl) ORDER BY c.relname),'[]') FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public'`,
		`SELECT COALESCE(jsonb_agg(jsonb_build_object('name',p.oid::regprocedure::text,'acl',p.proacl,'definition',pg_get_functiondef(p.oid)) ORDER BY p.oid::regprocedure::text),'[]') FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='public' AND p.prokind='f'`,
		`SELECT COALESCE(jsonb_agg(jsonb_build_object('table',c.relname,'name',t.tgname,'definition',pg_get_triggerdef(t.oid)) ORDER BY c.relname,t.tgname),'[]') FROM pg_trigger t JOIN pg_class c ON c.oid=t.tgrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public'`,
	} {
		var raw []byte
		if err := db.QueryRow(query).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		result.Write(raw)
	}
	return result.Bytes()
}

// seedTypedPlanRecovery arranges one accepted acquisition whose inspection
// committed a queued Plan, then moves the Plan work item into the recovery
// state and returns its recovery claim.
func seedTypedPlanRecovery(t *testing.T) (*sql.DB, *sql.DB, *workitems.Store, workitems.Claim) {
	t.Helper()
	owner, db, p := progressProbe(t, "plan-recovery", nil, nil)
	ctx := context.Background()
	if _, err := owner.ExecContext(ctx, `UPDATE download_tasks SET state='completed' WHERE id=$1`, p.TaskID); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.ExecContext(ctx, `INSERT INTO artifacts(id,download_task_id,acquisition_id,inventory_snapshot,fingerprint_snapshot,state) VALUES('plan-artifact',$1,$2,'{}','{}','pending')`, p.TaskID, p.AcquisitionID); err != nil {
		t.Fatal(err)
	}
	var id string
	if err := db.QueryRowContext(ctx, `SELECT public.work_item_create('plan-inspect','artifact','plan-artifact','artifact_inspect','plan-inspect',3,'{}',1,0::double precision,7::smallint)`).Scan(&id); err != nil {
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
		files = append(files, map[string]any{"id": "plan-file-" + f["file_ref"].(string), "file_ref": f["file_ref"], "relative_path": f["relative_path"], "source_path": "/downloads/" + f["relative_path"].(string), "size_bytes": f["exact_size"], "media_kind": f["media_kind"], "fingerprint": metadataFingerprintFixture(f["exact_size"]), "facts": map[string]any{}})
	}
	body, err := json.Marshal(files)
	if err != nil {
		t.Fatal(err)
	}
	inventory, fingerprints := inspectionSnapshotFixture(t, files)
	var raw []byte
	if err := db.QueryRowContext(ctx, `SELECT public.artifact_commit_inspection($1,$2,$3,$4,$5,$6::jsonb,$9::jsonb,$10::jsonb,$7::jsonb,'recovery-plan','recovery-plan-work',$8,'','[]')`, c.Owner.ID, c.ID, c.FencingToken, p.AdmissionDigest, p.ManifestDigest, p.ManifestFiles, body, p.AdmissionDigest, inventory, fingerprints).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	c, err = store.ClaimDue(ctx, "plan", []string{workitems.KindPostProcessPlan}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.BlockRecovery(ctx, c, "effect_unknown"); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.ExecContext(ctx, `UPDATE work_items SET lease_until=CURRENT_TIMESTAMP-INTERVAL '1 second',available_at=CURRENT_TIMESTAMP WHERE id=$1`, c.ID); err != nil {
		t.Fatal(err)
	}
	c, err = store.ClaimRecovery(ctx, []string{workitems.KindPostProcessPlan}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return owner, db, store, c
}
