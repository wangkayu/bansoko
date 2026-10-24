package application

import (
	"testing"

	animedomain "bansoko/internal/anime/domain"
	catalogapplication "bansoko/internal/catalog/application"
)

// A custom draft's bench is scoped by the saved preset revision that its
// candidate fetch persisted under; the scope is the candidate_revision read's
// own identity, and it is accepted for a custom draft only.
func TestDraftCustomSnapshotScopeIsTheSavedRevision(t *testing.T) {
	custom := animedomain.AnimeDraft{Origin: animedomain.SubscriptionOriginCustom}
	provider := animedomain.AnimeDraft{Origin: animedomain.SubscriptionOriginMikan}

	if scope, err := draftCustomSnapshotScope(custom, ReadRSSListViewInput{}); err != nil || scope != "" {
		t.Fatalf("unscoped draft = %q, %v", scope, err)
	}
	scope, err := draftCustomSnapshotScope(custom, ReadRSSListViewInput{CandidateID: "cand-1", Revision: 3})
	if err != nil || scope != catalogapplication.CandidateCustomSnapshotIdentity("cand-1", 3) || scope != "cand-1#3" {
		t.Fatalf("scoped custom draft = %q, %v", scope, err)
	}
	for name, input := range map[string]struct {
		draft animedomain.AnimeDraft
		read  ReadRSSListViewInput
	}{
		"candidate without revision": {custom, ReadRSSListViewInput{CandidateID: "cand-1"}},
		"revision without candidate": {custom, ReadRSSListViewInput{Revision: 3}},
		"provider draft":             {provider, ReadRSSListViewInput{CandidateID: "cand-1", Revision: 3}},
		"one-shot":                   {custom, ReadRSSListViewInput{CandidateID: "cand-1", Revision: 3, OneShot: true}},
	} {
		if _, err := draftCustomSnapshotScope(input.draft, input.read); err == nil {
			t.Fatalf("%s was accepted", name)
		}
	}
}
