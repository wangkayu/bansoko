package domain

import (
	"bytes"
	"time"
)

// ValidateFileReceiptTransition validates the wire against an independently
// loaded original identity and persisted checkpoint. It is not provenance:
// only the trusted adapter recorder may submit facts, after a real return/join.
// The recorder must hold the evidence lock when applying this transition.
func ValidateFileReceiptTransition(envelope, identity, previous Value) error {
	if err := Validate("FileReceiptEnvelopeV1", envelope); err != nil {
		return err
	}
	if err := Validate("ReceiptIdentityV1", identity); err != nil {
		return err
	}
	if err := Validate("FileCheckpointV1", previous); err != nil {
		return err
	}
	fail := func() error { return reader{}.fail(CodeConstraintViolated) }
	if !bytes.Equal(Canonical(identity), Canonical(envelope.Object["identity"])) {
		return fail()
	}
	next := envelope.Object["payload"].Object["checkpoint"]
	observed, err := time.Parse(time.RFC3339Nano, envelope.Object["observed_at"].String)
	if err != nil {
		return err
	}
	covered := []string{}
	for _, step := range fileStepOrder {
		old := previous.Object[step]
		current := next.Object[step]
		state := old.Object["state"].String
		switch state {
		case "not_started", "not_required":
			if !bytes.Equal(Canonical(old), Canonical(current)) {
				return fail()
			}
		case "confirmed", "no_effect":
			// A contradictory terminal proof is admissible only as evidence for
			// the recorder's fixed conflict slot; it never overwrites this proof.
			if current.Object["state"].String != "confirmed" && current.Object["state"].String != "no_effect" {
				return fail()
			}
		case "started", "unknown":
			covered = append(covered, step)
			if current.Object["state"].String != "confirmed" && current.Object["state"].String != "no_effect" && current.Object["state"].String != "unknown" && !bytes.Equal(Canonical(old), Canonical(current)) {
				return fail()
			}
			at, err := time.Parse(time.RFC3339Nano, current.Object["observed_at"].String)
			if err != nil {
				return err
			}
			start, err := time.Parse(time.RFC3339Nano, old.Object["observed_at"].String)
			if err != nil {
				return err
			}
			if at.Before(start) || at.After(observed) {
				return fail()
			}
		}
		if state == "confirmed" || state == "no_effect" {
			covered = append(covered, step)
		}
	}
	closure := envelope.Object["payload"].Object["closure"]
	if closure.Object["state"].String == "known" {
		c := closure.Object["value"].Object
		// Dispatch receipts cannot forge no_dispatch or process lifecycle proof.
		// Those have separate owner/lifecycle producers. Lookup joins cannot close
		// the original mutating invocation either.
		if envelope.Object["emitter"].Object["mode"].String != "dispatch_session" || c["kind"].String != "local_join" || c["executor_session"].String != identity.Object["executor_session"].String {
			return fail()
		}
		steps := c["covered_steps"].Array
		if len(steps) != len(covered) {
			return fail()
		}
		for i, step := range covered {
			if steps[i].String != step {
				return fail()
			}
		}
		joined, err := time.Parse(time.RFC3339Nano, c["joined_at"].String)
		if err != nil {
			return err
		}
		if joined.After(observed) {
			return fail()
		}
	}
	return nil
}
