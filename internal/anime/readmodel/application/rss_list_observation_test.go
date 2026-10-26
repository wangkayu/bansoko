package application

import (
	"bansoko/internal/testutil/presetdraft"
	"context"
	"testing"
	"time"

	animedomain "bansoko/internal/anime/domain"
)

// These tests pin the original defect: groups the provider lists for a show but
// that the draft has not configured were invisible to the RSS preview, so a
// not-yet-subscribed draft listed no groups even though discovery had found
// them. The observation is the input that closes that gap.

func unconfiguredDraftEffective(maxRSS int) animedomain.EffectiveConfig {
	return animedomain.EffectiveConfig{
		Selection: animedomain.SubgroupsSection{MaxRss: maxRSS, Entries: []animedomain.SubgroupScoreEntry{}},
		Versions:  map[string]int64{animedomain.SectionSubgroups: 1},
	}
}

func observedFacts(groups ...string) []ObservedGroupFact {
	facts := make([]ObservedGroupFact, 0, len(groups))
	for _, group := range groups {
		facts = append(facts, ObservedGroupFact{GroupRef: group, DisplayName: "Observed " + group})
	}
	return facts
}

func TestApplyObservedGroupsListsDiscoveredGroupsOfAnUnconfiguredDraft(t *testing.T) {
	draft := previewTestDraft(animedomain.ProviderKindAnibt)
	effective, sources, err := applyObservedGroups(draft, unconfiguredDraftEffective(2), observedFacts("12", "34", "56"))
	if err != nil {
		t.Fatalf("applyObservedGroups: %v", err)
	}
	if len(effective.Selection.Entries) != 3 {
		t.Fatalf("every observed group must be listed, got %+v", effective.Selection.Entries)
	}
	selected := 0
	for _, entry := range effective.Selection.Entries {
		if entry.EffectiveSelected {
			selected++
		}
		if sources[entry.GroupRef] != RSSListSelectionSourceObservedDefault {
			t.Fatalf("group %s has source %q, want observed_default", entry.GroupRef, sources[entry.GroupRef])
		}
		if entry.DisplayName == "" || entry.Provider != animedomain.ProviderKindAnibt {
			t.Fatalf("a listed group needs its provider and name: %+v", entry)
		}
	}
	if selected != 2 {
		t.Fatalf("max_rss=2 selects two of three, got %d", selected)
	}
}

// A configured group keeps its identity and mode. An automatic entry has no
// score of its own: it is ranked, and shown, at the site score, so the manual
// include of an observed group is selected without taking the only slot here.
func TestApplyObservedGroupsKeepsConfiguredGroupsAndTheirPolicy(t *testing.T) {
	draft := previewTestDraft(animedomain.ProviderKindAnibt, "12")
	effective := unconfiguredDraftEffective(1)
	on := true
	effective.Selection.Entries = []animedomain.SubgroupScoreEntry{{
		GroupRef: "12", DisplayName: "Configured 12", Provider: animedomain.ProviderKindAnibt,
		SelectionMode: animedomain.SelectionModeAuto, Score: 90, EffectiveSelected: true,
	}, {
		GroupRef: "34", DisplayName: "Configured 34", Provider: animedomain.ProviderKindAnibt,
		SelectionMode: animedomain.SelectionModeManual, ManualSelected: &on,
	}}
	got, sources, err := applyObservedGroups(draft, effective, observedFacts("12", "34", "56"))
	if err != nil {
		t.Fatalf("applyObservedGroups: %v", err)
	}
	byRef := map[string]animedomain.SubgroupScoreEntry{}
	for _, entry := range got.Selection.Entries {
		byRef[entry.GroupRef] = entry
	}
	if len(byRef) != 3 {
		t.Fatalf("configured groups must not be duplicated by their own observation: %+v", got.Selection.Entries)
	}
	if byRef["12"].DisplayName != "Configured 12" || byRef["12"].SelectionMode != animedomain.SelectionModeAuto || byRef["12"].Score != 50 {
		t.Fatalf("an automatic entry keeps its identity and shows the site score: %+v", byRef["12"])
	}
	if !byRef["34"].EffectiveSelected || !byRef["12"].EffectiveSelected || byRef["56"].EffectiveSelected || byRef["34"].Score != 100 {
		t.Fatalf("the manual include is outside max_rss and scored 100; the one ranked slot goes to the lower key: %+v", byRef)
	}
	if sources["12"] != RSSListSelectionSourceConfigured || sources["34"] != RSSListSelectionSourceConfigured || sources["56"] != RSSListSelectionSourceObservedDefault {
		t.Fatalf("sources = %+v", sources)
	}
}

func TestApplyObservedGroupsRejectsACustomOriginDraft(t *testing.T) {
	draft := previewTestDraft(animedomain.ProviderKindAnibt)
	draft.Origin = animedomain.SubscriptionOriginCustom
	if _, _, err := applyObservedGroups(draft, unconfiguredDraftEffective(1), observedFacts("1")); err == nil {
		t.Fatal("a custom RSS draft has no provider group observation")
	}
}

// The original defect, end to end through the composed view: before the fix an
// unconfigured draft produced zero groups.
func TestComposeRSSListViewShowsObservedGroupsOfAnUnconfiguredDraft(t *testing.T) {
	draft := previewTestDraft(animedomain.ProviderKindAnibt)
	effective, sources, err := applyObservedGroups(draft, unconfiguredDraftEffective(3), observedFacts("12", "34"))
	if err != nil {
		t.Fatal(err)
	}
	service := &Service{seedPreviews: &readmodelPreviewSource{}}
	view, err := service.composeRSSListViewObserved(
		context.Background(), "current_input", "draft:test", draft, effective, previewTestSelection(), nil,
		sources, nil, 0,
	)
	if err != nil {
		t.Fatalf("composeRSSListViewObserved: %v", err)
	}
	if len(view.Groups) != 2 {
		t.Fatalf("both discovered groups must be listed, got %d: %+v", len(view.Groups), view.Groups)
	}
	for _, group := range view.Groups {
		if !group.Selection.EffectiveSelected {
			t.Fatalf("max_rss=3 selects both: %+v", group.Selection)
		}
		if group.Selection.Source != RSSListSelectionSourceObservedDefault {
			t.Fatalf("an unconfigured group must say where its selection came from: %+v", group.Selection)
		}
		if group.Feed.State != RSSListFeedSnapshotRequired {
			t.Fatalf("a selected group without a feed identity needs a refresh: %+v", group.Feed)
		}
	}
}

func TestValidateObservationMatchesDraft(t *testing.T) {
	draft := previewTestDraft(animedomain.ProviderKindAnibt) // entry ref "1001"
	ok := RSSListObservationRef{Provider: "anibt", ProviderEntryRef: "1001", SnapshotVersion: 3}
	if err := validateObservationMatchesDraft(draft, ok); err != nil {
		t.Fatalf("matching observation rejected: %v", err)
	}
	for name, bad := range map[string]RSSListObservationRef{
		"other provider": {Provider: "mikan", ProviderEntryRef: "1001", SnapshotVersion: 3},
		"other entry":    {Provider: "anibt", ProviderEntryRef: "9999", SnapshotVersion: 3},
		"no version":     {Provider: "anibt", ProviderEntryRef: "1001", SnapshotVersion: 0},
	} {
		err := validateObservationMatchesDraft(draft, bad)
		if err == nil {
			t.Fatalf("%s must be rejected", name)
		}
		if code := businessCode(t, err); code != "INPUT_SOURCE_INVALID" {
			t.Fatalf("%s: code = %q", name, code)
		}
	}
}

type fakeConfigReader struct{}

func (fakeConfigReader) ReadOverrides(context.Context, string) (animedomain.OverrideValue, map[string]animedomain.OverrideValue, error) {
	return animedomain.OverrideValue{}, nil, nil
}

type fakeObservationReader struct {
	observation GroupObservation
	found       bool
	reads       int
	lastRef     RSSListObservationRef
}

func (f *fakeObservationReader) ReadGroupObservation(_ context.Context, ref RSSListObservationRef) (GroupObservation, bool, error) {
	f.reads++
	f.lastRef = ref
	return f.observation, f.found, nil
}

// readableDraft is previewTestDraft made valid for the full read path: every rules
// section carries its complete default copy, which is what an untouched draft
// sends.
func readableDraft(t *testing.T, origin animedomain.ProviderKind, groups ...string) animedomain.AnimeDraft {
	t.Helper()
	draft := previewTestDraft(origin, groups...)
	draft.Rules.Sections = presetdraft.DefaultDomainRules(t)
	draft.Business = businessSnapshotFixture(t, draft.Subgroups.MaxRss)
	return draft
}

func observationService(observations *fakeObservationReader) *Service {
	return &Service{
		config:       fakeConfigReader{},
		seedPreviews: &readmodelPreviewSource{}, observations: observations,
	}
}

// The whole read path, not just the pure pieces: a draft with no configured
// subgroup, a retained observation, and the groups show up with their source.
func TestReadAnimeRSSListViewListsObservedGroupsOfAnUnconfiguredDraft(t *testing.T) {
	observations := &fakeObservationReader{found: true, observation: GroupObservation{
		ObservedAt: time.Unix(1700000000, 0).UTC(),
		Groups:     observedFacts("12", "34"),
	}}
	draft := readableDraft(t, animedomain.ProviderKindAnibt)
	view, err := observationService(observations).ReadAnimeRSSListView(context.Background(), ReadRSSListViewInput{
		InputSource: "current_input", Draft: &draft,
		Observation: &RSSListObservationRef{Provider: "anibt", ProviderEntryRef: "1001", SnapshotVersion: 4},
	})
	if err != nil {
		t.Fatalf("ReadAnimeRSSListView: %v", err)
	}
	if observations.reads != 1 || observations.lastRef.SnapshotVersion != 4 {
		t.Fatalf("the named observation must be read exactly once: %+v", observations)
	}
	if len(view.Groups) != 2 {
		t.Fatalf("both observed groups must be listed, got %+v", view.Groups)
	}
	if view.Observation == nil || view.Observation.State != RSSListObservationUsed || view.Observation.GroupCount != 2 || view.Observation.SnapshotVersion != 4 {
		t.Fatalf("the view must say which observation it used: %+v", view.Observation)
	}
	if view.Observation.ObservedAt == nil {
		t.Fatal("a used observation carries its observed_at")
	}
}

// A purged observation is not an error: the view is composed without it and says
// so, so the client can ask for a fresh discovery instead of showing a dead end.
func TestReadAnimeRSSListViewReportsAnObservationThatIsNoLongerRetained(t *testing.T) {
	draft := readableDraft(t, animedomain.ProviderKindAnibt)
	view, err := observationService(&fakeObservationReader{found: false}).ReadAnimeRSSListView(context.Background(), ReadRSSListViewInput{
		InputSource: "current_input", Draft: &draft,
		Observation: &RSSListObservationRef{Provider: "anibt", ProviderEntryRef: "1001", SnapshotVersion: 4},
	})
	if err != nil {
		t.Fatalf("ReadAnimeRSSListView: %v", err)
	}
	if view.Observation == nil || view.Observation.State != RSSListObservationNotRetained {
		t.Fatalf("a missing observation must be reported, got %+v", view.Observation)
	}
	if len(view.Groups) != 0 {
		t.Fatalf("no observation and no configured group means no group: %+v", view.Groups)
	}
}

func TestReadAnimeRSSListViewRefusesAnObservationOfAnotherEntry(t *testing.T) {
	observations := &fakeObservationReader{found: true}
	draft := readableDraft(t, animedomain.ProviderKindAnibt)
	_, err := observationService(observations).ReadAnimeRSSListView(context.Background(), ReadRSSListViewInput{
		InputSource: "current_input", Draft: &draft,
		Observation: &RSSListObservationRef{Provider: "anibt", ProviderEntryRef: "other", SnapshotVersion: 1},
	})
	if err == nil || businessCode(t, err) != "INPUT_SOURCE_INVALID" {
		t.Fatalf("a foreign observation must be refused, got %v", err)
	}
	if observations.reads != 0 {
		t.Fatal("the observation must be refused before it is read")
	}
}

// Without an observation the view is what it always was: no observation block
// and every group is configured.
func TestReadAnimeRSSListViewWithoutObservationIsUnchanged(t *testing.T) {
	draft := readableDraft(t, animedomain.ProviderKindAnibt)
	draft.Subgroups.Entries = []animedomain.SubgroupScoreEntry{{
		GroupRef: "12", DisplayName: "Group 12", Provider: animedomain.ProviderKindAnibt,
		SelectionMode: animedomain.SelectionModeAuto, Score: 80,
	}}
	view, err := observationService(&fakeObservationReader{}).ReadAnimeRSSListView(context.Background(), ReadRSSListViewInput{
		InputSource: "current_input", Draft: &draft,
	})
	if err != nil {
		t.Fatalf("ReadAnimeRSSListView: %v", err)
	}
	if view.Observation != nil {
		t.Fatalf("no observation was asked for: %+v", view.Observation)
	}
	if len(view.Groups) != 1 {
		t.Fatalf("one configured group expected, got %+v", view.Groups)
	}
	for _, group := range view.Groups {
		if group.Selection.Source != RSSListSelectionSourceConfigured {
			t.Fatalf("a configured group says so: %+v", group.Selection)
		}
	}
}

// config_identity says which configuration a view was computed from. The same
// draft read over two different observations lists different groups, so the two
// views must not share an identity — or a stale response could pass for current.
func TestReadAnimeRSSListViewIdentityIncludesTheObservation(t *testing.T) {
	read := func(version int64, observations *fakeObservationReader) RSSListView {
		draft := readableDraft(t, animedomain.ProviderKindAnibt)
		view, err := observationService(observations).ReadAnimeRSSListView(context.Background(), ReadRSSListViewInput{
			InputSource: "current_input", Draft: &draft,
			Observation: &RSSListObservationRef{Provider: "anibt", ProviderEntryRef: "1001", SnapshotVersion: version},
		})
		if err != nil {
			t.Fatalf("ReadAnimeRSSListView: %v", err)
		}
		return view
	}
	retained := &fakeObservationReader{found: true, observation: GroupObservation{ObservedAt: time.Unix(1, 0).UTC(), Groups: observedFacts("12")}}
	v1, v2 := read(1, retained), read(2, retained)
	if v1.ConfigIdentity == v2.ConfigIdentity {
		t.Fatalf("different observations must have different identities, both %q", v1.ConfigIdentity)
	}
	draft := readableDraft(t, animedomain.ProviderKindAnibt)
	none, err := observationService(&fakeObservationReader{}).ReadAnimeRSSListView(context.Background(), ReadRSSListViewInput{InputSource: "current_input", Draft: &draft})
	if err != nil {
		t.Fatal(err)
	}
	if none.ConfigIdentity == v1.ConfigIdentity {
		t.Fatal("a view without the observation must not share the identity of one that used it")
	}
	// An observation that is no longer retained is composed without it, so the
	// identity is the observation-free one: the view says what it was computed from.
	gone := read(3, &fakeObservationReader{found: false})
	if gone.ConfigIdentity != none.ConfigIdentity {
		t.Fatalf("a purged observation leaves the identity unchanged: %q vs %q", gone.ConfigIdentity, none.ConfigIdentity)
	}
}
