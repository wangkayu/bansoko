package attrrules

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// LoadExact reads the immutable rule set a business snapshot pins, by its content
// fingerprint, and verifies the content against that fingerprint. It never falls
// back to the active (Current) rule set: a pinned version that cannot be read is
// an error, not a substitution. read returns the stored content for a fingerprint.
//
// Preview and one-shot admission both resolve attr rules through it, so the same
// draft is evaluated with the same rules by both.
func LoadExact(ctx context.Context, version, digest string, read func(context.Context, string) ([]byte, error)) (Rules, error) {
	if digest == "" || version != ProfileVersion(digest) {
		return Rules{}, errors.New("exact attrrules reference unavailable")
	}
	raw, err := read(ctx, digest)
	if err != nil {
		return Rules{}, fmt.Errorf("exact attrrules source unavailable: %w", err)
	}
	var rules Rules
	if err := json.Unmarshal(raw, &rules); err != nil {
		return Rules{}, err
	}
	got, err := Fingerprint(rules)
	if err != nil {
		return Rules{}, err
	}
	if got != digest {
		return Rules{}, errors.New("exact attrrules digest mismatch")
	}
	return rules, nil
}
