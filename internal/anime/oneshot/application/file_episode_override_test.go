package application

import (
	"encoding/json"
	"reflect"
	"testing"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	acquisitiontarget "bansoko/internal/acquisition/target"
	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/filekinds"
)

func TestOneShotOverridesEnterCanonicalRequestIdentity(t *testing.T) {
	zero := 0
	input := AcquireSeedInput{CandidateID: "candidate-1", Revision: 2, EffectiveDigest: "sha256:e", Seed: validOneShotSeed(), Overrides: []acquisitiontarget.FileEpisodeOverride{
		{Path: "Pack/b.mkv", Episode: 9},
		{Path: "Pack/a.mkv", Season: &zero, Episode: 7},
	}}
	reordered := input
	reordered.Overrides = []acquisitiontarget.FileEpisodeOverride{input.Overrides[1], input.Overrides[0]}
	first, err := canonicalOneShotRequestHash(input, "key-0001")
	if err != nil {
		t.Fatal(err)
	}
	second, err := canonicalOneShotRequestHash(reordered, "key-0001")
	if err != nil || first != second {
		t.Fatalf("canonical override order changed request hash: %s != %s (%v)", first, second, err)
	}
	changed := input
	changed.Overrides = append([]acquisitiontarget.FileEpisodeOverride(nil), input.Overrides...)
	changed.Overrides[0].Episode++
	third, err := canonicalOneShotRequestHash(changed, "key-0001")
	if err != nil || third == first {
		t.Fatalf("different overrides replayed the same request: %s (%v)", third, err)
	}
	currentFirst, err := canonicalOneShotCurrentInputRequestHash("sha256:d", input, "key-0001")
	if err != nil {
		t.Fatal(err)
	}
	currentSecond, err := canonicalOneShotCurrentInputRequestHash("sha256:d", reordered, "key-0001")
	if err != nil || currentFirst != currentSecond {
		t.Fatalf("current-input override order changed request hash: %v", err)
	}
}

func TestOneShotInputSnapshotRoundTripsOverrides(t *testing.T) {
	zero := 0
	snapshot := OneShotInputSnapshot{
		CandidateID: "candidate-1", Revision: 1, SelectedSeed: validOneShotSeed(),
		Overrides: []acquisitiontarget.FileEpisodeOverride{{Path: "Pack/a.mkv", Season: &zero, Episode: 7}},
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var decoded OneShotInputSnapshot
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded.Overrides, snapshot.Overrides) || decoded.Overrides[0].Season == nil || *decoded.Overrides[0].Season != 0 {
		t.Fatalf("snapshot overrides = %+v", decoded.Overrides)
	}
	var legacy OneShotInputSnapshot
	if err := json.Unmarshal([]byte(`{"selected_seed":{"locator":"https://example.invalid/a.torrent","provider":"mikan","provider_entry_ref":"e","provider_group_ref":"g","published_at":"2026-01-01T00:00:00Z","title":"a"}}`), &legacy); err != nil || legacy.Overrides != nil {
		t.Fatalf("legacy snapshot overrides = %+v, %v", legacy.Overrides, err)
	}
}

func TestValidateOneShotFileOverridesAgainstSelectedVideos(t *testing.T) {
	files := []acquisitiondomain.ReleaseFile{
		{SourcePath: "Pack/a.mkv", Filename: "a.mkv"},
		{SourcePath: "Pack/a.ass", Filename: "a.ass"},
		{SourcePath: "Pack/nested/b.mkv", Filename: "b.mkv"},
	}
	selection := filekinds.NewSelection([]string{"Pack/a.mkv", "Pack/a.ass"})
	valid := []acquisitiontarget.FileEpisodeOverride{{Path: "Pack/a.mkv", Episode: 1}}
	if err := validateOneShotFileOverrides(valid, files, animedomain.FileFilterRules{}, selection); err != nil {
		t.Fatal(err)
	}
	for _, overrides := range [][]acquisitiontarget.FileEpisodeOverride{
		{{Path: "Pack/missing.mkv", Episode: 1}},
		{{Path: "Pack/a.ass", Episode: 1}},
		{{Path: "Pack/nested/b.mkv", Episode: 1}},
	} {
		if err := validateOneShotFileOverrides(overrides, files, animedomain.FileFilterRules{}, selection); err == nil {
			t.Fatalf("out-of-scope overrides were accepted: %+v", overrides)
		} else {
			requireFileSelectionInvalid(t, err, "override path")
		}
	}
}
