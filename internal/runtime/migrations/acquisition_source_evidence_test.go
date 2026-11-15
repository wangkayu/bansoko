package migrations

import (
	"context"
	"encoding/json"
	"testing"
)

func TestAcquisitionSourceEvidenceRecheckedBeforeAccept(t *testing.T) {
	db := installedDB(t)
	seedAcceptableRelease(t, db, "source-freeze")
	p := validQueued(t, "acq-source-freeze", "key-source-freeze", "source-freeze")
	// Preserve the release version to exercise the evidence comparison itself.
	if _, err := db.ExecContext(context.Background(), `UPDATE releases SET internal_facts='{"channel":"jackett","details_url":"https://tracker.example.test/details/1"}' WHERE id=$1`, p.SourceReleaseID); err != nil {
		t.Fatal(err)
	}
	if _, err := callQueuedAccept(t, db, p); adoptAcceptPGMessage(err) != "ACQ_QUALIFICATION_SOURCE_CHANGED" {
		t.Fatalf("changed provenance accepted: %v", err)
	}
	var evidence json.RawMessage
	if err := db.QueryRowContext(context.Background(), `SELECT release_source_evidence($1)`, p.SourceReleaseID).Scan(&evidence); err != nil {
		t.Fatal(err)
	}
	var input map[string]any
	if err := json.Unmarshal([]byte(p.InputCanonical), &input); err != nil {
		t.Fatal(err)
	}
	input["source"].(map[string]any)["provenance"] = evidence
	p.InputCanonical = canonicalJSON(t, input)
	p.AdmissionDigest = acqDomainDigest("acquisition-input:v1", []byte(p.InputCanonical))
	if _, err := callQueuedAccept(t, db, p); err != nil {
		t.Fatalf("recaptured provenance refused: %v", err)
	}
	if _, err := db.ExecContext(context.Background(), `UPDATE releases SET internal_facts='{}' WHERE id=$1`, p.SourceReleaseID); err != nil {
		t.Fatal(err)
	}
	// Same accepted request must replay its receipt, not revalidate mutable evidence.
	if _, err := callQueuedAccept(t, db, p); err != nil {
		t.Fatalf("accepted replay consulted changed source: %v", err)
	}
	var provider string
	if err := db.QueryRowContext(context.Background(), `SELECT input->'source'->'provenance'->>'provider' FROM acquisitions WHERE id=$1`, p.AcquisitionID).Scan(&provider); err != nil {
		t.Fatal(err)
	}
	if provider != "jackett" {
		t.Fatalf("accepted source=%q", provider)
	}
}
