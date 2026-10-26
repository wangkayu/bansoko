package qualityprofile

import (
	"encoding/json"
	"strings"
	"testing"
)

func i64(v int64) *int64 { return &v }

func subject(values map[string][]string, score, size *int64) Subject {
	return Subject{Values: values, Score: score, Size: size}
}

func TestDefaultProfileIsValidAndBuiltInOnly(t *testing.T) {
	profile, err := Parse(DefaultJSON())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{KeySubgroupScore, KeyAniBTLanguage, KeyRtSubLang, KeyReleaseResolution, KeyReleaseRevision}
	if strings.Join(profile.Keys(), ",") != strings.Join(want, ",") {
		t.Fatalf("default items = %v", profile.Keys())
	}
}

func TestValidateRejections(t *testing.T) {
	cases := map[string]string{
		"unknown version":      `{"version":"profile-v0","items":[]}`,
		"unknown key":          `{"version":"profile-v1","items":[{"key":"media.resolution","ranks":[]}]}`,
		"duplicate key":        `{"version":"profile-v1","items":[{"key":"release.lang","ranks":[]},{"key":"release.lang","ranks":[]}]}`,
		"missing ranks":        `{"version":"profile-v1","items":[{"key":"release.lang"}]}`,
		"score with values":    `{"version":"profile-v1","items":[{"key":"subgroup.score","ranks":[["x"]]}]}`,
		"revision with values": `{"version":"profile-v1","items":[{"key":"release.revision","ranks":[["v2"]]}]}`,
		"combo on single item": `{"version":"profile-v1","items":[{"key":"release.lang","ranks":[["sc","tc"]]}]}`,
		"duplicate set":        `{"version":"profile-v1","items":[{"key":"anibt.language","ranks":[["CHS","CHT"],["cht","chs"]]}]}`,
		"duplicate single":     `{"version":"profile-v1","items":[{"key":"release.resolution","ranks":[["1080p"],["1080P"]]}]}`,
		"repeat inside entry":  `{"version":"profile-v1","items":[{"key":"rt.sub_lang","ranks":[["SC","sc"]]}]}`,
		"blank value":          `{"version":"profile-v1","items":[{"key":"release.lang","ranks":[[" "]]}]}`,
		"untrimmed value":      `{"version":"profile-v1","items":[{"key":"release.lang","ranks":[[" sc"]]}]}`,
		"joined value":         `{"version":"profile-v1","items":[{"key":"anibt.language","ranks":[["CHS+CHT"]]}]}`,
		"empty entry":          `{"version":"profile-v1","items":[{"key":"anibt.language","ranks":[[]]}]}`,
		"unknown field":        `{"version":"profile-v1","items":[],"cutoff":{}}`,
		"null items":           `{"version":"profile-v1","items":null}`,
	}
	for name, raw := range cases {
		if _, err := Parse(json.RawMessage(raw)); err == nil {
			t.Errorf("%s: accepted %s", name, raw)
		}
	}
	ok := `{"version":"profile-v1","items":[{"key":"anibt.language","ranks":[["CHS","CHT","JPN"],["CHS","CHT"],["CHS"]]},{"key":"rt.codec","ranks":[]}]}`
	if _, err := Parse(json.RawMessage(ok)); err != nil {
		t.Fatalf("valid profile rejected: %v", err)
	}
}

func TestLexicographicFirstDifferenceDecides(t *testing.T) {
	profile := Profile{Version: Version, Items: []Item{
		{Key: KeyReleaseLang, Ranks: [][]string{{"sc"}, {"tc"}}},
		{Key: KeyReleaseResolution, Ranks: [][]string{{"2160p"}, {"1080p"}}},
	}}
	// Candidate has the better language but the worse resolution: language decides.
	candidate := subject(map[string][]string{KeyReleaseLang: {"sc"}, KeyReleaseResolution: {"1080p"}}, nil, i64(1))
	current := subject(map[string][]string{KeyReleaseLang: {"tc"}, KeyReleaseResolution: {"2160p"}}, nil, i64(9))
	result := Compare(candidate, current, profile)
	if !result.Better || result.Decided != KeyReleaseLang || len(result.Steps) != 1 {
		t.Fatalf("result = %+v", result)
	}
	// Reversed: the candidate loses on the first item.
	if back := Compare(current, candidate, profile); back.Better || back.Equal || back.Decided != KeyReleaseLang {
		t.Fatalf("reverse = %+v", back)
	}
}

func TestSizeTiebreakAndKeepExisting(t *testing.T) {
	profile := Profile{Version: Version, Items: []Item{{Key: KeyReleaseResolution, Ranks: [][]string{{"1080p"}}}}}
	values := map[string][]string{KeyReleaseResolution: {"1080p"}}
	if r := Compare(subject(values, nil, i64(2)), subject(values, nil, i64(1)), profile); !r.Better || r.Decided != SizeStep {
		t.Fatalf("larger size must win: %+v", r)
	}
	if r := Compare(subject(values, nil, i64(1)), subject(values, nil, i64(2)), profile); r.Better || r.Equal {
		t.Fatalf("smaller size must lose: %+v", r)
	}
	if r := Compare(subject(values, nil, i64(1)), subject(values, nil, i64(1)), profile); r.Better || !r.Equal {
		t.Fatalf("equal size keeps existing: %+v", r)
	}
	if r := Compare(subject(values, nil, i64(5)), subject(values, nil, nil), profile); r.Better || !r.Equal {
		t.Fatalf("unknown size keeps existing: %+v", r)
	}
}

func TestContainmentWidestFirstAndBestHitWins(t *testing.T) {
	item := Item{Key: KeyAniBTLanguage, Ranks: [][]string{{"CHS", "CHT", "JPN"}, {"CHS", "CHT"}, {"CHS"}, {"CHT"}}}
	cases := []struct {
		have []string
		want int
		hit  bool
	}{
		{[]string{"JPN", "CHT", "CHS"}, 0, true},
		{[]string{"cht", "chs"}, 1, true},        // order and case do not matter
		{[]string{"CHS", "CHT", "ENG"}, 1, true}, // a superset still contains CHS+CHT
		{[]string{"CHS"}, 2, true},               // CHS alone does not hit CHS+CHT
		{[]string{"CHT", "ENG"}, 3, true},
		{[]string{"ENG"}, 0, false}, // unlisted
		{nil, 0, false},             // missing
	}
	for _, tc := range cases {
		index, ok := RankIndex(item.Ranks, tc.have)
		if ok != tc.hit || (ok && index != tc.want) {
			t.Errorf("RankIndex(%v) = %d,%v want %d,%v", tc.have, index, ok, tc.want, tc.hit)
		}
	}
	profile := Profile{Version: Version, Items: []Item{item}}
	wide := subject(map[string][]string{KeyAniBTLanguage: {"CHS", "CHT"}}, nil, nil)
	narrow := subject(map[string][]string{KeyAniBTLanguage: {"CHT"}}, nil, nil)
	if r := Compare(wide, narrow, profile); !r.Better || r.Steps[0].Candidate != "CHS+CHT" || !r.Steps[0].CandidateListed {
		t.Fatalf("wide set must beat a narrow one: %+v", r)
	}
}

func TestMissingAndUnlistedRankWorst(t *testing.T) {
	profile := Profile{Version: Version, Items: []Item{{Key: KeyRtSource, Ranks: [][]string{{"BDRip"}, {"WEB-DL"}}}}}
	listed := subject(map[string][]string{KeyRtSource: {"WEB-DL"}}, nil, nil)
	unlisted := subject(map[string][]string{KeyRtSource: {"HDTV"}}, nil, nil)
	missing := subject(map[string][]string{}, nil, nil)
	if r := Compare(listed, unlisted, profile); !r.Better {
		t.Fatalf("listed must beat unlisted: %+v", r)
	}
	if r := Compare(unlisted, missing, profile); r.Better || !r.Equal {
		t.Fatalf("unlisted and missing are equally worst: %+v", r)
	}
	if r := Compare(unlisted, missing, profile); r.Steps[0].Candidate != "HDTV" || r.Steps[0].CandidateListed {
		t.Fatalf("unlisted evidence keeps the raw value: %+v", r.Steps[0])
	}
}

func TestEmptyRankingIsSkipped(t *testing.T) {
	profile := Profile{Version: Version, Items: []Item{{Key: KeyRtCodec, Ranks: [][]string{}}}}
	r := Compare(subject(map[string][]string{KeyRtCodec: {"HEVC"}}, nil, nil), subject(nil, nil, nil), profile)
	if r.Better || !r.Equal || r.Steps[0].Result != StepSkipped {
		t.Fatalf("an empty ranking does not compare: %+v", r)
	}
}

func TestNoCrossNamespaceFallback(t *testing.T) {
	profile := Profile{Version: Version, Items: []Item{{Key: KeyAniBTResolution, Ranks: [][]string{{"1080p"}}}}}
	// The candidate has 1080p only under release.* and rt.*: anibt.* reads its
	// own key and finds nothing.
	candidate := subject(map[string][]string{KeyReleaseResolution: {"1080p"}, KeyRtResolution: {"1080p"}}, nil, nil)
	current := subject(map[string][]string{}, nil, nil)
	if r := Compare(candidate, current, profile); r.Better || !r.Equal {
		t.Fatalf("anibt.resolution must not fall back to another namespace: %+v", r)
	}
}

func TestRevisionComparesNumerically(t *testing.T) {
	profile := Profile{Version: Version, Items: []Item{{Key: KeyReleaseRevision, Ranks: [][]string{}}}}
	rev := func(v string) Subject {
		if v == "" {
			return subject(map[string][]string{}, nil, nil)
		}
		return subject(map[string][]string{KeyReleaseRevision: {v}}, nil, nil)
	}
	if r := Compare(rev("v3"), rev("v2"), profile); !r.Better {
		t.Fatal("v3 must beat v2")
	}
	if r := Compare(rev("v10"), rev("v9"), profile); !r.Better {
		t.Fatal("v10 must beat v9 (numeric, not lexical)")
	}
	if r := Compare(rev("v2"), rev(""), profile); !r.Better {
		t.Fatal("v2 must beat no revision")
	}
	if r := Compare(rev("v1"), rev(""), profile); r.Better || !r.Equal {
		t.Fatal("v1 equals no revision")
	}
	for value, want := range map[string]int64{"v2": 2, "V3": 3, "4": 4, "": 1, "v0": 1, "x": 1} {
		if got := RevisionNumber(value); got != want {
			t.Errorf("RevisionNumber(%q) = %d want %d", value, got, want)
		}
	}
}

func TestSubgroupScoreNoSourceIsLowest(t *testing.T) {
	profile := Profile{Version: Version, Items: []Item{{Key: KeySubgroupScore, Ranks: [][]string{}}}}
	negative := subject(nil, i64(-5), nil)
	none := subject(nil, nil, nil)
	if r := Compare(negative, none, profile); !r.Better {
		t.Fatalf("any score beats no score: %+v", r)
	}
	if r := Compare(subject(nil, i64(90), nil), subject(nil, i64(80), nil), profile); !r.Better {
		t.Fatal("higher score wins")
	}
	if r := Compare(none, none, profile); !r.Equal {
		t.Fatal("two missing scores are equal")
	}
	if got := profile.Without(KeySubgroupScore); len(got.Items) != 0 {
		t.Fatalf("Without must drop the item: %+v", got)
	}
}

func TestFrozenValuesRoundTrip(t *testing.T) {
	values := NewValues()
	values.SetJoined(KeyAniBTLanguage, "CHS+CHT+chs")
	values.Set(KeyReleaseLang, " ", "")
	values.Subgroup = &SubgroupSource{Provider: "mikan", GroupRef: "370"}
	parsed := ParseValues(values.JSON())
	if strings.Join(parsed.Values[KeyAniBTLanguage], ",") != "CHS,CHT" {
		t.Fatalf("values = %v", parsed.Values)
	}
	if _, has := parsed.Values[KeyReleaseLang]; has {
		t.Fatal("blank values must not be recorded")
	}
	if parsed.Subgroup == nil || parsed.Subgroup.GroupRef != "370" {
		t.Fatalf("subgroup = %+v", parsed.Subgroup)
	}
	if empty := ParseValues([]byte(`{}`)); len(empty.Values) != 0 || empty.Subgroup != nil {
		t.Fatalf("an unversioned document has no values: %+v", empty)
	}
}

// A language item matches the release's language set against the ranking: a
// release with several languages takes its best-ranked one, and a composite
// reltitle code counts as each language it names.
func TestLanguageItemsMatchTheReleaseLanguageSet(t *testing.T) {
	profile := Profile{Version: Version, Items: []Item{{Key: KeyRtSubLang, Ranks: [][]string{{"SC"}, {"TC"}, {"JPN"}}}}}
	cases := []struct {
		name               string
		candidate, current []string
		better, decided    bool
	}{
		{"JPSC hits SC and beats TC", []string{"JPSC"}, []string{"TC"}, true, true},
		{"JPSC ties with SC", []string{"JPSC"}, []string{"SC"}, false, false},
		{"JPTC hits TC and beats JPN", []string{"JPTC"}, []string{"JPN"}, true, true},
		{"SC+TC takes its best", []string{"SC", "TC"}, []string{"TC"}, true, true},
		{"unlisted loses to listed", []string{"ENG"}, []string{"JPN"}, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := Compare(subject(map[string][]string{KeyRtSubLang: tc.candidate}, nil, nil), subject(map[string][]string{KeyRtSubLang: tc.current}, nil, nil), profile)
			if result.Better != tc.better || (result.Decided != "") != tc.decided {
				t.Fatalf("result = %+v", result)
			}
		})
	}
	bilingual := Profile{Version: Version, Items: []Item{{Key: KeyRtSubLang, Ranks: [][]string{{"JPN", "SC"}, {"SC"}}}}}
	if result := Compare(subject(map[string][]string{KeyRtSubLang: {"JPSC"}}, nil, nil), subject(map[string][]string{KeyRtSubLang: {"SC"}}, nil, nil), bilingual); !result.Better {
		t.Fatalf("a JPN+SC entry must rank JPSC above SC: %+v", result)
	}
	if result := Compare(subject(map[string][]string{KeyRtSubLang: {"JPSC"}}, nil, nil), subject(map[string][]string{KeyRtSubLang: {"SC"}}, nil, nil), Profile{Version: Version, Items: []Item{{Key: KeyRtSubLang, Ranks: [][]string{{"JPSC"}, {"SC"}}}}}); !result.Better {
		t.Fatalf("an entry written as JPSC must still hit: %+v", result)
	}
}

// A ranked layer one side has no data for ties and the next item decides; a
// present but unlisted value still ranks lowest.
func TestLayerWithoutDataTies(t *testing.T) {
	profile := Profile{Version: Version, Items: []Item{
		{Key: KeyAniBTLanguage, Ranks: [][]string{{"CHS"}}},
		{Key: KeyRtSubLang, Ranks: [][]string{{"SC"}, {"TC"}}},
	}}
	anibt := subject(map[string][]string{KeyAniBTLanguage: {"CHS"}, KeyRtSubLang: {"TC"}}, nil, nil)
	mikan := subject(map[string][]string{KeyRtSubLang: {"SC"}}, nil, nil)
	if result := Compare(mikan, anibt, profile); !result.Better || result.Decided != KeyRtSubLang {
		t.Fatalf("the AniBT layer must tie for a release without tags: %+v", result)
	}
	unlisted := subject(map[string][]string{KeyAniBTLanguage: {"ENG"}, KeyRtSubLang: {"SC"}}, nil, nil)
	if result := Compare(unlisted, anibt, profile); result.Better || result.Decided != KeyAniBTLanguage {
		t.Fatalf("a present unlisted value must still rank lowest: %+v", result)
	}
}
