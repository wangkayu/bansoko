package filemaintenance

import (
	"database/sql"
	"encoding/json"
	"testing"

	"bansoko/internal/qualityprofile"
	releasefacts "bansoko/internal/release/facts"
)

func sizePtr(size int64) *int64 { return &size }

func int64Ptr(value int64) *int64 { return &value }

// Compare reads only the frozen values: the title-parse fields alone do not
// rank a release.
func TestCompareReadsFrozenValuesOnly(t *testing.T) {
	profile := resolutionProfile("2160p", "1080p", "720p")
	candidate := Facts{Resolution: "1080p"}
	current := Facts{Resolution: "720p"}
	if got := Compare(candidate, current, profile); got.Better || !got.Equal {
		t.Fatalf("facts without frozen values must tie: %+v", got)
	}
	got := Compare(released(candidate), released(current), profile)
	if !got.Better || got.Decided != qualityprofile.KeyRtResolution || len(got.Steps) != 1 {
		t.Fatalf("1080p must beat 720p on rt.resolution: %+v", got)
	}
	if got.Reason != "better on rt.resolution" {
		t.Fatalf("reason = %q", got.Reason)
	}
}

// The first differing item decides; later items are never read.
func TestCompareLexicographicThenKeepExisting(t *testing.T) {
	profile := Profile{Version: qualityprofile.Version, Items: []qualityprofile.Item{
		{Key: qualityprofile.KeyRtSubLang, Ranks: [][]string{{"chs"}, {"cht"}}},
		{Key: qualityprofile.KeyRtResolution, Ranks: [][]string{{"1080p"}, {"720p"}}},
	}}
	candidate := released(Facts{Language: "chs", Resolution: "720p", Size: sizePtr(1)})
	current := released(Facts{Language: "cht", Resolution: "1080p", Size: sizePtr(9)})
	if got := Compare(candidate, current, profile); !got.Better || got.Decided != qualityprofile.KeyRtSubLang {
		t.Fatalf("the language item must decide before resolution: %+v", got)
	}
	same := released(Facts{Language: "chs", Resolution: "720p", Size: sizePtr(5)})
	if got := Compare(same, candidate, profile); got.Better || !got.Equal || got.Decided != "" {
		t.Fatalf("all items equal: size must not decide: %+v", got)
	}
	unknown := released(Facts{Language: "chs", Resolution: "720p"})
	if got := Compare(unknown, candidate, profile); got.Better || !got.Equal {
		t.Fatalf("an unknown size must keep the existing file: %+v", got)
	}
}

// The subscription score is looked up by the frozen (provider, group ref)
// source only; a release without a source or score ranks lowest.
func TestScoredLooksUpTheFrozenSource(t *testing.T) {
	scores := SubgroupScores{{Provider: "anibt", GroupRef: "g-1"}: 70}
	withSource := Facts{Group: "Other Title Name", Subgroup: &qualityprofile.SubgroupSource{Provider: "anibt", GroupRef: "g-1"}}.scored(scores)
	if withSource.Score == nil || *withSource.Score != 70 {
		t.Fatalf("score = %v, want 70", withSource.Score)
	}
	byName := Facts{Group: "g-1"}.scored(scores)
	if byName.Score != nil {
		t.Fatal("a title group name must never resolve a score")
	}
	unscored := Facts{Subgroup: &qualityprofile.SubgroupSource{Provider: "mikan", GroupRef: "g-1"}, Score: int64Ptr(5)}.scored(scores)
	if unscored.Score != nil {
		t.Fatal("a source without a selection row has no score")
	}
	profile := Profile{Version: qualityprofile.Version, Items: []qualityprofile.Item{{Key: qualityprofile.KeySubgroupScore, Ranks: [][]string{}}}}
	if got := Compare(withSource, unscored, profile); !got.Better {
		t.Fatalf("a scored source must beat an unscored one: %+v", got)
	}
}

func TestWithFrozenValuesReadsTheStoredDocument(t *testing.T) {
	values := qualityprofile.NewValues()
	values.SourceDigest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	values.Set(qualityprofile.KeyAniBTLanguage, "CHS", "CHT")
	values.Subgroup = &qualityprofile.SubgroupSource{Provider: "anibt", GroupRef: "g"}
	facts, err := Facts{}.withFrozenValues(values.JSON())
	if err != nil {
		t.Fatal(err)
	}
	if len(facts.Values[qualityprofile.KeyAniBTLanguage]) != 2 || facts.Subgroup == nil || facts.Subgroup.GroupRef != "g" {
		t.Fatalf("facts = %+v", facts)
	}
	if _, err := (Facts{}).withFrozenValues([]byte(`{}`)); err == nil {
		t.Fatal("an unversioned cache must fail closed")
	}
}

func TestCurrentComparisonRebuildsLegacyCacheFromFrozenSource(t *testing.T) {
	source := releasefacts.CompareSource{
		SourceFacts: []byte(`{"title_tags":{"schema":"reltitle-attrs-v1","implementation":"reltitle@test","tags":["resolution:1080p"]}}`),
		RSSSourceID: "src-1", SourceKind: "custom_rss",
	}
	for _, legacy := range []string{`{}`, `{"version":"compare-values-v1","values":{"rt.resolution":["720p"]}}`} {
		current, err := (Facts{}).withCurrentComparison([]byte(legacy), source)
		if err != nil {
			t.Fatalf("rebuild %s: %v", legacy, err)
		}
		if got := current.Values[qualityprofile.KeyRtResolution]; len(got) != 1 || got[0] != "1080p" {
			t.Fatalf("rebuilt values = %+v", current.Values)
		}
		if current.Subgroup == nil || current.Subgroup.Key() != "custom:src-1" || current.SourceDigest == "" {
			t.Fatalf("rebuilt source = %+v", current)
		}
	}
}

func TestCurrentComparisonKeepsValidFrozenCache(t *testing.T) {
	values := qualityprofile.NewValues()
	values.SourceDigest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	values.Set(qualityprofile.KeyRtResolution, "720p")
	current, err := (Facts{}).withCurrentComparison(values.JSON(), releasefacts.CompareSource{})
	if err != nil {
		t.Fatal(err)
	}
	if got := current.Values[qualityprofile.KeyRtResolution]; len(got) != 1 || got[0] != "720p" {
		t.Fatalf("valid frozen cache was replaced: %+v", current.Values)
	}
}

func TestParseProfileFailsClosed(t *testing.T) {
	if _, err := ParseProfile(json.RawMessage(`{"version":"profile-v0","dimensions":[],"ranks":{},"codec_enabled":false}`)); err == nil {
		t.Fatal("a profile-v0 document must not evaluate")
	}
	profile, err := ParseProfile(qualityprofile.DefaultJSON())
	if err != nil || len(profile.Items) != 6 {
		t.Fatalf("default profile = %+v, %v", profile, err)
	}
}

func TestComparedDimensionsMapItemsToApprovalCategories(t *testing.T) {
	got := comparedDimensions(qualityprofile.Default())
	want := []string{"group", "languages", "resolution", "revision"}
	if len(got) != len(want) {
		t.Fatalf("dimensions = %v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("dimensions = %v, want %v", got, want)
		}
	}
	codecOnly := rankProfile(qualityprofile.KeyRtCodec, "HEVC")
	if got := comparedDimensions(codecOnly); len(got) != 0 {
		t.Fatalf("an item without a category is not named: %v", got)
	}
}

func seedPlacement(t *testing.T, db *sql.DB, id, animeID, path, fingerprint string, coverage []string, createdAt string) {
	t.Helper()
	if _, err := db.Exec(`
INSERT INTO placements(id, physical_path, managed_anime_id, source_evidence_type, source_owner_ref,
 source_fingerprint, target_fingerprint, quality_facts_ref, facts_version, verification_state, state, version, created_at, updated_at)
VALUES ($1, $2, $3, 'file_operation', $4, $5, $5, NULL, 'parser-v1', 'verified', 'active', 1, $6::timestamptz, $6::timestamptz)`,
		id, path, animeID, id+"-evidence", fingerprint, createdAt); err != nil {
		t.Fatal(err)
	}
	for i, key := range coverage {
		if _, err := db.Exec(`INSERT INTO placement_coverage(placement_id, observation_key, coverage_ordinal, facts_version, evidence_ref) VALUES ($1, $2, $3, 'parser-v1', $4)`, id, key, i, id+"-evidence"); err != nil {
			t.Fatal(err)
		}
	}
}
