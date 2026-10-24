package application

import (
	protocol "bansoko/internal/acquisition/terminal/domain"
	anime "bansoko/internal/anime/domain"
	"encoding/json"
)

func defaultSectionDirectory() (json.RawMessage, error) {
	return defaultSectionDirectoryFrom(anime.DefaultSectionValue)
}

// The provider is local to each call so tests cannot alter concurrent accepts.
func defaultSectionDirectoryFrom(valueFor func(string) (any, error)) (json.RawMessage, error) {
	defaults := map[string]json.RawMessage{}
	for _, key := range anime.EffectiveSectionOrder {
		value, err := valueFor(key)
		if err != nil {
			return nil, err
		}
		raw, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		if err := anime.ValidateSectionValue(key, raw); err != nil {
			return nil, err
		}
		if key == anime.SectionNamingRules {
			projected, err := projectNamingSection(raw)
			if err != nil {
				return nil, err
			}
			raw = protocol.Canonical(projected)
		}
		if key == anime.SectionSubgroups {
			canonical, err := canonicalPolicy(raw)
			if err != nil {
				return nil, err
			}
			raw = []byte(canonical)
		}
		defaults[key] = raw
	}
	return json.Marshal(defaults)
}
