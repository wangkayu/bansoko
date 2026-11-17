package notification

import "testing"

func TestInputSourceTagsRemainFrozen(t *testing.T) {
	input := []byte(`{"source":{"title":{"state":"known","value":"Show 2160p"},"tag_evidence":{"title_tags":{"schema":"reltitle-attrs-v1","implementation":"frozen","tags":["resolution:720p"]},"anibt":{"tags":["tag:HEVC"]}}}}`)
	facts := ValueFacts(nil, InputSourceTags(input), func(s string) any { return s })
	if facts["rt.res"] != "720p" {
		t.Fatalf("frozen resolution = %#v", facts["rt.res"])
	}
	for _, legacy := range [][]byte{nil, []byte(`{}`), []byte(`{"source":{"title":{"state":"known","value":"Show 2160p"}}}`)} {
		if len(InputSourceTags(legacy)) != 0 {
			t.Fatal("historical tags were invented")
		}
	}
}
