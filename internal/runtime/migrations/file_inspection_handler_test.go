package migrations

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	artifact "bansoko/internal/artifact/application"
	"bansoko/internal/download/domain"
	"bansoko/internal/parser"
	facts "bansoko/internal/release/facts"
	media "bansoko/internal/release/facts/adapter/persistence/media"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
)

type inspectionFixtureInspector struct{}

func (inspectionFixtureInspector) Inspect(ctx context.Context, path string) (domain.ArtifactFacts, error) {
	st, err := os.Stat(path)
	if err != nil {
		return domain.ArtifactFacts{}, err
	}
	kind := "video"
	if filepath.Ext(path) == ".ass" {
		kind = "subtitle"
	}
	return domain.ArtifactFacts{Path: path, SizeBytes: st.Size(), ObservedAt: time.Now(), MediaKind: kind}, nil
}

// The inspection handler writes no Activity: the artifact_verified run-log
// entry is derived after commit by the independent acquisition consumer. A
// failing run-log insert therefore does not roll the inspection back; the
// consumer's own tests cover the entry itself.
func TestFileInspectionHandlerAtomicActivity(t *testing.T) {
	t.Run("success", func(t *testing.T) { runInspectionActivity(t, false) })
	t.Run("activity_failure_keeps_inspection", func(t *testing.T) { runInspectionActivity(t, true) })
}

func runInspectionActivity(t *testing.T, inject bool) {
	owner, db, p := progressProbe(t, "inspect-handler", nil, nil)
	ctx := context.Background()
	root := t.TempDir()
	var manifest []map[string]any
	if err := json.Unmarshal([]byte(p.ManifestFiles), &manifest); err != nil {
		t.Fatal(err)
	}
	var releaseFiles []map[string]any
	for _, f := range manifest {
		path := filepath.Join(root, f["relative_path"].(string))
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, make([]byte, int(f["exact_size"].(float64))), 0644); err != nil {
			t.Fatal(err)
		}
		releaseFiles = append(releaseFiles, map[string]any{"source_path": path, "filename": filepath.Base(path), "size_bytes": f["exact_size"], "media_kind": f["media_kind"]})
	}
	frozen, _ := json.Marshal(map[string]any{"release_files": releaseFiles})
	correlation, _ := json.Marshal(map[string]any{"external_id": "tag:inspect-handler", "artifact_path": filepath.Join(root, manifest[0]["relative_path"].(string)), "path_snapshot": map[string]any{"remote_root": root, "application_root": root}})
	if _, err := owner.ExecContext(ctx, `UPDATE download_tasks SET state='completed',frozen_input=$2::jsonb,provider_correlation=$3::jsonb WHERE id=$1`, p.TaskID, frozen, correlation); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.ExecContext(ctx, `INSERT INTO artifacts(id,download_task_id,acquisition_id,inventory_snapshot,fingerprint_snapshot,state,correlation_id) VALUES('handler-artifact',$1,$2,'{}','{}','pending','handler-correlation')`, p.TaskID, p.AcquisitionID); err != nil {
		t.Fatal(err)
	}
	var id string
	if err := db.QueryRowContext(ctx, `SELECT public.work_item_create('handler-work','artifact','handler-artifact','artifact_inspect','handler-audit',3,'{}',1,0::double precision,7::smallint)`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	store := &workitems.Store{DB: db, RetentionDays: 7}
	c, err := store.ClaimDue(ctx, "handler", []string{workitems.KindArtifactInspect}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	service, err := artifact.NewService(db, inspectionFixtureInspector{}, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	service.SetSettleDelay(0)
	if _, err := owner.ExecContext(ctx, `INSERT INTO media_fact_snapshots(id,release_id,schema_version,resolver_version,parser_kind,parser_config_fingerprint,input_digest,release_identity,release_facts) VALUES('handler-media',$1,'media-facts-v1','test','regex',$2,'sha256:input','{}','{"media_kind":"movie","season":0}')`, p.SourceReleaseID, parser.NilConfigFingerprint()); err != nil {
		t.Fatal(err)
	}
	service.SetMediaFactsStore(facts.NewStoreWithRepository(media.NewRepository(db)))
	var beforeRevision int64
	if err := db.QueryRowContext(ctx, `SELECT facts_revision FROM acquisitions WHERE id=$1`, p.AcquisitionID).Scan(&beforeRevision); err != nil {
		t.Fatal(err)
	}
	if inject {
		if _, err := owner.ExecContext(ctx, `INSERT INTO artifact_files(id,artifact_id,acquisition_id,file_ref,relative_path,source_path,media_kind,size_bytes,fingerprint,facts,subtitle_binding_state) VALUES('handler-stale','handler-artifact',$1,'stale','old.mkv','/old/old.mkv','video',99,'{"value":"old"}','{"old":true}','not_applicable')`, p.AcquisitionID); err != nil {
			t.Fatal(err)
		}
		if _, err := owner.ExecContext(ctx, `CREATE FUNCTION reject_inspection_activity() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected activity failure'; END $$; CREATE TRIGGER reject_inspection_activity BEFORE INSERT ON subscription_log_entries FOR EACH ROW EXECUTE FUNCTION reject_inspection_activity()`); err != nil {
			t.Fatal(err)
		}
	}
	result, err := service.Handle(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	if result.Kind != worker.OutcomeCommitted || result.OwnerResult.State != "verified" {
		t.Fatalf("typed handler result=%+v", result)
	}
	var invalidSnapshots int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM artifacts a CROSS JOIN LATERAL jsonb_array_elements(a.inventory_snapshot->'files') f WHERE a.id='handler-artifact' AND NOT EXISTS(SELECT 1 FROM artifact_files af WHERE af.artifact_id=a.id AND af.file_ref=f->>'file_ref')`).Scan(&invalidSnapshots); err != nil {
		t.Fatal(err)
	}
	if invalidSnapshots != 0 {
		t.Fatalf("verified inventory contains %d file references absent from committed file set", invalidSnapshots)
	}
	var mediaCount int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM media_fact_files m JOIN artifact_files f ON f.file_ref=m.file_ref WHERE m.snapshot_id='handler-media' AND f.artifact_id='handler-artifact'`).Scan(&mediaCount); err != nil {
		t.Fatal(err)
	}
	if mediaCount != len(manifest) {
		t.Fatalf("media facts=%d want %d", mediaCount, len(manifest))
	}
	var state string
	var version, revision int64
	var plans, logged, stale int
	if err := db.QueryRowContext(ctx, `SELECT a.state,a.version,r.facts_revision,(SELECT count(*) FROM postprocessing_plans WHERE artifact_id=a.id),(SELECT count(*) FROM subscription_log_entries WHERE subject_id=a.id),(SELECT count(*) FROM artifact_files WHERE id='handler-stale') FROM artifacts a JOIN acquisitions r ON r.id=a.acquisition_id WHERE a.id='handler-artifact'`).Scan(&state, &version, &revision, &plans, &logged, &stale); err != nil {
		t.Fatal(err)
	}
	// The inspection committed in full whether or not a run-log insert would
	// fail; the inspection itself wrote no run-log entry.
	if state != "verified" || version != 2 || revision < beforeRevision || plans != 1 || logged != 0 || stale != 0 {
		t.Fatalf("inspection state=%s version=%d revision=%d plans=%d activity=%d stale=%d", state, version, revision, plans, logged, stale)
	}
}
