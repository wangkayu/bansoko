package qualityprofile

import (
	"encoding/json"
	"strings"
	"testing"
)

func i64(v int64) *int64 { return &v }

func subject(values map[string][]string, score, _ *int64) Subject {
	return Subject{Values: values, Score: score}
}

func TestDefaultProfileIsValidAndBuiltInOnly(t *testing.T) {
	profile, err := Parse(DefaultJSON())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{KeySubgroupScore, KeyAniBTLanguage, KeyRtSubLang, KeyAniBTResolution, KeyRtResolution, KeyRtRevision}
	if strings.Join(profile.Keys(), ",") != strings.Join(want, ",") {
		t.Fatalf("default items = %v", profile.Keys())
	}
}

func TestValidateRejections(t *testing.T) {
	cases := map[string]string{
		"unknown version":      `{"version":"profile-v0","items":[]}`,
		"unknown key":          `{"version":"profile-v2","items":[{"key":"media.resolution","ranks":[]}]}`,
		"duplicate key":        `{"version":"profile-v2","items":[{"key":"rt.sub_lang","ranks":[]},{"key":"rt.sub_lang","ranks":[]}]}`,
		"missing ranks":        `{"version":"profile-v2","items":[{"key":"rt.sub_lang"}]}`,
		"score with values":    `{"version":"profile-v2","items":[{"key":"subgroup.score","ranks":[["x"]]}]}`,
		"revision with values": `{"version":"profile-v2","items":[{"key":"rt.revision","ranks":[["v2"]]}]}`,
		"combo on single item": `{"version":"profile-v2","items":[{"key":"rt.resolution","ranks":[["1080p","720p"]]}]}`,
		"duplicate set":        `{"version":"profile-v2","items":[{"key":"anibt.language","ranks":[["CHS","CHT"],["cht","chs"]]}]}`,
		"duplicate single":     `{"version":"profile-v2","items":[{"key":"rt.resolution","ranks":[["1080p"],["1080P"]]}]}`,
		"repeat inside entry":  `{"version":"profile-v2","items":[{"key":"rt.sub_lang","ranks":[["SC","sc"]]}]}`,
		"blank value":          `{"version":"profile-v2","items":[{"key":"rt.sub_lang","ranks":[[" "]]}]}`,
		"untrimmed value":      `{"version":"profile-v2","items":[{"key":"rt.sub_lang","ranks":[[" sc"]]}]}`,
		"joined value":         `{"version":"profile-v2","items":[{"key":"anibt.language","ranks":[["CHS+CHT"]]}]}`,
		"empty entry":          `{"version":"profile-v2","items":[{"key":"anibt.language","ranks":[[]]}]}`,
		"unknown field":        `{"version":"profile-v2","items":[],"cutoff":{}}`,
		"null items":           `{"version":"profile-v2","items":null}`,
	}
	for name, raw := range cases {
		if _, err := Parse(json.RawMessage(raw)); err == nil {
			t.Errorf("%s: accepted %s", name, raw)
		}
	}
	ok := `{"version":"profile-v2","items":[{"key":"anibt.language","ranks":[["CHS","CHT","JPN"],["CHS","CHT"],["CHS"]]},{"key":"rt.codec","ranks":[]}]}`
	if _, err := Parse(json.RawMessage(ok)); err != nil {
		t.Fatalf("valid profile rejected: %v", err)
	}
}

func TestLexicographicFirstDifferenceDecides(t *testing.T) {
	profile := Profile{Version: Version, Items: []Item{
		{Key: KeyRtSubLang, Ranks: [][]string{{"sc"}, {"tc"}}},
		{Key: KeyRtResolution, Ranks: [][]string{{"2160p"}, {"1080p"}}},
	}}
	// Candidate has the better language but the worse resolution: language decides.
	candidate := subject(map[string][]string{KeyRtSubLang: {"sc"}, KeyRtResolution: {"1080p"}}, nil, i64(1))
	current := subject(map[string][]string{KeyRtSubLang: {"tc"}, KeyRtResolution: {"2160p"}}, nil, i64(9))
	result := Compare(candidate, current, profile)
	if !result.Better || result.Decided != KeyRtSubLang || len(result.Steps) != 1 {
		t.Fatalf("result = %+v", result)
	}
	// Reversed: the candidate loses on the first item.
	if back := Compare(current, candidate, profile); back.Better || back.Equal || back.Decided != KeyRtSubLang {
		t.Fatalf("reverse = %+v", back)
	}
}

func TestEqualItemsKeepExisting(t *testing.T) {
	profile := Profile{Version: Version, Items: []Item{{Key: KeyRtResolution, Ranks: [][]string{{"1080p"}}}}}
	values := map[string][]string{KeyRtResolution: {"1080p"}}
	result := Compare(Subject{Values: values}, Subject{Values: values}, profile)
	if result.Better || !result.Equal || result.Decided != "" || len(result.Steps) != 1 {
		t.Fatalf("equal items must keep existing without a hidden step: %+v", result)
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
	candidate := subject(map[string][]string{KeyRtResolution: {"1080p"}}, nil, nil)
	current := subject(map[string][]string{}, nil, nil)
	if r := Compare(candidate, current, profile); r.Better || !r.Equal {
		t.Fatalf("anibt.resolution must not fall back to another namespace: %+v", r)
	}
}

func TestRevisionComparesNumerically(t *testing.T) {
	profile := Profile{Version: Version, Items: []Item{{Key: KeyRtRevision, Ranks: [][]string{}}}}
	rev := func(v string) Subject { return Subject{Values: map[string][]string{KeyRtRevision: {v}}} }
	for _, pair := range [][2]string{{"v3", "v2"}, {"v10", "v9"}, {"10", "9"}} {
		if got := Compare(rev(pair[0]), rev(pair[1]), profile); !got.Better {
			t.Fatalf("numeric ordering failed: %+v", got)
		}
	}
	for _, missing := range []string{"", "v0", "x", "-1", "1.5", "v2+v3", "9999999999999999999999999"} {
		for _, pair := range [][2]string{{"v2", missing}, {missing, "v2"}} {
			if got := Compare(rev(pair[0]), rev(pair[1]), profile); got.Better || !got.Equal {
				t.Fatalf("missing revision must tie: %v %+v", pair, got)
			}
		}
		if _, ok := RevisionNumber(missing); ok {
			t.Fatalf("accepted invalid revision %q", missing)
		}
	}
	for value, want := range map[string]int64{"v2": 2, "V3": 3, "4": 4} {
		if got, ok := RevisionNumber(value); !ok || got != want {
			t.Fatalf("revision %q = %d,%v", value, got, ok)
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
	values.SourceDigest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	values.SetJoined(KeyAniBTLanguage, "CHS+CHT+chs")
	values.Set(KeyRtSubLang, " ", "")
	values.Subgroup = &SubgroupSource{Provider: "mikan", GroupRef: "370"}
	parsed, err := ParseValues(values.JSON())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(parsed.Values[KeyAniBTLanguage], ",") != "CHS,CHT" {
		t.Fatalf("values = %v", parsed.Values)
	}
	if _, has := parsed.Values[KeyRtSubLang]; has {
		t.Fatal("blank values must not be recorded")
	}
	if parsed.Subgroup == nil || parsed.Subgroup.GroupRef != "370" {
		t.Fatalf("subgroup = %+v", parsed.Subgroup)
	}
	for _, raw := range []string{`{}`, `{"version":"compare-values-v1","values":{}}`, `not-json`} {
		if _, err := ParseValues([]byte(raw)); err == nil {
			t.Fatalf("stale/corrupt cache accepted: %s", raw)
		}
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

// rt.sub_flag is a registered multi-valued item outside the default order: a
// set entry hits when the release's flags contain it, the best-ranked hit
// wins, and a release without flags ties.
func TestSubFlagItemIsMultiValuedAndTiesWithoutData(t *testing.T) {
	def, ok := Lookup(KeyRtSubFlag)
	if !ok || def.Namespace != NamespaceRt || def.Kind != KindMulti {
		t.Fatalf("rt.sub_flag registration = %+v, %v", def, ok)
	}
	if profile, err := Parse(DefaultJSON()); err != nil || strings.Contains(strings.Join(profile.Keys(), ","), KeyRtSubFlag) {
		t.Fatalf("rt.sub_flag must not be in the default order: %v %v", profile.Keys(), err)
	}
	profile := Profile{Version: Version, Items: []Item{
		{Key: KeyRtSubFlag, Ranks: [][]string{{"soft", "ex"}, {"soft"}, {"hard"}}},
		{Key: KeyRtResolution, Ranks: [][]string{{"1080p"}, {"720p"}}},
	}}
	if _, err := Parse(mustJSON(t, profile)); err != nil {
		t.Fatalf("profile with rt.sub_flag rejected: %v", err)
	}
	both := subject(map[string][]string{KeyRtSubFlag: {"soft", "ex"}}, nil, nil)
	soft := subject(map[string][]string{KeyRtSubFlag: {"soft"}}, nil, nil)
	hard := subject(map[string][]string{KeyRtSubFlag: {"hard"}}, nil, nil)
	if result := Compare(both, soft, profile); !result.Better || result.Decided != KeyRtSubFlag {
		t.Fatalf("soft+ex must beat soft: %+v", result)
	}
	if result := Compare(soft, hard, profile); !result.Better || result.Decided != KeyRtSubFlag {
		t.Fatalf("soft must beat hard: %+v", result)
	}
	none := subject(map[string][]string{KeyRtResolution: {"720p"}}, nil, nil)
	better := subject(map[string][]string{KeyRtResolution: {"1080p"}}, nil, nil)
	if result := Compare(better, none, profile); !result.Better || result.Decided != KeyRtResolution {
		t.Fatalf("a release without flags must tie and the next item decide: %+v", result)
	}
}

func mustJSON(t *testing.T, profile Profile) []byte {
	t.Helper()
	raw, err := json.Marshal(profile)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// rt.content_rating is a registered single-value ranked item outside the
// default order: the user orders the values, an unlisted value ranks worst,
// and a side without a rating ties on it.
func TestContentRatingItemRanksUserOrderedValues(t *testing.T) {
	def, ok := Lookup(KeyRtContentRating)
	if !ok || def.Namespace != NamespaceRt || def.Kind != KindSingle {
		t.Fatalf("rt.content_rating registration = %+v, %v", def, ok)
	}
	if defaultProfile := Default(); strings.Contains(strings.Join(defaultProfile.Keys(), ","), KeyRtContentRating) {
		t.Fatalf("rt.content_rating must not be in the default order: %v", defaultProfile.Keys())
	}
	bad := Profile{Version: Version, Items: []Item{{Key: KeyRtContentRating, Ranks: [][]string{{"UNCENSORED", "CENSORED"}}}}}
	if _, err := Parse(mustJSON(t, bad)); err == nil {
		t.Fatal("rt.content_rating must reject combinations")
	}
	profile := Profile{Version: Version, Items: []Item{{Key: KeyRtContentRating, Ranks: [][]string{{"UNCENSORED"}, {"AGE_RESTRICTED"}, {"CENSORED"}}}}}
	order := []string{"UNCENSORED", "AGE_RESTRICTED", "CENSORED"}
	for i := 0; i+1 < len(order); i++ {
		better := subject(map[string][]string{KeyRtContentRating: {order[i]}}, nil, nil)
		worse := subject(map[string][]string{KeyRtContentRating: {order[i+1]}}, nil, nil)
		if result := Compare(better, worse, profile); !result.Better || result.Decided != KeyRtContentRating {
			t.Fatalf("%s must beat %s: %+v", order[i], order[i+1], result)
		}
		if result := Compare(worse, better, profile); result.Better {
			t.Fatalf("%s must not beat %s: %+v", order[i+1], order[i], result)
		}
	}
	unlisted := subject(map[string][]string{KeyRtContentRating: {"R18"}}, nil, nil)
	none := subject(map[string][]string{}, nil, nil)
	listed := subject(map[string][]string{KeyRtContentRating: {"UNCENSORED"}}, nil, nil)
	if result := Compare(listed, unlisted, profile); !result.Better || result.Decided != KeyRtContentRating {
		t.Fatalf("an unlisted rating must rank below a listed one: %+v", result)
	}
	if result := Compare(none, none, profile); !result.Equal {
		t.Fatalf("two sides without a rating must tie: %+v", result)
	}
}

func TestDefaultRtSubLangRanksCombinedChineseJapanese(t *testing.T) {
	profile := Default()
	order := [][]string{{"JPSC", "JPTC"}, {"JPSC"}, {"SC", "TC"}, {"SC"}}
	for i := 0; i+1 < len(order); i++ {
		better := subject(map[string][]string{KeyRtSubLang: order[i]}, nil, nil)
		worse := subject(map[string][]string{KeyRtSubLang: order[i+1]}, nil, nil)
		if result := Compare(better, worse, profile); !result.Better {
			t.Fatalf("%v must beat %v: %+v", order[i], order[i+1], result)
		}
		if result := Compare(worse, better, profile); result.Better {
			t.Fatalf("%v must not beat %v: %+v", order[i+1], order[i], result)
		}
	}
}
