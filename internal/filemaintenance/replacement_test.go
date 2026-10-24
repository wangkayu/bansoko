package filemaintenance

import (
	"database/sql"
	"encoding/json"
	"testing"

	"bansoko/internal/qualityprofile"
)

func sizePtr(size int64) *int64 { return &size }

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
	if !got.Better || got.Decided != qualityprofile.KeyReleaseResolution || len(got.Steps) != 1 {
		t.Fatalf("1080p must beat 720p on release.resolution: %+v", got)
	}
	if got.Reason != "better on release.resolution" {
		t.Fatalf("reason = %q", got.Reason)
	}
}

// The first differing item decides; later items are never read.
func TestCompareLexicographicThenSizeThenKeepExisting(t *testing.T) {
	profile := Profile{Version: qualityprofile.Version, Items: []qualityprofile.Item{
		{Key: qualityprofile.KeyReleaseLang, Ranks: [][]string{{"chs"}, {"cht"}}},
		{Key: qualityprofile.KeyReleaseResolution, Ranks: [][]string{{"1080p"}, {"720p"}}},
	}}
	candidate := released(Facts{Language: "chs", Resolution: "720p", Size: sizePtr(1)})
	current := released(Facts{Language: "cht", Resolution: "1080p", Size: sizePtr(9)})
	if got := Compare(candidate, current, profile); !got.Better || got.Decided != qualityprofile.KeyReleaseLang {
		t.Fatalf("the language item must decide before resolution: %+v", got)
	}
	same := released(Facts{Language: "chs", Resolution: "720p", Size: sizePtr(5)})
	if got := Compare(same, candidate, profile); !got.Better || got.Decided != qualityprofile.SizeStep {
		t.Fatalf("all items equal: the larger size must win: %+v", got)
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
	values.Set(qualityprofile.KeyAniBTLanguage, "CHS", "CHT")
	values.Subgroup = &qualityprofile.SubgroupSource{Provider: "anibt", GroupRef: "g"}
	facts := Facts{}.withFrozenValues(values.JSON())
	if len(facts.Values[qualityprofile.KeyAniBTLanguage]) != 2 || facts.Subgroup == nil || facts.Subgroup.GroupRef != "g" {
		t.Fatalf("facts = %+v", facts)
	}
	if empty := (Facts{}).withFrozenValues([]byte(`{}`)); len(empty.Values) != 0 || empty.Subgroup != nil {
		t.Fatalf("an empty document has no values: %+v", empty)
	}
}

func TestParseProfileFailsClosed(t *testing.T) {
	if _, err := ParseProfile(json.RawMessage(`{"version":"profile-v0","dimensions":[],"ranks":{},"codec_enabled":false}`)); err == nil {
		t.Fatal("a profile-v0 document must not evaluate")
	}
	profile, err := ParseProfile(qualityprofile.DefaultJSON())
	if err != nil || len(profile.Items) != 4 {
		t.Fatalf("default profile = %+v, %v", profile, err)
	}
}

func TestComparedDimensionsMapItemsToApprovalCategories(t *testing.T) {
	got := comparedDimensions(qualityprofile.Default())
	want := []string{"group", "languages", "resolution", "revision", "size"}
	if len(got) != len(want) {
		t.Fatalf("dimensions = %v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("dimensions = %v, want %v", got, want)
		}
	}
	codecOnly := rankProfile(qualityprofile.KeyRtCodec, "HEVC")
	if got := comparedDimensions(codecOnly); len(got) != 1 || got[0] != "size" {
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
