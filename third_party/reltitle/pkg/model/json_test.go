package model

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseResultMarshalJSON_FiltersEpisodeAttributes(t *testing.T) {
	res := ParseResult{
		RawTitle:    "[A] Sample - 01",
		ContentKind: "TV_EP",
		EPSemantics: &EPSemantics{
			Primary: &EPReference{
				Kind:    "EP",
				Axis:    "SE",
				Season:  "2",
				Episode: "1",
				Source:  "episode_final",
			},
		},
		Attributes: []Attribute{
			{Type: AttrTitle, Normalized: "Sample"},
			{Type: AttrSeason, Normalized: "2"},
			{Type: AttrEpisode, Normalized: "1"},
		},
	}

	data, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	text := string(data)
	if !strings.Contains(text, `"ep_semantics"`) {
		t.Fatalf("expected ep_semantics in json: %s", text)
	}
	if strings.Contains(text, `"epslot"`) {
		t.Fatalf("legacy epslot should not be serialized: %s", text)
	}
	if strings.Contains(text, `"type":"episode"`) || strings.Contains(text, `"type":"season"`) {
		t.Fatalf("legacy episode attrs should be filtered from serialized attributes: %s", text)
	}
	if !strings.Contains(text, `"type":"title"`) {
		t.Fatalf("non-ep attrs should remain serialized: %s", text)
	}
}

func TestCanonicalEPSemantics_NoLegacyFallbacks(t *testing.T) {
	res := &ParseResult{
		Attributes: []Attribute{{Type: AttrSeason, Normalized: "2"}, {Type: AttrEpisode, Normalized: "1"}},
	}
	if sem := res.CanonicalEPSemantics(); sem != nil {
		t.Fatalf("expected no fallback semantics, got=%+v", sem)
	}
	if got := res.CanonicalEpisode(); got != "" {
		t.Fatalf("canonical episode=%q want empty", got)
	}
	if got := res.CanonicalSeason(); got != "" {
		t.Fatalf("canonical season=%q want empty", got)
	}
}
