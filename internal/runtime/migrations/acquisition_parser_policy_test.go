package migrations

import (
	"context"
	"strings"
	"testing"
)

func TestAcquisitionParserPolicyRecheck(t *testing.T) {
	owner, runtime := setupQualification(t)
	f := seedQualification(t, owner, "parser-recheck")
	ctx := context.Background()
	if _, err := recheckQualification(runtime, f.qualification(nil), acqDigest("6"), f.businessDigest, f.payloadDigest); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.ExecContext(ctx, `UPDATE anime_business_configs SET parser_version=parser_version+1 WHERE managed_anime_id=$1`, f.animeID); err != nil {
		t.Fatal(err)
	}
	if _, err := recheckQualification(runtime, f.qualification(nil), acqDigest("6"), f.businessDigest, f.payloadDigest); err == nil || !strings.Contains(err.Error(), "ACQ_QUALIFICATION_PARSER_CHANGED") {
		t.Fatalf("changed parser accepted: %v", err)
	}
	if _, err := recheckQualification(runtime, f.qualification(func(q map[string]any) { q["management"].(map[string]any)["expected_parser_policy_version"] = 2 }), acqDigest("6"), f.businessDigest, f.payloadDigest); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := owner.QueryRowContext(ctx, `SELECT count(*) FROM acquisitions`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("qualification wrote roots: %d %v", count, err)
	}
}
