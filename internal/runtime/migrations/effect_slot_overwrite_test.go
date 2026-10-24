package migrations

import (
	"testing"
	"time"
)

// The fold enforces the old `*_unresolved_uq` predicate as a column guard: a
// slot may only be replaced when it is empty or its `retry_safe` is true, and a
// replacement starts a fresh attempt. These tests pin both halves for the
// download submission slot and the file publication slot.

func TestDownloadSubmissionSlotOverwriteGuard(t *testing.T) {
	env := newRootEnv(t)

	// An unresolved (prepared) slot must never be overwritten.
	root := env.root(t, validInput)
	task := rootEnvTaskID(root)
	if err := applySlot(env.db, submissionTable, "id", task, submissionSlot("permit-a", root, env.now)); err != nil {
		t.Fatal(err)
	}
	replacement := merged(submissionSlot("permit-b", root, env.now), map[string]any{
		"submit_dispatch_state": "dispatching", "submit_dispatch_started_at": env.now.Add(time.Second),
		"submit_attempt": 1, "submit_closed_proof": nil, "submit_resolution_proof": nil,
		"submit_retry_safe": false, "submit_lookup_count": 0, "submit_lookup_ticket": nil,
	})
	expectState(t, "an unresolved submission slot cannot be overwritten", applySlot(env.db, submissionTable, "id", task, replacement), "23514")

	// A closed retry_safe slot admits a replacement whose attempt advances by one.
	root = env.root(t, validInput)
	task = rootEnvTaskID(root)
	closed := merged(submissionSlot("permit-c", root, env.now), map[string]any{
		"submit_dispatch_state": "closed", "submit_dispatch_started_at": env.now.Add(time.Second),
		"submit_closed_proof": jsonv(`{"kind":"channel_closed"}`),
		"submit_effect_class": "no_external_effect", "submit_resolution_proof": jsonv(`{"k":1}`),
		"submit_retry_safe": true, "submit_attempt": 1,
	})
	if err := applySlot(env.db, submissionTable, "id", task, closed); err != nil {
		t.Fatalf("seed a closed retry_safe slot: %v", err)
	}
	replacement = merged(submissionSlot("permit-d", root, env.now), map[string]any{
		"submit_dispatch_state": "dispatching", "submit_dispatch_started_at": env.now.Add(2 * time.Second),
		"submit_attempt": 2, "submit_closed_proof": nil, "submit_resolution_proof": nil,
		"submit_retry_safe": false, "submit_lookup_count": 0, "submit_lookup_ticket": nil,
	})
	expectState(t, "a closed retry_safe submission slot admits a fresh attempt", applySlot(env.db, submissionTable, "id", task, replacement), "")

	// The replacement must raise the attempt by exactly one.
	root = env.root(t, validInput)
	task = rootEnvTaskID(root)
	if err := applySlot(env.db, submissionTable, "id", task, closed); err != nil {
		t.Fatal(err)
	}
	stalled := merged(replacement, map[string]any{"submit_attempt": 1})
	expectState(t, "a replacement without an attempt step", applySlot(env.db, submissionTable, "id", task, stalled), "23514")
}

func TestFilePublicationSlotOverwriteGuard(t *testing.T) {
	env := newRootEnv(t)

	// An unresolved (prepared) slot must never be overwritten.
	operation := "op-guard-a"
	seedCompletedFileOperation(t, env.db, "guard-a", operation)
	if err := applySlot(env.db, publicationTable, "id", operation, publicationSlot("pub-a", "guard-a", env.now)); err != nil {
		t.Fatal(err)
	}
	replacement := merged(publicationSlot("pub-b", "guard-a", env.now), map[string]any{
		"publication_attempt": 2,
	})
	expectState(t, "an unresolved publication slot cannot be overwritten", applySlot(env.db, publicationTable, "id", operation, replacement), "23514")

	// A closed retry_safe slot admits a replacement that starts the next attempt.
	operation = "op-guard-b"
	seedCompletedFileOperation(t, env.db, "guard-b", operation)
	closed := func(permit, tag string, wall time.Time) map[string]any {
		return merged(publicationSlot(permit, tag, env.now), map[string]any{
			"publication_dispatch_state": "closed", "publication_dispatch_started_at": wall,
			"publication_closed_proof": jsonv(`{"kind":"channel_closed"}`),
			"publication_effect_class": "confirmed", "publication_retry_safe": true, "publication_attempt": 1,
		})
	}
	if err := applySlot(env.db, publicationTable, "id", operation, closed("pub-c", "guard-b", env.now.Add(time.Second))); err != nil {
		t.Fatalf("seed a closed retry_safe publication slot: %v", err)
	}
	replacement = merged(publicationSlot("pub-d", "guard-b", env.now), map[string]any{
		"publication_attempt": 2, "publication_closed_proof": nil, "publication_retry_safe": false,
		"publication_dispatch_started_at": nil,
	})
	expectState(t, "a closed retry_safe publication slot admits a fresh attempt", applySlot(env.db, publicationTable, "id", operation, replacement), "")

	// The replacement must start a strictly later attempt.
	operation = "op-guard-c"
	seedCompletedFileOperation(t, env.db, "guard-c", operation)
	if err := applySlot(env.db, publicationTable, "id", operation, closed("pub-f", "guard-c", env.now.Add(time.Second))); err != nil {
		t.Fatal(err)
	}
	stalled := merged(publicationSlot("pub-g", "guard-c", env.now), map[string]any{
		"publication_attempt": 1, "publication_closed_proof": nil, "publication_retry_safe": false,
		"publication_dispatch_started_at": nil,
	})
	expectState(t, "a publication replacement without an attempt step", applySlot(env.db, publicationTable, "id", operation, stalled), "23514")
}
