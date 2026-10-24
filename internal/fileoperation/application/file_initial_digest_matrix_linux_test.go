//go:build linux

package application

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	protocol "bansoko/internal/acquisition/terminal/domain"
	"bansoko/internal/library/domain"
	"bansoko/internal/workitems"
	"github.com/jackc/pgx/v5/pgconn"
)

// Intercept only test receipts, after real I/O and before the real recorder.
// The original receipt is recorded exactly once after rejected mutations.
func digestMatrixCallbacks(t *testing.T, s *Service, db *sql.DB, c workitems.Claim, permit string, op domain.FileOperation) domain.TypedPublicationCallbacks {
	callbacks := s.typedPublicationCallbacks(c, permit, op)
	original := callbacks.RecordStep
	callbacks.RecordStep = func(ctx context.Context, session domain.PublicationSession, r domain.PublicationStepReceipt) error {
		if r.Err != nil {
			return original(ctx, session, r)
		}
		fields := []string{}
		message := ""
		switch r.Step {
		case "stage_temp":
			fields = []string{"verified_source_digest"}
			message = "FILE_RECEIPT_TEMP_BINDING"
		case "publish_target":
			fields = []string{"source_evidence_digest", "dependency_digest"}
			message = "FILE_RECEIPT_PUBLICATION_BINDING"
		case "source_cleanup", "temp_cleanup":
			fields = []string{"mutation_receipt_digest", "before", "parent", "after_parent", "after_location", "after_method", "after_extra"}
			message = "FILE_RECEIPT_CLEANUP_BINDING"
		}
		for _, field := range fields {
			base, objects, dependency, err := s.publicationReceiptBase(ctx, permit)
			if err != nil {
				t.Fatal(err)
			}
			proof, err := publicationStepProof(base, objects, dependency, op, r)
			if err != nil {
				t.Fatal(err)
			}
			encodedProof, err := json.Marshal(proof)
			if err != nil {
				t.Fatal(err)
			}
			decoder := json.NewDecoder(bytes.NewReader(encodedProof))
			decoder.UseNumber()
			if err := decoder.Decode(&proof); err != nil {
				t.Fatal(err)
			}
			switch field {
			case "before":
				proof[field].(map[string]any)["size"] = int64(-1)
			case "parent":
				parent := proof[field].(map[string]any)
				parent["inode"] = parent["inode"].(string) + "0"
			case "after_parent":
				parent := proof["after"].(map[string]any)["parent"].(map[string]any)
				parent["inode"] = parent["inode"].(string) + "0"
			case "after_location":
				proof["after"].(map[string]any)["location"].(map[string]any)["canonical_path"] = "/foreign"
			case "after_method":
				proof["after"].(map[string]any)["method"] = "foreign"
			case "after_extra":
				proof["after"].(map[string]any)["extra"] = true
			default:
				proof[field] = "sha256:" + strings.Repeat("0", 64)
			}
			at := observationTime(r.ObservedAt)
			base["observed_at"] = at
			base["payload"].(map[string]any)["checkpoint"].(map[string]any)[r.Step] = map[string]any{"state": "confirmed", "proof": proof, "observed_at": at, "reason_code": nil}
			encoded, err := json.Marshal(base)
			if err != nil {
				t.Fatal(err)
			}
			receipt, err := protocol.ParseStrict(encoded)
			if err != nil {
				t.Fatal(err)
			}
			before := planGraphSnapshot(t, db)
			// Go wire/transition validation delegates semantic object digest binding
			// to SQL; the application call must surface the same SQL rejection.
			err = s.recordFileReceipt(ctx, permit, receipt)
			expectedSQL := message
			if field == "before" || field == "after_location" {
				expectedSQL = "FILE_RECEIPT_OBJECT_INVALID"
			}
			assert := func(err error) {
				var pg *pgconn.PgError
				if !errors.As(err, &pg) || pg.Code != "23514" || pg.Message != expectedSQL {
					t.Fatalf("initial %s/%s rejection=%v", r.Step, field, err)
				}
				if planGraphSnapshot(t, db) != before {
					t.Fatal("initial digest rejection wrote graph")
				}
			}
			if field == "before" || field == "after_method" || field == "after_extra" {
				var rejected *protocol.Error
				expected := protocol.CodeConstraintViolated
				if field == "after_method" {
					expected = protocol.CodeUnionBranchInvalid
				}
				if field == "after_extra" {
					expected = protocol.CodeUnknownKey
				}
				if !errors.As(err, &rejected) || rejected.Code != expected {
					t.Fatalf("cleanup schema %s=%v", field, err)
				}
				if planGraphSnapshot(t, db) != before {
					t.Fatal("cleanup schema refusal wrote graph")
				}
			} else {
				assert(err)
			}
			_, err = db.ExecContext(ctx, `SELECT public.file_record_receipt($1,$2)`, permit, string(protocol.Canonical(receipt)))
			assert(err)
		}
		return original(ctx, session, r)
	}
	return callbacks
}
