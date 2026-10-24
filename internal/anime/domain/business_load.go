package domain

import (
	"encoding/json"
	"fmt"
)

// LoadBusinessIntoDraft returns draft carrying one complete business snapshot
// built from preset (the current Global business preset) with the draft's own
// edits frozen into it, and the digest of that snapshot. It is the server form
// of the frontend's load + freeze, for clients (agents) that cannot compute a
// canonical business digest themselves. It reads nothing and writes nothing.
//
//   - subgroups: the draft's section, with the preset's name keywords and
//     provider defaults when the draft carries none, the preset's entries for
//     groups the draft does not name, and effective selections normalized;
//   - each rules section (parser, release filter, file filter, naming, file
//     effect): the draft's value is kept and frozen into the snapshot; an
//     absent section takes a copy of the preset value;
//   - tag_filters: the draft's value is kept; an absent section takes a copy
//     of globalTagFilters (the current Global tag_filters value);
//   - every other preset field (parser policy, retention, file-effect route,
//     program references, section provenance) is the preset's.
//
// Every per-anime section is one-time input: the
// result carries complete values, and saving, previewing or admitting it
// resolves exactly the configuration the draft shows. Later Global changes
// never reach it.
func LoadBusinessIntoDraft(draft AnimeDraft, preset BusinessSnapshot, globalTagFilters json.RawMessage) (AnimeDraft, error) {
	business, err := preset.Freeze()
	if err != nil {
		return AnimeDraft{}, fmt.Errorf("business preset: %w", err)
	}
	var base SubgroupsSection
	if err := json.Unmarshal(business.Sections[SectionSubgroups], &base); err != nil {
		return AnimeDraft{}, fmt.Errorf("business preset subgroups: %w", err)
	}
	subgroups := draft.Subgroups
	if subgroups.NameKeywords == nil {
		subgroups.NameKeywords = base.NameKeywords
	}
	if subgroups.ProviderDefaults == nil {
		subgroups.ProviderDefaults = base.ProviderDefaults
	}
	entries := make([]SubgroupScoreEntry, 0, len(base.Entries)+len(subgroups.Entries))
	for _, entry := range base.Entries {
		if !draftNamesGroup(subgroups.Entries, entry) {
			entries = append(entries, entry)
		}
	}
	subgroups.Entries = append(entries, subgroups.Entries...)
	normalized, err := SubgroupsSectionWithEffectiveSelections(subgroups)
	if err != nil {
		return AnimeDraft{}, fmt.Errorf("subgroups: %w", err)
	}
	raw, err := json.Marshal(normalized)
	if err != nil {
		return AnimeDraft{}, err
	}
	if err := ValidateSectionValue(SectionSubgroups, raw); err != nil {
		return AnimeDraft{}, fmt.Errorf("subgroups: %w", err)
	}
	business.Sections[SectionSubgroups] = raw
	draft.Subgroups = normalized

	rules := []struct {
		field   *json.RawMessage
		section string
		name    string
	}{
		{&draft.Rules.Sections.Parser, SectionParser, "parser"},
		{&draft.Rules.Sections.ReleaseFilter, SectionReleaseRules, "release_filter"},
		{&draft.Rules.Sections.FileFilter, SectionFileRules, "file_filter"},
		{&draft.Rules.Sections.Naming, SectionNamingRules, "naming"},
		{&draft.Rules.Sections.FileEffect, SectionFileEffect, "file_effect"},
	}
	for _, rule := range rules {
		// An absent section takes a copy of the preset value.
		value := business.Sections[rule.section]
		if !SectionAbsent(*rule.field) {
			decoded, err := decodeOverride(*rule.field)
			if err != nil {
				return AnimeDraft{}, fmt.Errorf("rules.%s: %w", rule.name, err)
			}
			value = decoded.Value
		}
		business.Sections[rule.section] = value
		wrapped, err := SectionEnvelope(value)
		if err != nil {
			return AnimeDraft{}, err
		}
		*rule.field = wrapped
	}
	if SectionAbsent(draft.Rules.Sections.TagFilters) {
		if _, err := decodeTagFilterRules(globalTagFilters); err != nil {
			return AnimeDraft{}, fmt.Errorf("global tag_filters: %w", err)
		}
		wrapped, err := SectionEnvelope(globalTagFilters)
		if err != nil {
			return AnimeDraft{}, err
		}
		draft.Rules.Sections.TagFilters = wrapped
	} else if _, _, err := StoredTagFiltersOverride(draft.Rules.Sections.TagFilters); err != nil {
		return AnimeDraft{}, fmt.Errorf("rules.tag_filters: %w", err)
	}

	frozen, err := business.Freeze()
	if err != nil {
		return AnimeDraft{}, fmt.Errorf("business snapshot: %w", err)
	}
	digest, err := frozen.Digest()
	if err != nil {
		return AnimeDraft{}, err
	}
	draft.Business = &frozen
	draft.BusinessDigest = &digest
	return draft, nil
}

// SectionEnvelope wraps one complete section value in the wire envelope
// {"mode":"replace","value":...} a draft or owner write carries.
func SectionEnvelope(value json.RawMessage) (json.RawMessage, error) {
	return json.Marshal(struct {
		Mode  string          `json:"mode"`
		Value json.RawMessage `json:"value"`
	}{"replace", value})
}

func draftNamesGroup(entries []SubgroupScoreEntry, candidate SubgroupScoreEntry) bool {
	for _, entry := range entries {
		if entry.Provider == candidate.Provider && entry.GroupRef == candidate.GroupRef {
			return true
		}
	}
	return false
}
