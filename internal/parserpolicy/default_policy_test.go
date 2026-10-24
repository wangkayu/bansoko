package parserpolicy

import (
	"testing"

	"bansoko/internal/parser"
)

// reltitle is the creation default; regex stays a
// registered, selectable kind.
func TestDefaultPolicyIsReltitle(t *testing.T) {
	policy := DefaultPolicy()
	if policy.Kind != parser.ParserReltitle {
		t.Fatalf("default kind=%q, want %q", policy.Kind, parser.ParserReltitle)
	}
	if err := Validate(policy); err != nil {
		t.Fatalf("default policy must validate: %v", err)
	}
	if err := Validate(Policy{Kind: parser.ParserRegex}); err != nil {
		t.Fatalf("regex must stay selectable: %v", err)
	}
}
