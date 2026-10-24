package domain

import (
	"testing"

	"bansoko/internal/episodeidentity"
)

func resolveFile(t *testing.T, evidence episodeidentity.EpisodeEvidence) episodeidentity.EpisodeIdentity {
	t.Helper()
	return episodeidentity.NewResolver().Resolve(episodeidentity.EpisodeIdentityInput{MediaKind: "tv", Season: 2, SeasonBound: true, FileEvidence: evidence})
}

// Resolver output validates against its schema: a single episode and a movie
// as IdentityV1, a multi-episode file as IdentityV2.
func TestResolvedIdentitiesValidate(t *testing.T) {
	single := resolveFile(t, episodeidentity.NewValueEvidence(episodeidentity.EpisodeSourceFileParser, "file_name", "7"))
	double := resolveFile(t, episodeidentity.NewUnsupportedEvidence(episodeidentity.EpisodeSourceFileParser, "file_name", episodeidentity.ShapeRange, "07-08", ""))
	quad := resolveFile(t, episodeidentity.NewUnsupportedEvidence(episodeidentity.EpisodeSourceFileParser, "file_name", episodeidentity.ShapeList, "[01][02][03][04]", ""))
	movie := episodeidentity.NewResolver().Resolve(episodeidentity.EpisodeIdentityInput{MediaKind: "movie"})
	for name, tc := range map[string]struct {
		identity episodeidentity.EpisodeIdentity
		schema   string
	}{
		"single": {single, "IdentityV1"}, "double": {double, "IdentityV2"}, "quad": {quad, "IdentityV2"}, "movie": {movie, "IdentityV1"},
	} {
		if got := IdentitySchema(tc.identity); got != tc.schema {
			t.Fatalf("%s: schema %s, want %s", name, got, tc.schema)
		}
		if err := ValidateResolvedIdentity(tc.identity); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		// IdentityV2 also admits everything V1 does.
		if err := Validate("IdentityV2", withEpisodeEnd(IdentityValue(tc.identity))); err != nil {
			t.Fatalf("%s as IdentityV2: %v", name, err)
		}
	}
	// IdentityV1 stays single-key: a multi-episode identity is not a V1 identity.
	v1 := IdentityValue(double)
	delete(v1.Object, "episode_end")
	if err := Validate("IdentityV1", v1); err == nil {
		t.Fatal("IdentityV1 must refuse a range identity")
	}
	// Unresolved identities are not checked.
	missing := resolveFile(t, episodeidentity.NewMissingEvidence(episodeidentity.EpisodeSourceFileParser, "file_name"))
	if err := ValidateResolvedIdentity(missing); err != nil {
		t.Fatal(err)
	}
}

func withEpisodeEnd(v Value) Value {
	if _, ok := v.Object["episode_end"]; !ok {
		v.Object["episode_end"] = Absent[string]().value(str)
	}
	return v
}

// IdentityV2 refuses a multi-episode identity whose keys are not the
// consecutive members from episode to episode_end in one season, more than
// MaxEpisodesPerFile members, or one taken from a release title.
func TestIdentityV2RefusesInconsistentRanges(t *testing.T) {
	double := resolveFile(t, episodeidentity.NewUnsupportedEvidence(episodeidentity.EpisodeSourceFileParser, "file_name", episodeidentity.ShapeRange, "07-08", ""))
	cases := map[string]func(v Value){
		"gap":            func(v Value) { v.Object["coverage_keys"] = arr([]Value{str("tv:s2:e7"), str("tv:s2:e9")}) },
		"mixed season":   func(v Value) { v.Object["coverage_keys"] = arr([]Value{str("tv:s2:e7"), str("tv:s3:e8")}) },
		"end mismatch":   func(v Value) { v.Object["episode_end"] = Known("9").value(str) },
		"end absent":     func(v Value) { v.Object["episode_end"] = Absent[string]().value(str) },
		"release source": func(v Value) { v.Object["selected_source"] = Known("release_parser").value(str) },
		"too many": func(v Value) {
			v.Object["episode_end"] = Known("11").value(str)
			keys := []Value{}
			for _, key := range []string{"tv:s2:e7", "tv:s2:e8", "tv:s2:e9", "tv:s2:e10", "tv:s2:e11"} {
				keys = append(keys, str(key))
			}
			v.Object["coverage_keys"] = arr(keys)
		},
	}
	for name, mutate := range cases {
		value := IdentityValue(double)
		mutate(value)
		if err := Validate("IdentityV2", value); err == nil {
			t.Fatalf("%s: IdentityV2 accepted %s", name, Canonical(value))
		}
	}
	// A single identity must not carry an end.
	single := IdentityValue(resolveFile(t, episodeidentity.NewValueEvidence(episodeidentity.EpisodeSourceFileParser, "file_name", "7")))
	single.Object["episode_end"] = Known("8").value(str)
	if err := Validate("IdentityV2", single); err == nil {
		t.Fatal("a single identity with an end must be refused")
	}
}
