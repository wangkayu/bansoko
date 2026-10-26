package animeconfig

import "testing"

func TestBusinessReplacementRejectsInvalidProfiles(t *testing.T) {
	for _, raw := range []string{
		`{"version":"profile-v0","dimensions":["resolution"],"ranks":{},"codec_enabled":false}`,
		`{"version":"profile-v1","items":[{"key":"file.codec","ranks":[]}]}`,
		`{"version":"profile-v1","items":[{"key":"release.lang","ranks":[]},{"key":"release.lang","ranks":[]}]}`,
		`{"version":"profile-v1","items":[],"hidden":true}`,
		`{"version":"profile-v1","items":[{"key":"release.lang","ranks":[["chs","cht"]]}]}`,
	} {
		p := presetFixture(t)
		p.Replacement.QualityProfile = []byte(raw)
		if _, err := p.Freeze(); err == nil {
			t.Fatalf("accepted unsupported profile %s", raw)
		}
	}
}
