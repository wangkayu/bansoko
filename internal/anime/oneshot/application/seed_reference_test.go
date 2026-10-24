package application

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	animeapplication "bansoko/internal/anime/application"
	animeport "bansoko/internal/anime/port"
	"bansoko/internal/apperrors"
	rssapplication "bansoko/internal/rss/application"
	"bansoko/internal/testutil/animetest"
)

// seedReferenceOf strips a locator-form seed down to the reference an agent
// sends: provenance, snapshot identity and stable key, no locator and no facts.
func seedReferenceOf(seed SelectedSeed) SelectedSeed {
	return SelectedSeed{
		Provider:            seed.Provider,
		ProviderEntryRef:    seed.ProviderEntryRef,
		ProviderGroupRef:    seed.ProviderGroupRef,
		SnapshotID:          seed.SnapshotID,
		SnapshotVersion:     seed.SnapshotVersion,
		SnapshotContentHash: seed.SnapshotContentHash,
		StableSeedKey:       seed.StableSeedKey,
	}
}

func businessCode(err error) string {
	var business *apperrors.Business
	if errors.As(err, &business) {
		return business.Code
	}
	return ""
}

// A reference resolves to exactly the locator-form seed of the same snapshot
// seed: same struct, same canonical request hash in both config input
// variants. Everything after the entry point consumes only this value.
func TestSeedReferenceResolvesToTheLocatorForm(t *testing.T) {
	locatorForm := canonicalSelectedSeed(boundOneShotSeed())
	source := &fakeSeedPreviewSource{snapshot: matchingSeedPreviewSnapshot(boundOneShotSeed())}
	service := &Service{seedPreviews: source}

	references := map[string]SelectedSeed{
		"identity only": seedReferenceOf(locatorForm),
		"identity plus the seed view facts": func() SelectedSeed {
			ref := seedReferenceOf(locatorForm)
			ref.Title = locatorForm.Title
			ref.PublishedAt = locatorForm.PublishedAt.In(time.FixedZone("UTC+8", 8*3600))
			ref.SizeBytes = animetest.Int64Ptr(*locatorForm.SizeBytes)
			ref.InfoHash = animetest.StringPtr(strings.ToUpper(*locatorForm.InfoHash))
			return ref
		}(),
	}
	for name, reference := range references {
		t.Run(name, func(t *testing.T) {
			resolved, err := service.resolveSeedReference(context.Background(), reference)
			if err != nil {
				t.Fatalf("resolve: %v", err)
			}
			if !reflect.DeepEqual(resolved, locatorForm) {
				t.Fatalf("resolved seed differs from the locator form:\n got %+v\nwant %+v", resolved, locatorForm)
			}
			if source.lastIdentity.SnapshotID != reference.SnapshotID || source.lastIdentity.SnapshotVersion != reference.SnapshotVersion || source.lastIdentity.ContentHash != reference.SnapshotContentHash {
				t.Fatalf("snapshot read with identity %+v", source.lastIdentity)
			}
			byLocator := AcquireSeedInput{CandidateID: "c-1", Revision: 2, EffectiveDigest: "sha256:e", Seed: locatorForm}
			byReference := byLocator
			byReference.Seed = resolved
			for _, hash := range []func(AcquireSeedInput, string) (string, error){
				canonicalOneShotRequestHash,
				func(input AcquireSeedInput, key string) (string, error) {
					return canonicalOneShotCurrentInputRequestHash("sha256:d", input, key)
				},
			} {
				left, err := hash(byLocator, "key-0001")
				if err != nil {
					t.Fatal(err)
				}
				right, err := hash(byReference, "key-0001")
				if err != nil {
					t.Fatal(err)
				}
				if left != right {
					t.Fatalf("replay identity differs: %s vs %s", left, right)
				}
			}
			frozenLocator, err := json.Marshal(oneShotSelectedSeedJSONFromDomain(locatorForm))
			if err != nil {
				t.Fatal(err)
			}
			frozenReference, err := json.Marshal(oneShotSelectedSeedJSONFromDomain(resolved))
			if err != nil {
				t.Fatal(err)
			}
			if string(frozenLocator) != string(frozenReference) {
				t.Fatalf("frozen seed bytes differ:\n%s\n%s", frozenLocator, frozenReference)
			}
		})
	}
}

// The published time is the snapshot instant in UTC, whatever location the
// snapshot read returns, so the frozen bytes do not depend on the session zone.
func TestSeedReferencePublishedAtIsUTC(t *testing.T) {
	snapshot := matchingSeedPreviewSnapshot(boundOneShotSeed())
	snapshot.Seeds[0].PublishedAt = snapshot.Seeds[0].PublishedAt.In(time.FixedZone("UTC+8", 8*3600))
	service := &Service{seedPreviews: &fakeSeedPreviewSource{snapshot: snapshot}}
	resolved, err := service.resolveSeedReference(context.Background(), seedReferenceOf(boundOneShotSeed()))
	if err != nil {
		t.Fatal(err)
	}
	if resolved.PublishedAt.Location() != time.UTC || !resolved.PublishedAt.Equal(boundOneShotSeed().PublishedAt) {
		t.Fatalf("published_at = %v, want the same instant in UTC", resolved.PublishedAt)
	}
}

func TestSeedReferenceFailsClosed(t *testing.T) {
	reference := seedReferenceOf(boundOneShotSeed())
	matching := func(mutate func(*animeapplication.SeedPreviewSnapshot)) *fakeSeedPreviewSource {
		snapshot := matchingSeedPreviewSnapshot(boundOneShotSeed())
		if mutate != nil {
			mutate(&snapshot)
		}
		return &fakeSeedPreviewSource{snapshot: snapshot}
	}
	cases := []struct {
		name     string
		source   *fakeSeedPreviewSource
		nilSrc   bool
		mutate   func(*SelectedSeed)
		wantCode string
	}{
		{name: "missing snapshot id", source: matching(nil), mutate: func(s *SelectedSeed) { s.SnapshotID = "" }, wantCode: "SEED_INVALID"},
		{name: "missing snapshot version", source: matching(nil), mutate: func(s *SelectedSeed) { s.SnapshotVersion = 0 }, wantCode: "SEED_INVALID"},
		{name: "missing content hash", source: matching(nil), mutate: func(s *SelectedSeed) { s.SnapshotContentHash = " " }, wantCode: "SEED_INVALID"},
		{name: "missing stable seed key", source: matching(nil), mutate: func(s *SelectedSeed) { s.StableSeedKey = "" }, wantCode: "SEED_INVALID"},
		{name: "superseded or missing snapshot", source: &fakeSeedPreviewSource{err: errRefreshRequiredForTest()}, wantCode: "SEED_PREVIEW_STALE"},
		{name: "stale snapshot", source: matching(func(s *animeapplication.SeedPreviewSnapshot) { s.Freshness = "stale" }), wantCode: "SEED_PREVIEW_STALE"},
		{name: "other version", source: matching(func(s *animeapplication.SeedPreviewSnapshot) { s.SnapshotVersion++ }), wantCode: "SEED_PREVIEW_STALE"},
		{name: "other content hash", source: matching(func(s *animeapplication.SeedPreviewSnapshot) { s.ContentHash = "sha256:" + strings.Repeat("e", 64) }), wantCode: "SEED_PREVIEW_STALE"},
		{name: "other group", source: matching(func(s *animeapplication.SeedPreviewSnapshot) { s.ProviderGroupRef = "Other-Group" }), wantCode: "SEED_PREVIEW_STALE"},
		{name: "key not in snapshot", source: matching(nil), mutate: func(s *SelectedSeed) { s.StableSeedKey = "seed-99" }, wantCode: "SEED_PREVIEW_STALE"},
		{name: "supplied title differs", source: matching(nil), mutate: func(s *SelectedSeed) { s.Title = "[Other] Release" }, wantCode: "SEED_PREVIEW_STALE"},
		{name: "supplied info hash differs", source: matching(nil), mutate: func(s *SelectedSeed) { s.InfoHash = animetest.StringPtr(strings.Repeat("f", 40)) }, wantCode: "SEED_PREVIEW_STALE"},
		{name: "supplied digest differs", source: matching(nil), mutate: func(s *SelectedSeed) {
			s.ExpectedPayloadDigest = animetest.StringPtr("sha256:" + strings.Repeat("0", 64))
		}, wantCode: "SEED_PREVIEW_STALE"},
		{name: "supplied size differs", source: matching(nil), mutate: func(s *SelectedSeed) { s.SizeBytes = animetest.Int64Ptr(1) }, wantCode: "SEED_PREVIEW_STALE"},
		{name: "store failure", source: &fakeSeedPreviewSource{err: errors.New("connection reset")}, wantCode: "SEED_PREVIEW_UNAVAILABLE"},
		{name: "typed catalog refusal passes through", source: &fakeSeedPreviewSource{err: &apperrors.Business{Status: 422, Code: "FEED_SNAPSHOT_IDENTITY_INVALID", Message: "bad"}}, wantCode: "FEED_SNAPSHOT_IDENTITY_INVALID"},
		{name: "no seed source", nilSrc: true, wantCode: "SEED_PREVIEW_UNAVAILABLE"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			service := &Service{}
			if !tc.nilSrc {
				service.seedPreviews = tc.source
			}
			input := reference
			if tc.mutate != nil {
				tc.mutate(&input)
			}
			_, err := service.resolveSeedReference(context.Background(), input)
			if got := businessCode(err); got != tc.wantCode {
				t.Fatalf("error = %v (code %q), want %s", err, got, tc.wantCode)
			}
		})
	}
}

func TestSeedReferenceShape(t *testing.T) {
	if isSeedReference(boundOneShotSeed()) {
		t.Fatal("a seed with a locator is the locator form")
	}
	if !isSeedReference(seedReferenceOf(boundOneShotSeed())) {
		t.Fatal("a seed without a locator is a reference")
	}
	if err := validateSeedReference(seedReferenceOf(boundOneShotSeed())); err != nil {
		t.Fatalf("complete reference: %v", err)
	}
}

// A replayed reference resolves against the marker's frozen seed: the same
// reference (with or without matching facts) yields the frozen seed, anything
// else is a reused key.
func TestFrozenSeedForReference(t *testing.T) {
	frozen := canonicalSelectedSeed(boundOneShotSeed())
	raw, err := encodeOneShotInputSnapshot(OneShotInputSnapshot{CandidateID: "c-1", Revision: 1, SelectedSeed: frozen, EffectiveSections: json.RawMessage(`{}`), TargetFacts: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	record := animeport.OneShotMarkerRecord{InputSnapshot: raw}
	got, err := frozenSeedForReference(record, seedReferenceOf(frozen))
	if err != nil {
		t.Fatalf("matching reference: %v", err)
	}
	if !got.PublishedAt.Equal(frozen.PublishedAt) || got.Locator != frozen.Locator || got.StableSeedKey != frozen.StableSeedKey {
		t.Fatalf("replayed seed = %+v, want the frozen seed", got)
	}
	withFacts := seedReferenceOf(frozen)
	withFacts.Title = frozen.Title
	if _, err := frozenSeedForReference(record, withFacts); err != nil {
		t.Fatalf("matching facts: %v", err)
	}
	for name, mutate := range map[string]func(*SelectedSeed){
		"other key":      func(s *SelectedSeed) { s.StableSeedKey = "seed-02" },
		"other version":  func(s *SelectedSeed) { s.SnapshotVersion++ },
		"other snapshot": func(s *SelectedSeed) { s.SnapshotID = "snap-2" },
		"other title":    func(s *SelectedSeed) { s.Title = "x" },
	} {
		reference := seedReferenceOf(frozen)
		mutate(&reference)
		if _, err := frozenSeedForReference(record, reference); businessCode(err) != "IDEMPOTENCY_KEY_REUSED" {
			t.Fatalf("%s: err = %v, want IDEMPOTENCY_KEY_REUSED", name, err)
		}
	}
	if _, err := frozenSeedForReference(animeport.OneShotMarkerRecord{}, seedReferenceOf(frozen)); businessCode(err) != "IDEMPOTENCY_KEY_REUSED" {
		t.Fatalf("legacy marker without snapshot: err = %v", err)
	}
}

// The AniBT tag set is the one seed fact the request cannot carry: the provider
// sends it as namespaced XML, never as title text, and the locator form has no
// field for it. The verified preview snapshot is therefore its only source, and
// the shared source-facts encoder must freeze it exactly as the subscribed path
// does — otherwise a one-shot download fails every `anibt.*` filter that the
// same release passes once subscribed.
func TestSeedReferenceKeepsTheSnapshotAniBTTagSet(t *testing.T) {
	tags := acquisitiondomain.StoredAniBTTags{
		Schema: acquisitiondomain.AniBTTagSchemaV1,
		Tags:   []string{"resolution:1080p", "language:CHS"},
	}
	snapshot := matchingSeedPreviewSnapshot(boundOneShotSeed())
	snapshot.Seeds[0].AniBTTags = &tags
	service := &Service{seedPreviews: &fakeSeedPreviewSource{snapshot: snapshot}}

	resolved, err := service.resolveSeedReference(context.Background(), seedReferenceOf(boundOneShotSeed()))
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if resolved.AniBTTags == nil || !reflect.DeepEqual(resolved.AniBTTags.Tags, tags.Tags) {
		t.Fatalf("resolved seed tags = %+v, want the snapshot's %+v", resolved.AniBTTags, tags)
	}

	// The tag set never enters the replay identity: a snapshot that gained tags
	// still names the same request as the locator-form seed without them.
	untagged := canonicalSelectedSeed(boundOneShotSeed())
	if untagged.AniBTTags != nil {
		t.Fatal("fixture seed unexpectedly carries tags")
	}
	left, err := canonicalOneShotRequestHash(AcquireSeedInput{CandidateID: "c-1", Revision: 2, EffectiveDigest: "sha256:e", Seed: untagged}, "key-0001")
	if err != nil {
		t.Fatal(err)
	}
	right, err := canonicalOneShotRequestHash(AcquireSeedInput{CandidateID: "c-1", Revision: 2, EffectiveDigest: "sha256:e", Seed: resolved}, "key-0001")
	if err != nil {
		t.Fatal(err)
	}
	if left != right {
		t.Fatalf("replay identity moved with the tag set: %s vs %s", left, right)
	}

	// The frozen input snapshot does carry it, so a replay re-reads the same
	// facts rather than resolving the (possibly purged) snapshot again.
	item := buildOneShotFeedItem(animeapplication.TorrentPayload{}, resolved)
	_, _, facts, err := rssapplication.EncodeSourceFacts(item, "12")
	if err != nil {
		t.Fatal(err)
	}
	var stored struct {
		AniBT *acquisitiondomain.StoredAniBTTags `json:"anibt"`
	}
	if err := json.Unmarshal(facts, &stored); err != nil {
		t.Fatal(err)
	}
	if stored.AniBT == nil || !reflect.DeepEqual(stored.AniBT.Tags, tags.Tags) {
		t.Fatalf("frozen source facts anibt = %+v, want %+v", stored.AniBT, tags)
	}
}
