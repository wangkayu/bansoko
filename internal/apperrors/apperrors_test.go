package apperrors

import "testing"

// AppErrors: typed application-boundary errors the HTTP transport maps to the
// ErrorResponse shape. Each carries a stable code and never leaks secrets/SQL.

func TestNotFoundMessage(t *testing.T) {
	e := NotFound{Resource: "anime", ID: "anime-1"}
	if got := e.Error(); got != "anime anime-1 not found" {
		t.Fatalf("unexpected message: %q", got)
	}
}

func TestVersionConflictMessage(t *testing.T) {
	e := VersionConflict{Owner: "rules", CurrentVersion: 7}
	if got := e.Error(); got != "rules owner version conflict (current 7)" {
		t.Fatalf("unexpected message: %q", got)
	}
}

func TestBusinessMessageAndIdentity(t *testing.T) {
	e := Business{Status: 422, Code: "METADATA_REQUIRED", Message: "bgm and tmdb are required"}
	if e.Error() != "METADATA_REQUIRED: bgm and tmdb are required" {
		t.Fatalf("unexpected message: %q", e.Error())
	}
	if e.Status != 422 {
		t.Fatalf("status not preserved: %d", e.Status)
	}
}

func TestBadRequestMessage(t *testing.T) {
	e := BadRequest{Code: "MISSING_IDEMPOTENCY_KEY", Message: "header required"}
	if e.Error() != "MISSING_IDEMPOTENCY_KEY: header required" {
		t.Fatalf("unexpected message: %q", e.Error())
	}
}

func TestValidationCodeOnly(t *testing.T) {
	field := "username"
	e := Validation{Code: "INVALID_FIELDS", Details: []FieldViolation{
		{Code: "TOO_SHORT", Field: &field, Message: "too short"},
	}}
	if e.Error() != "INVALID_FIELDS" {
		t.Fatalf("validation error must expose only the stable code, got %q", e.Error())
	}
	if len(e.Details) != 1 || e.Details[0].Field == nil || *e.Details[0].Field != "username" {
		t.Fatalf("details not preserved: %+v", e.Details)
	}
}

func TestTypesImplementError(t *testing.T) {
	var _ error = &NotFound{}
	var _ error = &VersionConflict{}
	var _ error = &Business{}
	var _ error = &BadRequest{}
	var _ error = &Validation{}
}
