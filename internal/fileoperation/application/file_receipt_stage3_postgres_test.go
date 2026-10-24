package application

import (
	protocol "bansoko/internal/acquisition/terminal/domain"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5/pgconn"
	"testing"
	"time"
)

func verifyReceiptStage3(t *testing.T, ctx context.Context, db *sql.DB, permit string) {
	t.Helper()
	owner := ownerTestDB(t)
	var valid bool
	if err := owner.QueryRowContext(ctx, `SELECT publication_closure_receipt_observed_at IS NOT NULL AND (publication_closed_proof->>'joined_at')::timestamptz<=publication_closure_receipt_observed_at AND NOT EXISTS(SELECT 1 FROM jsonb_each(publication_step_checkpoint) s WHERE s.value->>'state'='confirmed' AND (file_step_authorized_at(e,s.key) IS NULL OR file_step_authorized_at(e,s.key)>(s.value->>'observed_at')::timestamptz)) FROM file_operations e WHERE publication_permit_id=$1`, permit).Scan(&valid); err != nil || !valid {
		t.Fatalf("stage3 persisted closure/authorization=%v err=%v", valid, err)
	}
	before := planGraphSnapshot(t, db)
	for _, assignment := range []string{`publication_closure_receipt_observed_at=NULL`, `publication_closure_receipt_observed_at=publication_closure_receipt_observed_at+interval '1 microsecond'`} {
		_, err := owner.ExecContext(ctx, `UPDATE file_operations SET `+assignment+` WHERE publication_permit_id=$1`, permit)
		var rejected *pgconn.PgError
		if !errors.As(err, &rejected) || rejected.Code != "23514" || rejected.Message != "FILE_STEP_AUTHORIZATION_INVALID" {
			t.Fatalf("stage3 immutable=%v", err)
		}
		if planGraphSnapshot(t, db) != before {
			t.Fatal("stage3 immutable wrote graph")
		}
	}
	s := &Service{db: db}
	for _, mode := range []string{"replay", "invalid_envelope", "join_after_envelope"} {
		base, _, _, err := s.publicationReceiptBase(ctx, permit)
		if err != nil {
			t.Fatal(err)
		}
		var closureRaw []byte
		if err := owner.QueryRowContext(ctx, `SELECT publication_closed_proof FROM file_operations WHERE publication_permit_id=$1`, permit).Scan(&closureRaw); err != nil {
			t.Fatal(err)
		}
		var closure map[string]any
		if err := json.Unmarshal(closureRaw, &closure); err != nil {
			t.Fatal(err)
		}
		base["payload"].(map[string]any)["closure"] = map[string]any{"state": "known", "value": closure, "reason_code": nil}
		base["observed_at"] = protocol.FormatTime(time.Now().UTC())
		want := ""
		if mode == "invalid_envelope" {
			base["observed_at"] = nil
			want = "FILE_RECEIPT_TIME_INVALID"
		}
		if mode == "join_after_envelope" {
			base["payload"].(map[string]any)["closure"].(map[string]any)["value"].(map[string]any)["joined_at"] = "2100-01-01T00:00:00Z"
			want = "FILE_RECEIPT_CLOSURE_INVALID"
		}
		raw, err := canonicalPlanValue(base)
		if err != nil {
			t.Fatal(err)
		}
		_, err = db.ExecContext(ctx, `SELECT file_record_receipt($1,$2)`, permit, raw)
		if want == "" {
			if err != nil {
				t.Fatal(err)
			}
		} else {
			var rejected *pgconn.PgError
			if !errors.As(err, &rejected) || rejected.Code != "23514" || rejected.Message != want {
				t.Fatalf("stage3 %s=%v", mode, err)
			}
		}
		if planGraphSnapshot(t, db) != before {
			t.Fatalf("stage3 %s wrote graph", mode)
		}
	}
}
