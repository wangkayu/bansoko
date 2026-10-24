package migrations

import (
	anime "bansoko/internal/anime/domain"
	"encoding/json"
	"fmt"
)

// Current accept fixtures use the complete internally generated code directory.
func defaultSections(raw string) string {
	if raw != "" {
		return raw
	}
	sections := map[string]json.RawMessage{}
	for _, key := range anime.EffectiveSectionOrder {
		value, err := anime.DefaultSectionValue(key)
		if err != nil {
			panic(err)
		}
		body, err := json.Marshal(value)
		if err != nil {
			panic(err)
		}
		body = projectDefaultSection(key, body)
		sections[key] = body
	}
	result, err := json.Marshal(sections)
	if err != nil {
		panic(err)
	}
	return string(result)
}
func projectDefaultSection(key string, raw json.RawMessage) json.RawMessage {
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		panic(err)
	}
	if key == anime.SectionNamingRules {
		if _, ok := body["preset"]; !ok {
			body["preset"] = nil
		}
	}
	if key == anime.SectionSubgroups {
		for _, field := range []string{"name_keywords", "provider_defaults"} {
			if _, ok := body[field]; !ok {
				body[field] = nil
			}
		}
	}
	result, err := json.Marshal(body)
	if err != nil {
		panic(fmt.Errorf("project %s: %w", key, err))
	}
	return result
}
