package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"

	"bansoko/internal/workitems"
	"github.com/jackc/pgx/v5/pgconn"
)

// Owner invocation verifies only this private projection component. Runtime
// provenance must be established separately by the complete fixed finish command.
func verifyFileSuccessProjection(t *testing.T, ctx context.Context, db *sql.DB, c workitems.Claim, p preparedFileResult) {
	t.Helper()
	before := planGraphSnapshot(t, db)
	owner := ownerTestDB(t)
	_, err := db.ExecContext(ctx, `SELECT public.file_success_projection($1,$2,$3)`, p.operation, p.permit, p.fileFactCanonical)
	var pe *pgconn.PgError
	if !errors.As(err, &pe) || pe.Code != "42501" {
		t.Fatalf("projection runtime ACL=%v", err)
	}
	for _, payload := range []string{`{"token":"redacted"}`, `{"nested":{"credential":"redacted"}}`, `{"nested":[{"label":"https://example.invalid"}]}`, `{"nested":{"callback_url":null}}`} {
		negative, err := owner.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		_, err = negative.ExecContext(ctx, `SELECT public.file_finish_success($1,$2,$3,$4,$5,$6)`, p.operation, c.ID, c.FencingToken, p.permit, p.fileFactCanonical, payload)
		negative.Rollback()
		var rejected *pgconn.PgError
		if !errors.As(err, &rejected) || rejected.Code != "23514" || rejected.Message != "FILE_NOTIFICATION_INVALID" {
			logFileFinalizationState(t, ctx, db, c, p)
			t.Fatalf("unsafe payload rejection=%v", err)
		}
		if planGraphSnapshot(t, db) != before {
			t.Fatal("unsafe notification changed graph")
		}
	}
	for _, field := range []string{"event.type", "result.outcome", "result.reason", "anime.id", "anime.title_override", "op.kind", "op.conflict", "file.size", "anime.title", "release.id", "release.title", "anime.group", "release.src", "file.name", "file.stem", "file.ext", "file.kind", "media.container", "media.resolution", "media.video_codec", "media.audio_codec", "out.dir", "out.name", "out.path", "bgm.id", "tmdb.id", "tmdb.kind", "tmdb.season", "torrent.hash_v1", "torrent.hash_v2", "ep.raw", "ep.number", "ep.display", "ep.offset", "unexpected_field"} {
		var payload map[string]any
		if err := json.Unmarshal([]byte(p.notificationPayload), &payload); err != nil {
			t.Fatal(err)
		}
		payload[field] = "mismatch"
		encoded, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		negative, err := owner.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		_, err = negative.ExecContext(ctx, `SELECT public.file_finish_success($1,$2,$3,$4,$5,$6)`, p.operation, c.ID, c.FencingToken, p.permit, p.fileFactCanonical, string(encoded))
		negative.Rollback()
		var rejected *pgconn.PgError
		if !errors.As(err, &rejected) || rejected.Code != "23514" || rejected.Message != "FILE_NOTIFICATION_PROOF_MISMATCH" {
			logFileFinalizationState(t, ctx, db, c, p)
			t.Fatalf("field %s mismatch=%v", field, err)
		}
		if planGraphSnapshot(t, db) != before {
			t.Fatal("notification mismatch wrote graph")
		}
	}
	var reconstructed bool
	if err := owner.QueryRowContext(ctx, `SELECT public.file_success_notification($1,$2,$3::jsonb)=$4::jsonb`, p.operation, p.permit, p.fileFactCanonical, p.notificationPayload).Scan(&reconstructed); err != nil || !reconstructed {
		t.Fatalf("SQL frozen notification parity=%v err=%v", reconstructed, err)
	}
	tx, err := owner.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `SELECT public.file_finalization_normal_gate($1,$2,$3,$4)`, c.Owner.ID, c.ID, c.FencingToken, p.permit); err != nil {
		t.Fatal(err)
	}
	if err := verifyPreparedFileResultTx(ctx, tx, c, p); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `SELECT public.file_finish_success($1,$2,$3,$4,$5,$6)`, p.operation, c.ID, c.FencingToken, p.permit, p.fileFactCanonical, p.notificationPayload); err != nil {
		t.Fatal(err)
	}
	var valid bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM public.acquisition_files WHERE acquisition_id=$1 AND file_ref=$2 AND operation_id=$3 AND outcome='succeeded' AND completion_mode='executed' AND effect_class='confirmed' AND final_object=$4::jsonb AND evidence_digest=$5 AND delivery_source_cleanup=$6)`, p.root, p.fileRef, p.operation, p.finalObjectCanonical, p.evidenceDigest, p.sourceCleanup).Scan(&valid); err != nil || !valid {
		t.Fatalf("projected result valid=%v err=%v", valid, err)
	}
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM public.work_item_outcomes WHERE id=$1 AND state='succeeded') AND EXISTS(SELECT 1 FROM public.notification_admissions WHERE source_owner_id=$2 AND managed_anime_id IS NULL AND payload=$3::jsonb) AND EXISTS(SELECT 1 FROM public.work_items WHERE acquisition_id=$4 AND kind='acquisition_settle' AND state IN ('available','leased'))`, c.ID, p.operation, p.notificationPayload, p.root).Scan(&valid); err != nil || !valid {
		t.Fatalf("atomic bookkeeping=%v err=%v", valid, err)
	}
	if _, err := tx.ExecContext(ctx, `SET CONSTRAINTS ALL IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	tx.Rollback()
	if planGraphSnapshot(t, db) != before {
		t.Fatal("rolled back projection changed graph")
	}
}

func logFileFinalizationState(t *testing.T, ctx context.Context, db *sql.DB, c workitems.Claim, p preparedFileResult) {
	t.Helper()
	var snapshot string
	err := db.QueryRowContext(ctx, `SELECT jsonb_build_object('now',clock_timestamp(),'budget_clock',o.budget_clock_at,'wall',o.file_wall_until,'operation',to_jsonb(o),'work',to_jsonb(w),'evidence_state',e.publication_dispatch_state,'effect',e.publication_effect_class,'conflict',e.publication_conflict_evidence,'evidence_token',e.publication_fencing_token,'evidence_owner_version',e.publication_owner_version,'resolution_present',e.publication_resolution_canonical IS NOT NULL,'closure_present',e.publication_closure_canonical IS NOT NULL)::text FROM file_operations o JOIN work_item_history w ON w.id=$2 JOIN file_operations e ON e.publication_permit_id=$3 WHERE o.id=$1`, c.Owner.ID, c.ID, p.permit).Scan(&snapshot)
	t.Logf("finalization diagnostic claim_token=%d claim_version=%d snapshot=%s err=%v", c.FencingToken, c.ExpectedOwnerVersion, snapshot, err)
}
