package migrations

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// Migration 102 moves every stored content-digest fingerprint to the
// path-level metadata form once: artifact files take the size-only metadata
// fingerprint of their row, their file operations follow (the completed
// target from its publication proof), the plan's committed declarations keep
// comparing equal to the operation rows, media and companion digests become
// "meta:size=<n>", and a verified artifact's v1 snapshots become the v2
// summaries of its rows. Placement text and the rest of the summaries stay.
func TestPathLevelInspectionUpgradeRewritesStoredFingerprints(t *testing.T) {
	ctx := context.Background()
	db := openDisposableDB(t)
	installMigrationsThrough(t, db, "101_frozen_target_set_reference.sql")

	const tag = "up102"
	seedCompletedFileOperation(t, db, tag, "op-"+tag)
	hexA, hexB, hexC := strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64)
	sha := func(value string, size int) string {
		raw, err := json.Marshal(map[string]any{"algorithm": "sha256", "value": value, "size_bytes": size})
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	operations, err := json.Marshal([]map[string]any{
		{"id": "op-" + tag, "file_ref": "file-a", "size": 100, "fingerprint": json.RawMessage(sha(hexA, 100))},
		{"id": "op-gone", "file_ref": "file-x", "size": 7, "fingerprint": json.RawMessage(sha(hexB, 7))},
		{"id": "op-nofp", "file_ref": "file-y", "size": 3},
	})
	if err != nil {
		t.Fatal(err)
	}
	rewriteExec(t, db,
		`ALTER TABLE artifacts DISABLE TRIGGER USER`,
		`ALTER TABLE artifact_files DISABLE TRIGGER USER`,
		`ALTER TABLE file_operations DISABLE TRIGGER USER`,
		`ALTER TABLE postprocessing_plans DISABLE TRIGGER USER`,
		`UPDATE artifact_files SET size_bytes=100, fingerprint='`+sha(hexA, 100)+`'::jsonb WHERE id='af-`+tag+`'`,
		`INSERT INTO artifact_files(id, artifact_id, acquisition_id, file_ref, relative_path, source_path, media_kind, size_bytes, fingerprint, subtitle_binding_state)
		 SELECT 'af-sub-`+tag+`', artifact_id, acquisition_id, 'Sub.ass', 'Subs/ep.ass', '/downloads/`+tag+`/Subs/ep.ass', 'subtitle', 5, '`+sha(hexB, 5)+`'::jsonb, 'missing'
		 FROM artifact_files WHERE id='af-`+tag+`'`,
		`UPDATE artifacts SET
		   inventory_snapshot='{"schema_version":"artifact-inspect/v1","primary_file_ref":"ep.mkv","source":"provider","files":[{"file_ref":"ep.mkv"}],"inspected_files":[{"file_ref":"ep.mkv"}],"inspected_primary_file":{"file_ref":"ep.mkv"}}'::jsonb,
		   fingerprint_snapshot='{"schema_version":"artifact-fingerprint/v1","files":[{"file_ref":"ep.mkv","sha256":"`+hexA+`"}]}'::jsonb
		 WHERE id='art-`+tag+`'`,
		`UPDATE file_operations SET source_fingerprint='`+sha(hexA, 100)+`'::jsonb, target_fingerprint='`+sha(hexA, 100)+`'::jsonb,
		   publication_checkpoint='{"target":{"size":100,"identity":{"kind":"local_fs","device":"64768","inode":"99"}}}'::jsonb
		 WHERE id='op-`+tag+`'`,
		`UPDATE postprocessing_plans SET committed_operations='`+string(operations)+`'::jsonb WHERE id='plan-`+tag+`'`,
		`ALTER TABLE artifacts ENABLE TRIGGER USER`,
		`ALTER TABLE artifact_files ENABLE TRIGGER USER`,
		`ALTER TABLE file_operations ENABLE TRIGGER USER`,
		`ALTER TABLE postprocessing_plans ENABLE TRIGGER USER`,
		`INSERT INTO media_fact_snapshots(id, release_id, schema_version, resolver_version, parser_kind, input_digest, release_identity, release_facts)
		 VALUES ('snap-`+tag+`', 'rel-`+tag+`', 'v1', 'episode-identity-v1', 'reltitle', 'digest-`+tag+`', '{}', '{}')`,
		`INSERT INTO media_fact_files(snapshot_id, file_ref, media_kind, size_bytes, source_fingerprint, file_evidence, effective_facts)
		 VALUES ('snap-`+tag+`', 'ep.mkv', 'video', 100, '`+hexA+`', '{}', '{}'),
		        ('snap-`+tag+`', 'Sub.ass', 'subtitle', 5, NULL, '{}', '{}')`,
		`INSERT INTO placements(id, physical_path, managed_anime_id, source_evidence_type, source_owner_ref, source_fingerprint, target_fingerprint, facts_version, state)
		 VALUES ('plc-`+tag+`', '/library/ep.mkv', 'anime-`+tag+`', 'reconciliation', 'scan:`+tag+`', '`+sha(hexA, 100)+`', 'target', 'facts-v1', 'active')`,
		`INSERT INTO placement_companions(id, placement_id, managed_anime_id, source_ref, registration, media_kind, suffix, original_extension, current_path, size_bytes, fingerprint)
		 VALUES ('cmp-`+tag+`', 'plc-`+tag+`', 'anime-`+tag+`', 'src-`+tag+`', 'intake', 'subtitle', 'chs', 'ass', '/library/ep.chs.ass', 5, '`+hexC+`')`,
	)

	if err := (Runner{DB: db, Dir: migrationsDir(t)}).Apply(ctx); err != nil {
		t.Fatalf("upgrade: %v", err)
	}

	videoFingerprint := `{"algorithm":"metadata","value":"meta:size=100","size_bytes":100}`
	rewriteExpect(t, db, "upgrade", "artifact files carry the size-only metadata fingerprint of their row",
		`SELECT bool_and(fingerprint = CASE id WHEN 'af-`+tag+`' THEN '`+videoFingerprint+`'::jsonb
		                                       ELSE '{"algorithm":"metadata","value":"meta:size=5","size_bytes":5}'::jsonb END) AND count(*) = 2
		 FROM artifact_files WHERE artifact_id = 'art-`+tag+`'`)
	rewriteExpect(t, db, "upgrade", "the operation's source is its file's fingerprint and its target the published object's",
		`SELECT o.source_fingerprint = f.fingerprint
		        AND o.target_fingerprint = '{"algorithm":"metadata","value":"meta:size=100;dev=64768;ino=99","size_bytes":100}'::jsonb
		 FROM file_operations o JOIN artifact_files f ON f.id = o.artifact_file_id WHERE o.id = 'op-`+tag+`'`)
	rewriteExpect(t, db, "upgrade", "committed declarations follow their operation, fall back to their size, or stay",
		`SELECT committed_operations = '[
		   {"id":"op-`+tag+`","file_ref":"file-a","size":100,"fingerprint":`+videoFingerprint+`},
		   {"id":"op-gone","file_ref":"file-x","size":7,"fingerprint":{"algorithm":"metadata","value":"meta:size=7","size_bytes":7}},
		   {"id":"op-nofp","file_ref":"file-y","size":3}]'::jsonb
		 FROM postprocessing_plans WHERE id = 'plan-`+tag+`'`)
	rewriteExpect(t, db, "upgrade", "a media digest becomes the size-only value; a missing one stays missing",
		`SELECT bool_and(CASE file_ref WHEN 'ep.mkv' THEN source_fingerprint = 'meta:size=100' ELSE source_fingerprint IS NULL END)
		 FROM media_fact_files WHERE snapshot_id = 'snap-`+tag+`'`)
	rewriteExpect(t, db, "upgrade", "a companion digest becomes the size-only value",
		`SELECT fingerprint = 'meta:size=5' FROM placement_companions WHERE id = 'cmp-`+tag+`'`)
	rewriteExpect(t, db, "upgrade", "placement fingerprint text is kept",
		`SELECT source_fingerprint = '`+sha(hexA, 100)+`' AND target_fingerprint = 'target' FROM placements WHERE id = 'plc-`+tag+`'`)
	rewriteExpect(t, db, "upgrade", "the rewrite guards are enabled again",
		`SELECT count(*) = 0 FROM pg_trigger WHERE NOT tgisinternal AND tgenabled <> 'O'
		   AND tgrelid IN ('file_operations'::regclass, 'postprocessing_plans'::regclass)`)

	files := []map[string]any{
		{"file_ref": "ep.mkv", "relative_path": "ep.mkv", "media_kind": "video", "size_bytes": 100, "fingerprint": metadataFingerprintFixture(100)},
		{"file_ref": "Sub.ass", "relative_path": "Subs/ep.ass", "media_kind": "subtitle", "size_bytes": 5, "fingerprint": metadataFingerprintFixture(5)},
	}
	digest := inspectionFilesDigestFixture(t, files)
	var inventory, fingerprints map[string]any
	var rawInventory, rawFingerprints []byte
	if err := db.QueryRowContext(ctx, `SELECT inventory_snapshot, fingerprint_snapshot FROM artifacts WHERE id = 'art-`+tag+`'`).Scan(&rawInventory, &rawFingerprints); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(rawInventory, &inventory); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(rawFingerprints, &fingerprints); err != nil {
		t.Fatal(err)
	}
	wantInventory := map[string]any{
		"schema_version": "artifact-inspect/v2", "primary_file_ref": "ep.mkv", "source": "provider",
		"file_count": float64(2), "video_count": float64(1), "subtitle_count": float64(1), "other_count": float64(0),
		"total_size_bytes": float64(105), "files_digest": digest,
	}
	wantFingerprints := map[string]any{"schema_version": "artifact-fingerprint/v2", "primary_file_ref": "ep.mkv", "file_count": float64(2), "files_digest": digest}
	if !sameJSON(inventory, wantInventory) || !sameJSON(fingerprints, wantFingerprints) {
		t.Fatalf("summaries after the upgrade:\n inventory %s\n fingerprints %s\nwant digest %s", rawInventory, rawFingerprints, digest)
	}

	if _, err := db.ExecContext(ctx, `INSERT INTO placement_companions(id, placement_id, managed_anime_id, source_ref, registration, media_kind, suffix, original_extension, current_path, size_bytes, fingerprint)
VALUES ('cmp-hex', 'plc-`+tag+`', 'anime-`+tag+`', 'src-hex', 'intake', 'subtitle', 'cht', 'ass', '/library/ep.cht.ass', 5, '`+hexC+`')`); err == nil {
		t.Fatal("a companion with a content digest fingerprint was accepted after the upgrade")
	}
}

func sameJSON(got, want map[string]any) bool {
	a, errA := json.Marshal(got)
	b, errB := json.Marshal(want)
	return errA == nil && errB == nil && string(a) == string(b)
}
