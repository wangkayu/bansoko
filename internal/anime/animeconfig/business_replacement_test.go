package animeconfig

import "testing"

func TestBusinessReplacementRejectsInvalidProfiles(t *testing.T) {
	for _, raw := range []string{
		`{"version":"profile-v0","dimensions":["resolution"],"ranks":{},"codec_enabled":false}`,
		`{"version":"profile-v2","items":[{"key":"file.codec","ranks":[]}]}`,
		`{"version":"profile-v2","items":[{"key":"rt.sub_lang","ranks":[]},{"key":"rt.sub_lang","ranks":[]}]}`,
		`{"version":"profile-v2","items":[],"hidden":true}`,
		`{"version":"profile-v2","items":[{"key":"rt.resolution","ranks":[["1080p","720p"]]}]}`,
	} {
		p := presetFixture(t)
		p.Replacement.QualityProfile = []byte(raw)
		if _, err := p.Freeze(); err == nil {
			t.Fatalf("accepted unsupported profile %s", raw)
		}
	}
}
