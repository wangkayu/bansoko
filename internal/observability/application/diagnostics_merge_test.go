package application

import (
	"fmt"
	"testing"
	"time"

	"bansoko/internal/observability/outbound"
	observabilityport "bansoko/internal/observability/port"
)

func metadataFailure(t *testing.T, affected string, class outbound.Class) preparedFailure {
	t.Helper()
	prepared, err := prepareFailure(observabilityport.DiagnosticFailureRequest{
		Condition: observabilityport.Condition{Kind: observabilityport.KindMetadata, Variant: observabilityport.VariantUnreachable},
		Severity:  observabilityport.SeverityWarning, TargetID: "tmdb", TargetName: "TMDB",
		Failure:    outbound.Failure{Host: "api.tmdb.example", Class: class, Via: outbound.ViaDirect},
		AffectedID: affected,
	})
	if err != nil {
		t.Fatal(err)
	}
	return prepared
}

// Three TMDB failures on three anime are one episode: three occurrences, three
// distinct affected anime, the latest detail.
func TestMergeFailureFoldsRepeatsIntoOneEpisode(t *testing.T) {
	now := time.Date(2026, 10, 28, 8, 0, 0, 0, time.UTC)
	var state *episodeState
	for i, class := range []outbound.Class{outbound.ClassDNS, outbound.ClassTimeout, outbound.ClassTLS} {
		next := mergeFailure(state, metadataFailure(t, fmt.Sprintf("anime-%d", i), class), now.Add(time.Duration(i)*time.Minute))
		state = &next
	}
	if state.Occurrences != 3 || len(state.AffectedIDs) != 3 {
		t.Fatalf("occurrences=%d affected=%d, want 3/3", state.Occurrences, len(state.AffectedIDs))
	}
	if state.ErrorClass != string(outbound.ClassTLS) {
		t.Fatalf("latest detail must win, got %q", state.ErrorClass)
	}
	if len(state.Recent) != 3 {
		t.Fatalf("recent = %d, want 3", len(state.Recent))
	}
}

// The same object failing again is one affected object, not two.
func TestMergeFailureDedupesAffectedIDs(t *testing.T) {
	now := time.Date(2026, 10, 28, 8, 0, 0, 0, time.UTC)
	first := mergeFailure(nil, metadataFailure(t, "anime-1", outbound.ClassTimeout), now)
	second := mergeFailure(&first, metadataFailure(t, "anime-1", outbound.ClassTimeout), now.Add(time.Minute))
	if second.Occurrences != 2 || len(second.AffectedIDs) != 1 {
		t.Fatalf("occurrences=%d affected=%d, want 2/1", second.Occurrences, len(second.AffectedIDs))
	}
}

func TestMergeFailureBoundsRecentAndAffected(t *testing.T) {
	now := time.Date(2026, 10, 28, 8, 0, 0, 0, time.UTC)
	var state *episodeState
	for i := 0; i < maxAffectedIDs+20; i++ {
		next := mergeFailure(state, metadataFailure(t, fmt.Sprintf("anime-%d", i), outbound.ClassTimeout), now.Add(time.Duration(i)*time.Second))
		state = &next
	}
	if len(state.Recent) != maxRecentOccurrences {
		t.Fatalf("recent = %d, want %d", len(state.Recent), maxRecentOccurrences)
	}
	if len(state.AffectedIDs) != maxAffectedIDs {
		t.Fatalf("affected = %d, want it capped at %d", len(state.AffectedIDs), maxAffectedIDs)
	}
	if state.Occurrences != maxAffectedIDs+20 {
		t.Fatalf("occurrences = %d", state.Occurrences)
	}
	last := state.Recent[len(state.Recent)-1].At
	if !last.Equal(now.Add(time.Duration(maxAffectedIDs+19) * time.Second)) {
		t.Fatalf("recent must keep the newest occurrences, last=%v", last)
	}
}

// mergeFailure must not alias the previous state's slices.
func TestMergeFailureDoesNotMutatePreviousState(t *testing.T) {
	now := time.Date(2026, 10, 28, 8, 0, 0, 0, time.UTC)
	first := mergeFailure(nil, metadataFailure(t, "anime-1", outbound.ClassTimeout), now)
	_ = mergeFailure(&first, metadataFailure(t, "anime-2", outbound.ClassTimeout), now.Add(time.Minute))
	if len(first.AffectedIDs) != 1 || len(first.Recent) != 1 || first.Occurrences != 1 {
		t.Fatalf("previous state mutated: %+v", first)
	}
}

// The newest variant decides: a paused site that recovers to a plain refresh
// failure reports the plain variant.
func TestMergeFailureNewestVariantWins(t *testing.T) {
	now := time.Date(2026, 10, 28, 8, 0, 0, 0, time.UTC)
	req := func(variant observabilityport.ConditionVariant) preparedFailure {
		prepared, err := prepareFailure(observabilityport.DiagnosticFailureRequest{
			Condition: observabilityport.Condition{Kind: observabilityport.KindSite, Variant: variant},
			Severity:  observabilityport.SeverityWarning, TargetID: "anibt", TargetName: "AniBT",
		})
		if err != nil {
			t.Fatal(err)
		}
		return prepared
	}
	first := mergeFailure(nil, req(observabilityport.VariantRefreshFailed), now)
	second := mergeFailure(&first, req(observabilityport.VariantPaused), now.Add(time.Minute))
	if second.Variant != string(observabilityport.VariantPaused) {
		t.Fatalf("variant = %q", second.Variant)
	}
}

func TestPrepareFailureRejectsMalformedFacts(t *testing.T) {
	base := observabilityport.DiagnosticFailureRequest{
		Condition: observabilityport.Condition{Kind: observabilityport.KindMetadata, Variant: observabilityport.VariantUnreachable},
		Severity:  observabilityport.SeverityWarning, TargetID: "tmdb",
	}
	cases := map[string]func(*observabilityport.DiagnosticFailureRequest){
		"status without http_status class": func(r *observabilityport.DiagnosticFailureRequest) {
			r.Failure = outbound.Failure{Class: outbound.ClassTimeout, Status: 503}
		},
		"unknown class": func(r *observabilityport.DiagnosticFailureRequest) {
			r.Failure = outbound.Failure{Class: "weird"}
		},
		"request id on a user condition": func(r *observabilityport.DiagnosticFailureRequest) { r.RequestID = "request-1" },
	}
	for name, edit := range cases {
		t.Run(name, func(t *testing.T) {
			req := base
			edit(&req)
			if _, err := prepareFailure(req); err == nil {
				t.Fatal("malformed request accepted")
			}
		})
	}
	if _, err := prepareFailure(base); err != nil {
		t.Fatalf("valid request rejected: %v", err)
	}
}

// An IPv6 literal is a valid host; a malformed one loses only the host, never
// the episode.
func TestPrepareFailureKeepsRecordWhenHostIsUnusable(t *testing.T) {
	cases := map[string]string{
		"ipv6 literal": "fd00::5",
		"ipv4 literal": "10.0.0.5",
		"bare name":    "qb.lan",
		"with a path":  "example.test/private",
		"with a zone":  "fe80::1%eth0",
	}
	want := map[string]string{"ipv6 literal": "fd00::5", "ipv4 literal": "10.0.0.5", "bare name": "qb.lan"}
	for name, host := range cases {
		t.Run(name, func(t *testing.T) {
			prepared, err := prepareFailure(observabilityport.DiagnosticFailureRequest{
				Condition: observabilityport.Condition{Kind: observabilityport.KindMetadata, Variant: observabilityport.VariantUnreachable},
				Severity:  observabilityport.SeverityWarning, TargetID: "tmdb",
				Failure: outbound.Failure{Host: host, Class: outbound.ClassTimeout},
			})
			if err != nil {
				t.Fatalf("record lost: %v", err)
			}
			state := mergeFailure(nil, prepared, time.Now())
			if state.TargetHost != want[name] || state.Recent[0].Host != want[name] {
				t.Fatalf("host = %q / %q, want %q", state.TargetHost, state.Recent[0].Host, want[name])
			}
		})
	}
}
