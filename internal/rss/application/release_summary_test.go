package application

import (
	"testing"

	observabilityport "bansoko/internal/observability/port"
)

// Every Release verdict names its own message, and a verdict the catalog does
// not map is an error, never a generic message.
func TestReleaseVerdictMessageNamesEveryVerdict(t *testing.T) {
	verdicts := []string{
		"discovered", "waiting_metadata", "subscription_stopped", "source_unavailable",
		"parse_failed", "accepted_pending_download", "filtered", "adopted_existing",
		"acquisition_failed", "submitted", ReleaseVerdictResumeBaseline,
		ReleaseVerdictStartBaseline, "awaiting_competition",
	}
	seen := map[observabilityport.MessageCode]string{}
	for _, verdict := range verdicts {
		code, err := observabilityport.ReleaseVerdictMessage(verdict)
		if err != nil {
			t.Fatalf("verdict %q has no message of its own: %v", verdict, err)
		}
		if spec, ok := observabilityport.LookupMessage(code); !ok || spec.Kind != observabilityport.ActivityKindReleaseProcessed {
			t.Fatalf("verdict %q message %q is not a release_processed message", verdict, code)
		}
		if other, dup := seen[code]; dup {
			t.Fatalf("verdicts %q and %q share message %q", other, verdict, code)
		}
		seen[code] = verdict
	}
	if got, err := observabilityport.ReleaseVerdictMessage("unknown_verdict"); err == nil {
		t.Fatalf("unknown verdict message = %q, want an error", got)
	}
}
