package application

import (
	"bansoko/internal/testutil/presetdraft"
	"context"
	"database/sql"
	"testing"

	observabilityapplication "bansoko/internal/observability/application"
	"bansoko/internal/parserpolicy"
	"bansoko/internal/testutil/animetest"
	"bansoko/internal/transport/httpapi/generated"
	"bansoko/internal/workitems"
)

// This file holds the draft bucket's own fixture copies. A draft test may not
// use another bucket's unexported fixtures; these are faithful copies under
// draft-prefixed names and need only exported symbols plus animetest
// primitives.

// draftNewTestService builds the draft service the way the composition root
// does for the ports the draft commands use: the work-item store at
// construction, the activity journal and the parser snapshots after it (the
// file-maintenance graph owns those and is built after this service). The
// direct Subscribe path deliberately has no feed reader: it performs no
// external reads.
func draftNewTestService(t *testing.T, db *sql.DB) *Service {
	t.Helper()
	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(db, Dependencies{WorkItems: store, Connections: draftNoConnections{}})
	if err != nil {
		t.Fatal(err)
	}
	activityWriter, err := observabilityapplication.NewService(db)
	if err != nil {
		t.Fatal(err)
	}
	service.SetActivityWriter(activityWriter)
	// Wired like the server composition root: Subscribe provisions the axes
	// from the draft's business snapshot through the frozen parser port.
	service.SetFrozenParserPolicy(parserpolicy.NewService(db))
	return service
}

func draftLocatorInput(_ *testing.T, url string) string { return url }

func draftDefaultSubgroups(t *testing.T) generated.SubgroupsSection {
	t.Helper()
	return generated.SubgroupsSection{Entries: []generated.SubgroupScoreEntry{}, MaxRss: 3}
}

// draftDefaultRules returns rules carrying every section at its code
// default: the complete copy a draft created from a never-saved Global holds.
func draftDefaultRules(t *testing.T) generated.RulesDraft {
	t.Helper()
	return presetdraft.DefaultRules(t)
}

func draftTestDraft(t *testing.T) generated.AnimeDraft {
	t.Helper()
	season := 1
	return generated.AnimeDraft{
		Origin:        generated.SubscriptionOriginCustom,
		InstanceLabel: animetest.StringPtr("test-instance"),
		Title:         "Test Show",
		Metadata: generated.AnimeMetadataDraft{
			Bgm:  &generated.NullableBGMBinding{WorkId: "bgm-123"},
			Tmdb: &generated.NullableTMDBBinding{WorkId: "tmdb-456", MediaKind: generated.TMDBMediaKindTv, Season: season},
		},
		Subgroups: draftDefaultSubgroups(t),
		Rules:     draftDefaultRules(t),
		RssSources: []generated.RSSSourceDraft{
			{SourceKind: generated.SourceKindCustomRss, Locator: draftLocatorInput(t, "https://feeds.example.com/rss.xml"), GroupName: "TestGroup", Title: "Test Show"},
		},
	}
}

func draftSubscribeTestDraft(t *testing.T, service *Service, key string) generated.SubscribeResult {
	t.Helper()
	result, err := service.SubscribeAnime(context.Background(), generated.SubscribeAnimeInput{Draft: presetdraft.Load(t, service.db, draftTestDraft(t)), Start: new(true)}, key)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	return result
}
