package migrations

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"bansoko/internal/workitems"
)

func TestFileInspectionReplacesExistingArtifactFiles(t *testing.T) {
	scenarios := []string{"pending", "waiting_retry_bound", "plan", "wrong_token", "expired", "cancel", "version", "inventory_count", "inventory_video_count", "inventory_list", "inventory_digest", "files_duplicate", "primary", "path", "size", "fingerprint_sha256", "fingerprint_size", "fingerprint_identity", "fingerprint_missing", "fingerprint_count", "fingerprint_extra", "fingerprint_digest", "media_duplicate", "media_missing", "media_extra", "media_identity", "media_binding", "media_size", "media_kind", "media_fingerprint", "rollback_media", "manifest", "rollback_delete", "rollback_insert", "rollback_plan", "rollback_work", "rollback_facts"}
	for _, scenario := range scenarios {
		t.Run(scenario, func(t *testing.T) {
			owner, db, p := progressProbe(t, "replace-"+scenario, nil, nil)
			ctx := context.Background()
			must := func(q string, args ...any) {
				t.Helper()
				if _, err := owner.ExecContext(ctx, q, args...); err != nil {
					t.Fatal(err)
				}
			}
			must(`UPDATE download_tasks SET state='completed' WHERE id=$1`, p.TaskID)
			state := "pending"
			if scenario == "waiting_retry_bound" {
				state = "waiting_retry"
			}
			must(`INSERT INTO artifacts(id,download_task_id,acquisition_id,inventory_snapshot,fingerprint_snapshot,state) VALUES('replace-artifact',$1,$2,'{"old":true}','{"old":true}',$3)`, p.TaskID, p.AcquisitionID, state)
			var manifest []map[string]any
			if err := json.Unmarshal([]byte(p.ManifestFiles), &manifest); err != nil {
				t.Fatal(err)
			}
			files := make([]map[string]any, 0, len(manifest))
			for i, f := range manifest {
				files = append(files, map[string]any{"id": fmt.Sprintf("replace-new-%d", i), "file_ref": f["file_ref"], "relative_path": f["relative_path"], "source_path": "/downloads/" + f["relative_path"].(string), "size_bytes": f["exact_size"], "media_kind": f["media_kind"], "fingerprint": metadataFingerprintFixture(f["exact_size"]), "facts": map[string]any{}})
			}
			oldRef := "old-video"
			if scenario == "waiting_retry_bound" {
				oldRef = files[0]["file_ref"].(string)
			}
			must(`INSERT INTO artifact_files(id,artifact_id,acquisition_id,file_ref,relative_path,source_path,media_kind,size_bytes,fingerprint,facts,subtitle_binding_state) VALUES('replace-old','replace-artifact',$1,$2,'old.mkv','/old/old.mkv','video',99,'{"algorithm":"metadata","value":"meta:size=99","size_bytes":99}','{"old":true}','not_applicable')`, p.AcquisitionID, oldRef)
			if scenario == "waiting_retry_bound" {
				must(`INSERT INTO artifact_files(id,artifact_id,acquisition_id,file_ref,relative_path,source_path,media_kind,size_bytes,fingerprint,subtitle_binding_state,bound_video_file_ref) VALUES('replace-old-sub','replace-artifact',$1,'old-sub','old.ass','/old/old.ass','subtitle',9,'{}','bound',$2)`, p.AcquisitionID, oldRef)
			}
			var id string
			if err := db.QueryRowContext(ctx, `SELECT work_item_create('replace-work','artifact','replace-artifact','artifact_inspect','replace-work',3,'{}',1,0::double precision,7::smallint)`).Scan(&id); err != nil {
				t.Fatal(err)
			}
			store := &workitems.Store{DB: db, RetentionDays: 7}
			claim, err := store.ClaimDue(ctx, "replace", []string{workitems.KindArtifactInspect}, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			inventory, fingerprints := inspectionSnapshotFixture(t, files)
			var inv, fp map[string]any
			if err := json.Unmarshal([]byte(inventory), &inv); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(fingerprints), &fp); err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "plan":
				must(`INSERT INTO postprocessing_plans(id,artifact_id,acquisition_id,artifact_version,input_digest,state) VALUES('old-plan','replace-artifact',$1,1,$2,'queued')`, p.AcquisitionID, p.AdmissionDigest)
			case "wrong_token":
				claim.FencingToken++
			case "expired":
				must(`UPDATE work_items SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, claim.ID)
			case "cancel":
				must(`UPDATE work_items SET cancel_requested=true WHERE id=$1`, claim.ID)
			case "version":
				must(`UPDATE artifacts SET version=version+1 WHERE id='replace-artifact'`)
			case "inventory_count":
				inv["file_count"] = inv["file_count"].(float64) + 1
			case "inventory_video_count":
				inv["video_count"] = inv["video_count"].(float64) + 1
			case "inventory_list":
				inv["files"] = []any{}
			case "inventory_digest":
				inv["files_digest"] = "sha256:" + strings.Repeat("0", 64)
			case "primary":
				inv["primary_file_ref"] = "old-video"
			case "path":
				// The committed files no longer match the summarized ones.
				files[0]["relative_path"] = "old.mkv"
			case "size":
				inv["total_size_bytes"] = inv["total_size_bytes"].(float64) + 99
			case "fingerprint_sha256":
				files[0]["fingerprint"] = map[string]any{"algorithm": "sha256", "value": strings.Repeat("c", 64), "size_bytes": files[0]["size_bytes"]}
			case "fingerprint_size":
				files[0]["fingerprint"] = map[string]any{"algorithm": "metadata", "value": "meta:size=1", "size_bytes": files[0]["size_bytes"]}
			case "fingerprint_identity":
				files[0]["fingerprint"] = map[string]any{"algorithm": "metadata", "value": fmt.Sprintf("meta:size=%d;dev=1", fixtureSize(files[0]["size_bytes"])), "size_bytes": files[0]["size_bytes"]}
			case "fingerprint_missing":
				delete(fp, "files_digest")
			case "files_duplicate":
				files[1] = files[0]
			case "fingerprint_count":
				fp["file_count"] = fp["file_count"].(float64) + 1
			case "fingerprint_extra":
				fp["files"] = map[string]any{}
			case "fingerprint_digest":
				fp["files_digest"] = "sha256:" + strings.Repeat("0", 64)
			case "manifest":
				files[0]["size_bytes"] = 999
				inventory, fingerprints = inspectionSnapshotFixture(t, files)
				if err := json.Unmarshal([]byte(inventory), &inv); err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal([]byte(fingerprints), &fp); err != nil {
					t.Fatal(err)
				}
			}
			invRaw, err := json.Marshal(inv)
			if err != nil {
				t.Fatal(err)
			}
			inventory = string(invRaw)
			fpRaw, err := json.Marshal(fp)
			if err != nil {
				t.Fatal(err)
			}
			fingerprints = string(fpRaw)
			body, _ := json.Marshal(files)
			mediaID := ""
			mediaFiles := []map[string]any{}
			if strings.HasPrefix(scenario, "media_") || scenario == "rollback_media" {
				mediaID = "replace-media"
				must(`INSERT INTO media_fact_snapshots(id,release_id,schema_version,resolver_version,parser_kind,input_digest,release_identity,release_facts) VALUES('replace-media',$1,'media-facts-v1','test','regex','sha256:input','{}','{}')`, p.SourceReleaseID)
				var binding json.RawMessage
				if err := owner.QueryRowContext(ctx, `SELECT jsonb_build_object('release_facts',release_facts,'release_identity',release_identity,'parser_kind',parser_kind,'parser_config',parser_config,'parser_config_fingerprint',parser_config_fingerprint) FROM media_fact_snapshots WHERE id='replace-media'`).Scan(&binding); err != nil {
					t.Fatal(err)
				}
				for _, f := range files {
					mediaFiles = append(mediaFiles, map[string]any{"file_ref": f["file_ref"], "media_kind": f["media_kind"], "size_bytes": f["size_bytes"], "source_fingerprint": f["fingerprint"].(map[string]any)["value"], "file_evidence": map[string]any{}, "effective_facts": map[string]any{}, "snapshot_binding": binding})
				}
				switch scenario {
				case "media_duplicate":
					mediaFiles[1] = mediaFiles[0]
				case "media_missing":
					mediaFiles = mediaFiles[:1]
				case "media_extra":
					mediaFiles = append(mediaFiles, mediaFiles[0])
				case "media_identity":
					mediaID = "foreign-snapshot"
				case "media_binding":
					mediaFiles[0]["snapshot_binding"] = map[string]any{}
				case "media_size":
					mediaFiles[0]["size_bytes"] = 999
				case "media_kind":
					mediaFiles[0]["media_kind"] = "other"
				case "media_fingerprint":
					mediaFiles[0]["source_fingerprint"] = "meta:size=1"
				}
			}
			mediaRaw, err := json.Marshal(mediaFiles)
			if err != nil {
				t.Fatal(err)
			}
			before := func() string {
				t.Helper()
				var raw string
				if err := owner.QueryRowContext(ctx, `SELECT jsonb_build_object('artifact',to_jsonb(a),'files',(SELECT jsonb_agg(to_jsonb(f) ORDER BY f.id) FROM artifact_files f WHERE f.artifact_id=a.id),'root',to_jsonb(r),'work',to_jsonb(w),'plans',(SELECT jsonb_agg(to_jsonb(p)) FROM postprocessing_plans p WHERE p.artifact_id=a.id),'manifest',jsonb_build_object('acquisition_id',r.id,'format_version',1,'manifest_digest',r.manifest_digest,'item_count',r.manifest_item_count,'sealed_at',r.manifest_sealed_at))::text FROM artifacts a JOIN acquisitions r ON r.id=a.acquisition_id JOIN work_item_history w ON w.id='replace-work' WHERE a.id='replace-artifact'`).Scan(&raw); err != nil {
					t.Fatal(err)
				}
				return raw
			}
			prior := before()
			if strings.HasPrefix(scenario, "rollback_") {
				table, event := "artifact_files", "AFTER DELETE"
				switch scenario {
				case "rollback_insert":
					event = "AFTER INSERT"
				case "rollback_plan":
					table = "postprocessing_plans"
					event = "AFTER INSERT"
				case "rollback_work":
					table = "work_items"
					event = "AFTER INSERT"
				case "rollback_media":
					table = "media_fact_files"
					event = "AFTER INSERT"
				case "rollback_facts":
					table = "acquisitions"
					event = "AFTER UPDATE"
				}
				must(fmt.Sprintf(`CREATE FUNCTION replace_fail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'replacement injected failure'; END $$; CREATE TRIGGER replace_fail %s ON %s FOR EACH ROW EXECUTE FUNCTION replace_fail()`, event, table))
			}
			commit := func() error {
				var raw []byte
				return db.QueryRowContext(ctx, `SELECT artifact_commit_inspection($1,$2,$3,$4,$5,$6::jsonb,$7::jsonb,$8::jsonb,$9::jsonb,'replace-plan','replace-plan-work',$4,$10,$11::jsonb)`, claim.Owner.ID, claim.ID, claim.FencingToken, p.AdmissionDigest, p.ManifestDigest, p.ManifestFiles, inventory, fingerprints, body, mediaID, mediaRaw).Scan(&raw)
			}
			err = commit()
			if scenario != "pending" && scenario != "waiting_retry_bound" {
				if err == nil {
					t.Fatal("invalid replacement committed")
				}
				if strings.HasPrefix(scenario, "rollback_") && !strings.Contains(err.Error(), "replacement injected failure") {
					t.Fatal(err)
				}
				if prior != before() {
					t.Fatal("rejected replacement mutated old graph")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var count, old, plans int
			var verified bool
			if err := owner.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM artifact_files WHERE artifact_id=a.id),(SELECT count(*) FROM artifact_files WHERE id IN ('replace-old','replace-old-sub')),(SELECT count(*) FROM postprocessing_plans WHERE artifact_id=a.id),a.state='verified' FROM artifacts a WHERE id='replace-artifact'`).Scan(&count, &old, &plans, &verified); err != nil {
				t.Fatal(err)
			}
			if count != len(files) || old != 0 || plans != 1 || !verified {
				t.Fatalf("count=%d old=%d plans=%d verified=%t", count, old, plans, verified)
			}
			committed := before()
			if err := commit(); err == nil {
				t.Fatal("repeated claim committed")
			}
			if before() != committed {
				t.Fatal("repeated claim changed committed graph")
			}
		})
		if t.Failed() {
			return
		}
	}
}

var _ *sql.DB
