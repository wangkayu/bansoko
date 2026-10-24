package application

import (
	"encoding/json"

	protocol "bansoko/internal/acquisition/terminal/domain"
	anime "bansoko/internal/anime/domain"
)

// NamingSectionV1 owns all seven keys. Business JSON has omitempty on preset;
// this explicit projection preserves absent as null without filling required
// templates or discarding unknown source fields.
func projectNamingSection(raw []byte) (protocol.Value, error) {
	value, err := protocol.ParseStrict(raw)
	if err != nil {
		return protocol.Value{}, err
	}
	if value.Kind != protocol.KindObject {
		return protocol.Value{}, materialError("naming", nil)
	}
	allowed := map[string]bool{"language": true, "movie": true, "unknown": true, "tv": true, "preset": true, "subtitle_suffix_template": true}
	for key := range value.Object {
		if !allowed[key] {
			return protocol.Value{}, materialError("naming", nil)
		}
	}
	var rules anime.NamingRules
	if err := json.Unmarshal(raw, &rules); err != nil {
		return protocol.Value{}, err
	}
	for _, key := range []string{"language", "movie", "unknown", "tv", "subtitle_suffix_template"} {
		if _, ok := value.Object[key]; !ok {
			return protocol.Value{}, materialError("naming", nil)
		}
	}
	var preset any
	if rules.Preset != nil {
		preset = *rules.Preset
	}
	projected, err := valueOf(map[string]any{"language": rules.Language, "movie": rules.Movie, "unknown": rules.Unknown, "tv": rules.Tv, "preset": preset, "subtitle_suffix_template": rules.SubtitleSuffixTemplate})
	if err == nil {
		err = protocol.Validate("NamingSectionV1", projected)
	}
	return projected, err
}
