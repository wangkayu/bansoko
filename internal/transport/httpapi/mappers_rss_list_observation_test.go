package httpapi

import (
	"testing"

	readmodelapplication "bansoko/internal/anime/readmodel/application"
	"bansoko/internal/apperrors"
	"bansoko/internal/transport/httpapi/generated"
)

func observationRef() *generated.RSSListObservationRef {
	return &generated.RSSListObservationRef{Provider: "anibt", ProviderEntryRef: "1001", SnapshotVersion: 7}
}

// An observation is evidence about one draft's catalog entry; it has no meaning
// for a saved revision or an owner read, so those modes refuse it instead of
// ignoring it silently.
func TestMapRSSListReadInputRefusesObservationOutsideCurrentInput(t *testing.T) {
	candidate := generated.ID("cand-1")
	revision := generated.Version(1)
	_, err := mapRSSListReadInput(generated.RSSListViewInput{
		InputSource: "candidate_revision", CandidateId: &candidate, Revision: &revision, Observation: observationRef(),
	})
	business, ok := err.(*apperrors.Business)
	if !ok || business.Code != "INPUT_SOURCE_INVALID" {
		t.Fatalf("observation on candidate_revision must be INPUT_SOURCE_INVALID, got %v", err)
	}
}

func TestMapRSSListViewReportsSelectionSourceAndObservation(t *testing.T) {
	view := readmodelapplication.RSSListView{
		Mode: "current_input", ConfigIdentity: "draft:x",
		Groups: []readmodelapplication.RSSListGroup{
			{Selection: readmodelapplication.RSSListGroupSelection{Source: readmodelapplication.RSSListSelectionSourceObservedDefault}},
			{Selection: readmodelapplication.RSSListGroupSelection{}}, // unclassified: configured
		},
		Observation: &readmodelapplication.RSSListObservation{
			State: readmodelapplication.RSSListObservationNotRetained, Provider: "anibt", ProviderEntryRef: "1001", SnapshotVersion: 7,
		},
	}
	mapped, err := mapRSSListView(view)
	if err != nil {
		t.Fatalf("mapRSSListView: %v", err)
	}
	if got := mapped.Groups[0].Selection.Source; got != generated.RSSListGroupSelectionSource("observed_default") {
		t.Fatalf("observed group source = %q", got)
	}
	if got := mapped.Groups[1].Selection.Source; got != generated.RSSListGroupSelectionSource("configured") {
		t.Fatalf("an unclassified group is configured, got %q", got)
	}
	if mapped.Observation == nil || mapped.Observation.State != generated.RSSListObservationState("not_retained") || mapped.Observation.SnapshotVersion != 7 {
		t.Fatalf("observation not mapped: %+v", mapped.Observation)
	}
	none, err := mapRSSListView(readmodelapplication.RSSListView{Mode: "current_input"})
	if err != nil {
		t.Fatal(err)
	}
	if none.Observation != nil {
		t.Fatalf("no observation asked for, none reported: %+v", none.Observation)
	}
}
