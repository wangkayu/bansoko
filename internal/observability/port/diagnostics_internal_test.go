package port

import (
	"strings"
	"testing"
)

func TestInternalHTTP5xxOperationSourcesAreBoundedAndValidated(t *testing.T) {
	longest := strings.Repeat("a", 119)
	if got := InternalHTTP5xxForOperation("listReleaseBatchEntries"); got != "http.5xx:listReleaseBatchEntries" {
		t.Fatalf("operation source = %q", got)
	}
	if got := InternalHTTP5xxForOperation(longest); len(got) != 128 || !IsInternalSource(got) {
		t.Fatalf("longest valid source has length %d and validity %v", len(got), IsInternalSource(got))
	}
	for _, operationID := range []string{"", "9startsWithDigit", "contains/slash", "contains:colon", strings.Repeat("a", 120)} {
		if got := InternalHTTP5xxForOperation(operationID); got != InternalHTTP5xx {
			t.Fatalf("invalid operation %q produced source %q", operationID, got)
		}
	}
	for _, source := range []string{"http.5xx:", "http.5xx:contains/slash", "http.5xx:listReleaseBatchEntries:extra", "http.5xx"} {
		if source != InternalHTTP5xx && IsInternalSource(source) {
			t.Fatalf("malformed operation source %q was accepted", source)
		}
	}
	if !IsInternalSource(InternalHTTP5xx) {
		t.Fatal("legacy base source was rejected")
	}
	for _, source := range InternalSources() {
		if strings.HasPrefix(source, InternalHTTP5xxOperationPrefix) {
			t.Fatalf("dynamic operation source %q leaked into the fixed source list", source)
		}
	}
	if !InternalFailure(InternalHTTP5xxForOperation("listReleaseBatchEntries"), "request-1").Validate() {
		t.Fatal("valid per-operation internal failure was rejected")
	}
}
