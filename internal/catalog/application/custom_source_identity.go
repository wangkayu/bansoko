package application

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// CustomSnapshotScope binds both locator and group, never the display name
// alone. Candidate revision resolution supplies these frozen values.
func CustomSnapshotScope(configIdentity, locator, group string) (entryRef, groupRef string) {
	raw, _ := json.Marshal([]string{configIdentity, locator, group})
	sum := sha256.Sum256(raw)
	return "custom-source:" + hex.EncodeToString(sum[:]), "custom-group:" + hex.EncodeToString(sum[:])
}

// CandidateCustomSnapshotIdentity is the snapshot scope identity of a saved
// custom preset revision: the candidate fetch persists under it, and both the
// candidate_revision read and a custom draft's bench read (current_input with
// candidate_id + revision) bind it.
func CandidateCustomSnapshotIdentity(candidateID string, revision int64) string {
	return fmt.Sprintf("%s#%d", candidateID, revision)
}

// OwnerCustomSnapshotIdentity is the snapshot scope identity of a pending
// custom subscription's test bench. It is per instance, so a saved source
// edit (different locator or group) scopes a different snapshot.
func OwnerCustomSnapshotIdentity(managedAnimeID string) string {
	return "owner:" + managedAnimeID
}
