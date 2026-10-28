//go:build soakscale

package soak

import (
	"bansoko/internal/testutil/presetdraft"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"testing"

	rootanimepersistence "bansoko/internal/anime/adapter/persistence"
	rootapplication "bansoko/internal/anime/application"
	draftapplication "bansoko/internal/anime/draft/application"
	ownershipapplication "bansoko/internal/anime/ownership/application"
	readmodel "bansoko/internal/anime/readmodel/application"
	"bansoko/internal/attrrules"
	attrpersistence "bansoko/internal/attrrules/adapter/persistence"
	"bansoko/internal/filemaintenance"
	"bansoko/internal/namingvars"
	varspersistence "bansoko/internal/namingvars/adapter/persistence"
	observabilityapplication "bansoko/internal/observability/application"
	"bansoko/internal/parserpolicy"
	facts "bansoko/internal/release/facts"
	mediapersistence "bansoko/internal/release/facts/adapter/persistence/media"
	"bansoko/internal/testutil/frozenpolicy"
	"bansoko/internal/transport/httpapi/generated"
	"bansoko/internal/workitems"
)

// noFeed is the feed fetcher of the services built here: the soak never reaches
// a real network provider, and the owner services only need the port to exist.
type noFeed struct{}

func (noFeed) Fetch(context.Context, string) (rootapplication.FeedResult, error) {
	return rootapplication.FeedResult{}, fmt.Errorf("soak: no external feed access")
}

// installRegistries installs the attribute rules and naming vars registries the
// owner and readmodel services evaluate with (same content as the PG test
// fixtures; copied because the shared helper is part of the PG test inventory).
func installRegistries(db *sql.DB) error {
	rules := attrrules.Rules{
		Version:        1,
		Resolution:     []string{"2160p", "1440p", "1080p", "720p", "480p"},
		Quality:        []string{"remux", "web-dl", "webrip", "bluray", "bdrip", "hdtv"},
		Language:       []string{"chs", "cht", "sc", "tc", "jpn", "eng"},
		SpecialMarkers: []string{"special", "ncop", "nced"},
		GroupBrackets:  []string{"[", "]"},
	}
	fp, err := attrrules.Fingerprint(rules)
	if err != nil {
		return err
	}
	if err := attrrules.SetActive(rules, fp); err != nil {
		return err
	}
	source := `vars.test = {marker: "ok"};`
	vars := namingvars.Vars{"test": {"marker": "ok"}}
	vfp, err := namingvars.SourceFingerprint(source)
	if err != nil {
		return err
	}
	if err := namingvars.SetActive(vars, vfp); err != nil {
		return err
	}
	ctx := context.Background()
	if err := attrrules.NewStore(attrpersistence.NewRepository(db)).Publish(ctx, rules, fp); err != nil {
		return fmt.Errorf("publish attr rules: %w", err)
	}
	computed, err := namingvars.Run(source, namingvars.DefaultInput, namingvars.DefaultTimeout)
	if err != nil {
		return err
	}
	if err := namingvars.NewStore(varspersistence.NewRepository(db)).PublishSource(ctx, source, computed, vfp); err != nil {
		return fmt.Errorf("publish vars: %w", err)
	}
	return nil
}

// ownerServices are the real owner services over the runtime database.
type ownerServices struct {
	DB       *sql.DB
	Store    *workitems.Store
	Kernel   *rootapplication.Service
	Draft    *draftapplication.Service
	Owners   *ownershipapplication.Service
	Readmode *readmodel.Service
	Activity *observabilityapplication.Service
}

func newOwnerServices(db *sql.DB) (*ownerServices, error) {
	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		return nil, err
	}
	kernel, err := rootapplication.NewService(db, store, noFeed{})
	if err != nil {
		return nil, err
	}
	draftService, err := draftapplication.NewService(db, draftapplication.Dependencies{WorkItems: store})
	if err != nil {
		return nil, err
	}
	activity, err := observabilityapplication.NewService(db)
	if err != nil {
		return nil, err
	}
	draftService.SetActivityWriter(activity)
	draftService.SetFrozenParserPolicy(parserpolicy.NewService(db))
	owners, err := ownershipapplication.NewService(db, ownershipapplication.Dependencies{
		WorkItems:       store,
		OneShotMarkers:  rootanimepersistence.NewRepository(db),
		ActivityWriter:  activity,
		OffsetAlignment: filemaintenance.NewOffsetAlignmentService(),
	})
	if err != nil {
		return nil, err
	}
	rm, err := readmodel.NewService(db, readmodel.Dependencies{
		Config: kernel, Owners: owners, Revisions: draftService,
		MediaFacts: facts.NewStoreWithRepository(mediapersistence.NewRepository(db)),
	})
	if err != nil {
		return nil, err
	}
	return &ownerServices{DB: db, Store: store, Kernel: kernel, Draft: draftService, Owners: owners, Readmode: rm, Activity: activity}, nil
}

// defaultRulesDraft returns rules carrying every section at its code default.
func defaultRulesDraft() (generated.RulesDraft, error) {
	return presetdraft.DefaultRulesValue()
}

// subscribeTemplate subscribes one custom-RSS anime through the real draft
// service, exactly as the readmodel tests do. Its rows are the shape template
// the other owners of the dataset are cloned from.
func (s *ownerServices) subscribeTemplate(ctx context.Context, key, label, locator string, sources int) (generated.SubscribeResult, error) {
	rules, err := defaultRulesDraft()
	if err != nil {
		return generated.SubscribeResult{}, err
	}
	lab := label
	draft := generated.AnimeDraft{
		Origin:        generated.SubscriptionOriginCustom,
		InstanceLabel: &lab,
		Title:         "Soak " + label,
		Metadata: generated.AnimeMetadataDraft{
			Bgm:  &generated.NullableBGMBinding{WorkId: "bgm-" + key},
			Tmdb: &generated.NullableTMDBBinding{WorkId: "tmdb-" + key, MediaKind: generated.TMDBMediaKindTv, Season: 1},
		},
		Subgroups: generated.SubgroupsSection{Entries: []generated.SubgroupScoreEntry{}, MaxRss: 3},
		Rules:     rules,
	}
	for i := 0; i < sources; i++ {
		draft.RssSources = append(draft.RssSources, generated.RSSSourceDraft{
			SourceKind: generated.SourceKindCustomRss,
			Locator:    fmt.Sprintf("%s/%s/%d.xml", locator, key, i),
			GroupName:  fmt.Sprintf("Group%d", i),
			Title:      "Soak " + label,
		})
	}
	b, err := frozenpolicy.SeedBusiness(ctx, s.DB, `vars.actual = {title: input.release_title};`)
	if err != nil {
		return generated.SubscribeResult{}, err
	}
	digest, err := b.Digest()
	if err != nil {
		return generated.SubscribeResult{}, err
	}
	raw, err := json.Marshal(b)
	if err != nil {
		return generated.SubscribeResult{}, err
	}
	var wire generated.BusinessSnapshot
	if err := json.Unmarshal(raw, &wire); err != nil {
		return generated.SubscribeResult{}, err
	}
	draft.Business = &wire
	draft.BusinessDigest = &digest
	return s.Draft.SubscribeAnime(ctx, generated.SubscribeAnimeInput{Draft: draft, Start: new(true)}, key)
}

func ensureServices(t *testing.T) *ownerServices {
	t.Helper()
	_, rt := dbs(t)
	svc, err := newOwnerServices(rt)
	if err != nil {
		t.Fatalf("owner services: %v", err)
	}
	return svc
}
