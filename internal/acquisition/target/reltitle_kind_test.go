package target

import (
	"context"
	"testing"
)

// The two kinds resolve the same title differently, and the frozen release
// input digest carries the kind and both versions: a kind switch (or a
// lexicon/implementation bump) re-evaluates, it never silently reuses.
func TestKindSwitchChangesResolvedEpisodeAndInputDigest(t *testing.T) {
	const title = "[Group] Show Season 2 [1080p]"
	resolve := func(kind string) ParserEvidence {
		t.Helper()
		evidence, err := ResolveParserEvidence(context.Background(), ParserSelectionInput{Kind: kind, MediaKind: "tv", Season: 1}, title)
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		return evidence
	}
	regex, reltitle := resolve("regex"), resolve("reltitle")
	if regex.Identity.Episode == nil || string(*regex.Identity.Episode) != "2" {
		t.Fatalf("regex should read the season digit as the episode, got %+v", regex.Identity)
	}
	if reltitle.Identity.Episode != nil {
		t.Fatalf("reltitle must not resolve an episode from a season-only title, got %+v", reltitle.Identity)
	}
	digest := func(e ParserEvidence) string {
		return ReleaseMediaInputDigest(ReleaseMediaSnapshotInput{Title: title, Evidence: e})
	}
	if digest(regex) == digest(reltitle) {
		t.Fatal("kind switch must change the release input digest")
	}
	bumped := reltitle
	bumped.Selection.Descriptor.ConfigVersion += "-next"
	if digest(bumped) == digest(reltitle) {
		t.Fatal("config version must enter the release input digest")
	}
	bumped = reltitle
	bumped.Selection.Descriptor.ImplementationVersion += "-next"
	if digest(bumped) == digest(reltitle) {
		t.Fatal("implementation version must enter the release input digest")
	}
}
