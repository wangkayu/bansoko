package domain

import (
	"strings"
	"testing"
)

func TestFileReceiptTransitionRequiresPersistedGrants(t *testing.T) {
	identity := parse(t, `{"schema_version":1,"permit_id":"permit","root_id":"root","original_work_id":"work","fencing_token":1,"executor_session":"session","spec_digest":"sha256:`+strings.Repeat("0", 64)+`"}`)
	empty := `{"state":"not_started","proof":null,"observed_at":null,"reason_code":null}`
	checkpoint := `{"stage_temp":` + empty + `,"reserve_version":` + empty + `,"publish_target":` + empty + `,"source_cleanup":` + empty + `,"temp_cleanup":` + empty + `}`
	previous := parse(t, checkpoint)
	envelope := parse(t, `{"identity":`+string(Canonical(identity))+`,"emitter":{"mode":"dispatch_session","session":"session"},"observed_at":"2026-10-08T00:00:02.000001Z","payload":{"checkpoint":`+checkpoint+`,"closure":{"state":"unavailable","value":null,"reason_code":"PROOF_UNAVAILABLE"}}}`)
	if err := ValidateFileReceiptTransition(envelope, identity, previous); err != nil {
		t.Fatal(err)
	}
	envelope.Object["payload"].Object["checkpoint"].Object["stage_temp"] = parse(t, `{"state":"started","proof":null,"observed_at":"2026-10-08T00:00:01.000001Z","reason_code":null}`)
	if err := ValidateFileReceiptTransition(envelope, identity, previous); err == nil {
		t.Fatal("receipt granted its own step")
	}
	previous.Object["stage_temp"] = envelope.Object["payload"].Object["checkpoint"].Object["stage_temp"]
	if err := ValidateFileReceiptTransition(envelope, identity, previous); err != nil {
		t.Fatal(err)
	}
	envelope.Object["payload"].Object["closure"] = parse(t, `{"state":"known","value":{"kind":"local_join","executor_session":"session","boot_nonce":"boot","call_nonce":"call","covered_steps":[],"joined_at":"2026-10-08T00:00:02.000001Z","observer_version":"executor-v1"},"reason_code":null}`)
	if err := ValidateFileReceiptTransition(envelope, identity, previous); err == nil {
		t.Fatal("closure ignored granted step")
	}
	envelope.Object["payload"].Object["closure"].Object["value"].Object["covered_steps"] = parse(t, `["stage_temp"]`)
	if err := ValidateFileReceiptTransition(envelope, identity, previous); err != nil {
		t.Fatal(err)
	}
	envelope.Object["emitter"] = parse(t, `{"mode":"lookup_ticket","ticket_id":"lookup","ordinal":1,"session":"lookup-session"}`)
	if err := ValidateFileReceiptTransition(envelope, identity, previous); err == nil {
		t.Fatal("lookup join closed original mutation")
	}
}
