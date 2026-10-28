package domain

import (
	"testing"

	"bansoko/internal/episodeidentity"
)

// Coverage keys come in two shapes: the current seasonless tv:e<N> and the v1
// tv:s<S>:e<N> a sealed acquisition input keeps forever. Both validate; any
// other text does not.
func TestIdentityV1AcceptsBothCoverageKeyShapes(t *testing.T) {
	single := resolveFile(t, episodeidentity.NewValueEvidence(episodeidentity.EpisodeSourceFileParser, "file_name", "7"))
	for key, want := range map[string]bool{
		"tv:e7": true, "tv:s2:e7": true, "tv:s0:e7": true, "tv:s1:e7": true,
		"tv:e8": false, "tv:s2:e8": false, "tv:7": false, "tv:e7:x": false, "e7": false, "movie:0": false,
	} {
		value := IdentityValue(single)
		value.Object["coverage_keys"] = arr([]Value{str(key)})
		if err := Validate("IdentityV1", value); (err == nil) != want {
			t.Fatalf("IdentityV1 with %q: err=%v, want valid=%v", key, err, want)
		}
	}
}

func TestIdentityV2AcceptsOneCoverageKeyShapePerFile(t *testing.T) {
	double := resolveFile(t, episodeidentity.NewUnsupportedEvidence(episodeidentity.EpisodeSourceFileParser, "file_name", episodeidentity.ShapeRange, "07-08", ""))
	for name, tc := range map[string]struct {
		keys []string
		want bool
	}{
		"seasonless":         {[]string{"tv:e7", "tv:e8"}, true},
		"v1 one season":      {[]string{"tv:s2:e7", "tv:s2:e8"}, true},
		"v1 then seasonless": {[]string{"tv:s2:e7", "tv:e8"}, false},
		"seasonless then v1": {[]string{"tv:e7", "tv:s2:e8"}, false},
		"v1 mixed season":    {[]string{"tv:s2:e7", "tv:s3:e8"}, false},
		"seasonless gap":     {[]string{"tv:e7", "tv:e9"}, false},
	} {
		value := IdentityValue(double)
		keys := make([]Value, 0, len(tc.keys))
		for _, key := range tc.keys {
			keys = append(keys, str(key))
		}
		value.Object["coverage_keys"] = arr(keys)
		if err := Validate("IdentityV2", value); (err == nil) != tc.want {
			t.Fatalf("%s: err=%v, want valid=%v", name, err, tc.want)
		}
	}
}

// A file fact's identity coverage records the resolver version its frozen
// identity was resolved with: episode-identity-v1 for a sealed input,
// episode-identity-v2 for a current one.
func TestIdentityCoverageAcceptsBothResolverVersions(t *testing.T) {
	single := resolveFile(t, episodeidentity.NewValueEvidence(episodeidentity.EpisodeSourceFileParser, "file_name", "7"))
	identity := IdentityValue(single)
	keys := identity.Object["coverage_keys"]
	for schema, want := range map[string]bool{"episode-identity-v1": true, "episode-identity-v2": true, "episode-identity-v3": false} {
		value := obj(map[string]Value{
			"identity":        Known(identity).value(func(v Value) Value { return v }),
			"coverage_schema": str(schema),
			"coverage_keys":   keys,
		})
		if err := Validate("IdentityCoverageV1", value); (err == nil) != want {
			t.Fatalf("coverage_schema %q: err=%v, want valid=%v", schema, err, want)
		}
	}
}
