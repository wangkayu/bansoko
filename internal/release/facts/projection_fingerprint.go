package facts

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"

	"bansoko/internal/qualityprofile"
)

// ProjectionFingerprint is shared by direct RSS writes and lazy projection.
// JSONB presentation (whitespace/key order) cannot create a new evidence version.
func ProjectionFingerprint(candidate Candidate, compare []byte) (string, error) {
	values, err := qualityprofile.ParseValues(compare)
	if err != nil {
		return "", err
	}
	raw, err := json.Marshal(struct {
		Title    string
		Coverage []string
		Compare  json.RawMessage
	}{candidate.Title, candidate.Coverage, values.JSON()})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}

func sameComparisonValues(left, right []byte) bool {
	a, err := qualityprofile.ParseValues(left)
	if err != nil {
		return false
	}
	b, err := qualityprofile.ParseValues(right)
	return err == nil && reflect.DeepEqual(a, b)
}
