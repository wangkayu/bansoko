//go:build linux

package application

import (
	"context"
	"database/sql"
	"errors"
	"github.com/jackc/pgx/v5/pgconn"
	"strings"
	"testing"
	"time"

	protocol "bansoko/internal/acquisition/terminal/domain"
)

// A contradictory terminal proof supplements conflict evidence, never replaces
// the original successful proof. Two invocation paths share the SQL recorder.
func verifyTerminalDigestMatrix(t *testing.T, ctx context.Context, s *Service, db *sql.DB, permit string) {
	t.Helper()
	snapshot := func() string {
		var raw string
		if err := db.QueryRowContext(ctx, `SELECT jsonb_build_object('checkpoint',publication_step_checkpoint,'bytes',publication_step_proof_bytes,'closure',publication_closure_canonical,'resolution',publication_resolution_canonical)::text FROM file_operations e WHERE publication_permit_id=$1`, permit).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		return raw
	}
	original := snapshot()
	var count int
	if err := db.QueryRowContext(ctx, `SELECT publication_conflict_count FROM file_operations WHERE publication_permit_id=$1`, permit).Scan(&count); err != nil {
		t.Fatal(err)
	}
	for _, step := range []string{"stage_temp", "publish_target", "source_cleanup", "temp_cleanup"} {
		fields := []string{"mutation_receipt_digest"}
		if step == "stage_temp" {
			fields = []string{"verified_source_digest"}
		}
		if step == "publish_target" {
			fields = []string{"source_evidence_digest", "dependency_digest"}
		}
		for _, field := range fields {
			for _, direct := range []bool{false, true} {
				base, _, _, err := s.publicationReceiptBase(ctx, permit)
				if err != nil {
					t.Fatal(err)
				}
				slot := base["payload"].(map[string]any)["checkpoint"].(map[string]any)[step].(map[string]any)
				if slot["state"] != "confirmed" {
					continue
				}
				digit := "1"
				if direct {
					digit = "2"
				}
				slot["proof"].(map[string]any)[field] = "sha256:" + strings.Repeat(digit, 64)
				base["observed_at"] = observationTime(time.Now().UTC())
				receipt, err := publicationEnvelope(base)
				if err != nil {
					t.Fatal(err)
				}
				record := func() error {
					if direct {
						_, err := db.ExecContext(ctx, `SELECT public.file_record_receipt($1,$2)`, permit, string(protocol.Canonical(receipt)))
						return err
					}
					return s.recordFileReceipt(ctx, permit, receipt)
				}
				if err := record(); err != nil {
					t.Fatalf("terminal %s/%s direct=%v: %v", step, field, direct, err)
				}
				count++
				var got int
				if err := db.QueryRowContext(ctx, `SELECT publication_conflict_count FROM file_operations WHERE publication_permit_id=$1`, permit).Scan(&got); err != nil || got != count {
					t.Fatalf("terminal conflict count=%d want=%d err=%v", got, count, err)
				}
				if snapshot() != original {
					t.Fatal("terminal digest replaced original proof or binding")
				}
				before := planGraphSnapshot(t, db)
				if err := record(); err != nil {
					t.Fatal(err)
				}
				if planGraphSnapshot(t, db) != before {
					t.Fatal("terminal digest replay wrote graph")
				}
				beforeLoader := planGraphSnapshot(t, db)
				_, loaderErr := ownerTestDB(t).ExecContext(ctx, `SELECT file_assert_final_proof($1)`, permit)
				var loaderRejected *pgconn.PgError
				if !errors.As(loaderErr, &loaderRejected) || loaderRejected.Code != "23514" || loaderRejected.Message != "FILE_FINAL_PROOF_UNAVAILABLE" {
					t.Fatalf("fixed loader conflict=%v", loaderErr)
				}
				if planGraphSnapshot(t, db) != beforeLoader {
					t.Fatal("fixed loader conflict wrote graph")
				}
				if _, err := s.loadPublicationProof(ctx, permit); err == nil {
					t.Fatal("conflicted proof accepted as success")
				}
			}
		}
	}
}
