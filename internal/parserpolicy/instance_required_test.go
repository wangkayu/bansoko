package parserpolicy

import (
	"errors"
	"testing"

	"bansoko/internal/apperrors"
	"bansoko/internal/parser"
)

func TestInstanceFromRowValidatesTheFrozenPolicy(t *testing.T) {
	instance, err := InstanceFromRow("anime-1", "regex", 1, 4)
	if err != nil {
		t.Fatalf("valid row: %v", err)
	}
	if instance.Policy.Kind != parser.ParserRegex {
		t.Fatalf("policy = %+v", instance.Policy)
	}
	if instance.Version != 4 || instance.SourceDefaultVersion != 1 || instance.ManagedAnimeID != "anime-1" {
		t.Fatalf("instance = %+v", instance)
	}
}

func TestInstanceFromRowFailsClosedOnUnusableRows(t *testing.T) {
	cases := []struct {
		name string
		kind string
	}{
		{name: "empty kind", kind: "  "},
		{name: "unregistered kind", kind: "not-a-parser"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := InstanceFromRow("anime-1", tc.kind, 1, 1)
			if !errors.Is(err, ErrInstanceParserPolicyRequired) {
				t.Fatalf("error = %v, want ErrInstanceParserPolicyRequired", err)
			}
		})
	}
}

// The typed integrity error must reach the HTTP boundary as the stable 409
// code: the transport maps apperrors.Business, and unwrapping is how it is
// found. This pins both halves so a refactor cannot silently degrade a leaked
// mutation into a generic 500.
func TestInstancePolicyRequiredErrorMapsToTypedConflict(t *testing.T) {
	err := error(&InstancePolicyRequiredError{ManagedAnimeID: "anime-1"})
	if !errors.Is(err, ErrInstanceParserPolicyRequired) {
		t.Fatalf("sentinel identity lost: %v", err)
	}
	var business *apperrors.Business
	if !errors.As(err, &business) {
		t.Fatalf("error is not a typed Business error: %v", err)
	}
	if business.Status != 409 || business.Code != "INSTANCE_PARSER_POLICY_REQUIRED" {
		t.Fatalf("business = %+v", business)
	}
}

func TestInstancePolicyRequiredErrorCarriesTheDetail(t *testing.T) {
	_, err := InstanceFromRow("anime-1", "not-a-parser", 1, 1)
	var required *InstancePolicyRequiredError
	if !errors.As(err, &required) {
		t.Fatalf("error = %v, want *InstancePolicyRequiredError", err)
	}
	if required.ManagedAnimeID != "anime-1" || required.Detail == "" {
		t.Fatalf("required = %+v", required)
	}
}
