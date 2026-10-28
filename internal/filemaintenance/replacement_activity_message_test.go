package filemaintenance

import (
	"testing"

	observabilityport "bansoko/internal/observability/port"
)

// A key decision writes its line at admission only when its verdict is final
// there; a replace verdict writes nothing until it ends (replaced or
// not_replaced).
func TestAdmissionReplacementMessage(t *testing.T) {
	for verdict, want := range map[string]observabilityport.MessageCode{
		"keep":    observabilityport.MessageReplacementKeptExisting,
		"retain":  observabilityport.MessageReplacementKeptExisting,
		"queue":   observabilityport.MessageReplacementKeptAll,
		"blocked": observabilityport.MessageReplacementBlocked,
	} {
		got, final := admissionReplacementMessage(verdict)
		if !final || got != want {
			t.Fatalf("admissionReplacementMessage(%q) = %q, %v; want %q, true", verdict, got, final, want)
		}
		spec, ok := observabilityport.LookupMessage(got)
		if !ok || spec.Kind != observabilityport.ActivityKindReplacementDecided {
			t.Fatalf("message %q is not a replacement_decided message", got)
		}
	}
	for _, verdict := range []string{"replace", "other", ""} {
		if got, final := admissionReplacementMessage(verdict); final || got != "" {
			t.Fatalf("admissionReplacementMessage(%q) = %q, %v; want no admission line", verdict, got, final)
		}
	}
}
