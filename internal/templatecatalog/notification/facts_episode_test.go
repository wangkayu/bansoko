package notification

import (
	"encoding/json"
	"testing"
)

func TestEpisodeFactsProjectsSpanRange(t *testing.T) {
	raw := []byte(`{"identity":{"state":"known","value":{"state":"resolved","raw_episode":{"state":"known","value":"12"},"episode":{"state":"known","value":"23"},"episode_end":{"state":"known","value":"24"},"offset":0}}}`)
	parsed, ok := DecodeFacts(raw)
	if !ok {
		t.Fatal("failed to decode test identity")
	}
	facts := EpisodeFacts(parsed["identity"])
	if facts["ep.raw"] != "12" || facts["ep.number"] != "23" || facts["ep.end"] != "24" || facts["ep.display"] != "23-24" {
		t.Fatalf("range facts = %#v", facts)
	}
	if offset, ok := facts["ep.offset"].(json.Number); !ok || offset.String() != "0" {
		t.Fatalf("range offset = %#v", facts["ep.offset"])
	}

	singleRaw := []byte(`{"identity":{"state":"known","value":{"state":"resolved","raw_episode":{"state":"known","value":"7"},"episode":{"state":"known","value":"7"},"episode_end":{"state":"absent","value":null},"offset":0}}}`)
	singleParsed, _ := DecodeFacts(singleRaw)
	single := EpisodeFacts(singleParsed["identity"])
	if single["ep.number"] != "7" || single["ep.end"] != nil || single["ep.display"] != "7" {
		t.Fatalf("single facts = %#v", single)
	}
}

func TestMediaFileEpisodeFactsUsesFileIdentityAndLegacyReleaseFallback(t *testing.T) {
	release := []byte(`{"State":"resolved","RawEpisode":"13","Episode":"1","Offset":-12}`)
	combined := []byte(`{"State":"resolved","RawEpisode":"12","Episode":"23","EpisodeEnd":"24","Offset":2,"SelectedSource":"file_parser"}`)
	facts := MediaFileEpisodeFacts(combined, release)
	if facts["ep.raw"] != "12" || facts["ep.number"] != "23" || facts["ep.end"] != "24" || facts["ep.display"] != "23-24" || facts["ep.offset"] != 2 {
		t.Fatalf("combined file facts = %#v", facts)
	}

	manual := MediaFileEpisodeFacts([]byte(`{"State":"resolved","RawEpisode":"5","Episode":"5","EpisodeEnd":"12","Offset":0,"SelectedSource":"manual"}`), release)
	if manual["ep.number"] != "5" || manual["ep.end"] != "12" || manual["ep.display"] != "5-12" {
		t.Fatalf("manual range facts = %#v", manual)
	}

	fallback := MediaFileEpisodeFacts(nil, release)
	if fallback["ep.raw"] != "13" || fallback["ep.number"] != "1" || fallback["ep.end"] != nil || fallback["ep.display"] != "1" {
		t.Fatalf("legacy release fallback = %#v", fallback)
	}

	unresolved := MediaFileEpisodeFacts([]byte(`{"State":"unresolved","FailureCode":"episode_missing"}`), release)
	if unresolved["ep.number"] != nil || unresolved["ep.display"] != nil {
		t.Fatalf("present unresolved file must not borrow release identity: %#v", unresolved)
	}
}
