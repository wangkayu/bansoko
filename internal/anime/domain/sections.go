package domain

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	"bansoko/internal/postprocessing/naming"
	providerdomain "bansoko/internal/provider/domain"
)

// DB section names owned by the subgroups/rules config owners.
const (
	SectionSubgroups     = "subgroups"
	SectionParser        = "parser"
	SectionReleaseRules  = "release_rules"
	SectionFileRules     = "file_rules"
	SectionSubtitleRules = "subtitle_rules"
	SectionNamingRules   = "naming_rules"
	SectionFileEffect    = "file_effect"
)

// PolicyDefaultSections are the global_config_sections rows owned by the
// retention / parser / file-effect policy services. They are not frozen rules
// sections: they never appear in EffectiveSectionOrder, the generic
// /config/global-sections API or the business snapshot's `sections` map.
const (
	SectionRetentionPolicy  = "retention"
	SectionParserPolicy     = "parser_policy"
	SectionFileEffectPolicy = "file_effect_policy"
)

// PolicyDefaultSectionNames is the closed set of the three policy-default
// section names. Generic Global readers must exclude them.
var PolicyDefaultSectionNames = []string{SectionRetentionPolicy, SectionParserPolicy, SectionFileEffectPolicy}

// SectionSchemaVersions pin the typed shape of each stored section value.
var SectionSchemaVersions = map[string]string{
	SectionSubgroups:    "subgroups-v1",
	SectionParser:       "parser-section-v1",
	SectionReleaseRules: "release-rules-v1",
	SectionFileRules:    "file-rules-v1",
	SectionNamingRules:  "naming-rules-v1",
	SectionFileEffect:   "file-effect-v1",
	SectionTagFilters:   TagFiltersSchemaVersion,

	SectionDownloadCompletion: DownloadCompletionSchemaVersion,

	SectionDownloadTimeouts: DownloadTimeoutsSchemaVersion,

	SectionMetadataRefresh: MetadataRefreshSchemaVersion,
}

// ComponentVersions identifies the versioned pure components. Preview and
// execution must call the same versions.
const (
	ParserVersion    = "parser-v1"
	EvaluatorVersion = "evaluator-v1"
	SafetyVersion    = "path-safety-v1"
)

// OverrideValue is one per-anime section value: the complete whole-section
// value a draft or instance stores for itself. Every per-anime section is
// one-time input: it is copied from Global when the
// draft is created and never follows a later Global change, so there is no
// inherit state and an empty value is a missing section.
type OverrideValue struct {
	Value json.RawMessage
}

// GlobalSection is the stored typed Global section row.
type GlobalSection struct {
	SchemaVersion string
	Value         json.RawMessage
	Version       int64
}

func stringSlicePtr(value []string) *[]string { return &value }

// DefaultSectionValue returns the code-owned default for one section: the
// value a Global section has before it is first saved.
func DefaultSectionValue(section string) (any, error) {
	switch section {
	case SectionSubgroups:
		return SubgroupsSection{Entries: []SubgroupScoreEntry{}, MaxRss: 3}, nil
	case SectionParser:
		return ParserRules{EpisodePatterns: acquisitiondomain.DefaultEpisodePatterns}, nil
	case SectionReleaseRules:
		return ReleaseFilterRules{}, nil
	case SectionFileRules:
		// File rules are empty by default: the product already limits what is
		// processed to first-level videos and subtitles (internal/filekinds).
		return FileFilterRules{}, nil
	case SectionNamingRules:
		return NamingRulesFromDomain(naming.DefaultNamingRules()), nil
	case SectionFileEffect:
		return FileEffectRules{Operation: FileEffectRulesOperationCopy, CollisionPolicy: CollisionPolicyFail}, nil
	case SectionTagFilters:
		return DefaultTagFilterRules(), nil
	case SectionDownloadCompletion:
		return DefaultDownloadCompletionRules(), nil
	case SectionDownloadTimeouts:
		return DefaultDownloadTimeouts(), nil
	case SectionMetadataRefresh:
		return DefaultMetadataRefreshRules(), nil
	default:
		return nil, fmt.Errorf("unknown section %q", section)
	}
}

func decodeStrictJSON(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("JSON contains more than one value")
		}
		return err
	}
	return nil
}

// decodeOverride decodes one generated {mode: replace, value} section
// envelope. Values are complete sections: explicit empty lists, empty strings,
// false, and 0 are valid values. There is no inherit mode.
func decodeOverride(raw json.RawMessage) (OverrideValue, error) {
	var probe struct {
		Mode  string          `json:"mode"`
		Value json.RawMessage `json:"value"`
	}
	if err := decodeStrictJSON(raw, &probe); err != nil {
		return OverrideValue{}, fmt.Errorf("decode override: %w", err)
	}
	if probe.Mode != "replace" {
		return OverrideValue{}, fmt.Errorf("section mode must be replace with a complete value")
	}
	if len(probe.Value) == 0 || string(probe.Value) == "null" {
		return OverrideValue{}, fmt.Errorf("section requires a complete value")
	}
	return OverrideValue{Value: probe.Value}, nil
}

func marshalAndDecode(union interface{ MarshalJSON() ([]byte, error) }) (OverrideValue, error) {
	raw, err := union.MarshalJSON()
	if err != nil {
		return OverrideValue{}, err
	}
	return decodeOverride(raw)
}

// SectionValidationError carries a typed section problem whose code is owned by
// the section validator (for example a naming template's NAMING_VARS_UNKNOWN)
// through draft resolution without relabelling it as a generic override error.
type SectionValidationError struct {
	Code    string
	Field   string
	Message string
}

func (e *SectionValidationError) Error() string { return e.Message }

// RulesSectionOverrides decodes the six rules section overrides from a draft
// or owner value into their DB section names.
func RulesSectionOverrides(sections RulesSections) (map[string]OverrideValue, []FieldViolation) {
	return RulesSectionOverridesWithVars(sections, nil)
}

// RulesPreviewSections is the serialized unsaved owner rules patch carried by
// the RSSListView owner_draft read mode: the live request path, not the
// retired flat rules preview route.
type RulesPreviewSections []byte

func RulesPreviewSectionOverrides(sections RulesPreviewSections) (map[string]OverrideValue, []FieldViolation) {
	var value RulesSections
	if err := json.Unmarshal(sections, &value); err != nil {
		return nil, []FieldViolation{{Field: "rules", Code: "OVERRIDE_MODE_INVALID", Message: "rules sections must be a valid object"}}
	}
	return RulesSectionOverrides(value)
}

// RulesSectionOverridesWithVars is the save-time form: naming rules are
// validated against the injected Vars snapshot, so a template that reads a
// variable the deployment has not loaded is refused before it is stored.
func RulesSectionOverridesWithVars(sections RulesSections, vars map[string]map[string]string) (map[string]OverrideValue, []FieldViolation) {
	result := map[string]OverrideValue{}
	var violations []FieldViolation
	decode := func(section string, union interface{ MarshalJSON() ([]byte, error) }) {
		override, err := marshalAndDecode(union)
		if err != nil {
			violations = append(violations, FieldViolation{Field: "rules." + section, Code: "OVERRIDE_MODE_INVALID", Message: "section must be a complete replace value"})
			return
		}
		err = ValidateSectionValueWithVars(section, override.Value, vars)
		if err == nil && section == SectionNamingRules {
			err = ValidateNamingSectionForSave(override.Value, vars)
		}
		if err != nil {
			code := "OVERRIDE_MODE_INVALID"
			var namingErr *naming.Error
			var fileRuleErr *FileRuleError
			if section == SectionNamingRules && errors.As(err, &namingErr) {
				code = namingErr.Code
			} else if section == SectionFileRules && errors.As(err, &fileRuleErr) {
				code = fileRuleErr.Code
			}
			violations = append(violations, FieldViolation{Field: "rules." + section, Code: code, Message: err.Error()})
			return
		}
		if section == SectionTagFilters {
			override.Value = NormalizeTagFilterRulesJSON(override.Value)
		}
		result[section] = override
	}
	decode(SectionParser, sections.Parser)
	decode(SectionReleaseRules, sections.ReleaseFilter)
	decode(SectionFileRules, sections.FileFilter)
	decode(SectionNamingRules, sections.Naming)
	decode(SectionFileEffect, sections.FileEffect)
	// tag_filters is optional on the wire: an absent section produces no
	// entry, so an owner save from a client that does not know the section
	// leaves the stored override untouched.
	if len(sections.TagFilters) > 0 {
		decode(SectionTagFilters, sections.TagFilters)
	}
	return result, violations
}

// SubgroupsSectionOverride stores the complete generated subgroup section as a
// whole-section replacement. There is no source-editing draft shape.
//
// The stored value always carries the computed effective_selected for every
// entry: the section is normalized through the group-selection authority before
// it is persisted, so a saved revision cannot record a selection the runtime
// would not make.
func SubgroupsSectionOverride(section SubgroupsSection) (OverrideValue, []FieldViolation) {
	normalized, err := SubgroupsSectionWithEffectiveSelections(section)
	if err != nil {
		return OverrideValue{}, []FieldViolation{{Field: "subgroups", Code: "OVERRIDE_MODE_INVALID", Message: err.Error()}}
	}
	raw, err := json.Marshal(normalized)
	if err != nil {
		return OverrideValue{}, []FieldViolation{{Field: "subgroups", Code: "OVERRIDE_MODE_INVALID", Message: "subgroups must be a complete section"}}
	}
	if err := ValidateSectionValue(SectionSubgroups, raw); err != nil {
		return OverrideValue{}, []FieldViolation{{Field: "subgroups", Code: "OVERRIDE_MODE_INVALID", Message: err.Error()}}
	}
	return OverrideValue{Value: raw}, nil
}

// ValidateSubgroupsSection validates the complete provider-group selection
// section. Source locators and provenance are deliberately outside this owner.
func ValidateSubgroupsSection(section SubgroupsSection) []FieldViolation {
	raw, err := json.Marshal(section)
	if err != nil {
		return []FieldViolation{{Field: "subgroups", Code: "DRAFT_INVALID", Message: "subgroups must be a complete section"}}
	}
	if err := ValidateSectionValue(SectionSubgroups, raw); err != nil {
		return []FieldViolation{{Field: "subgroups", Code: "DRAFT_INVALID", Message: err.Error()}}
	}
	return nil
}

// SectionAbsent reports whether a draft rules section was left out (missing or
// a JSON null on the wire).
func SectionAbsent(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) == 0 || string(trimmed) == "null"
}

// ValidateRulesSections validates the rules sections a draft carries. A draft
// may omit a section until load_business_preset copies the Global value into
// it; every present section must be a complete replace value. Anything that
// resolves the draft (preview, save, subscribe, one-shot) fails closed on a
// section that is still absent.
func ValidateRulesSections(sections RulesSections) []FieldViolation {
	_, violations := RulesSectionOverrides(sections)
	absent := map[string]bool{
		"rules." + SectionParser:       SectionAbsent(sections.Parser),
		"rules." + SectionReleaseRules: SectionAbsent(sections.ReleaseFilter),
		"rules." + SectionFileRules:    SectionAbsent(sections.FileFilter),
		"rules." + SectionNamingRules:  SectionAbsent(sections.Naming),
		"rules." + SectionFileEffect:   SectionAbsent(sections.FileEffect),
	}
	kept := violations[:0]
	for _, violation := range violations {
		if !absent[violation.Field] {
			kept = append(kept, violation)
		}
	}
	return kept
}

// ValidateNamingSectionForSave is the save-only completeness and target-shape
// check of a typed naming value: every variant needs a video filename template,
// and every directory template must render an absolute path against the loaded
// Vars snapshot. Read-back of stored values (ValidateSectionValue) does not
// re-judge it.
func ValidateNamingSectionForSave(raw json.RawMessage, vars map[string]map[string]string) error {
	var value NamingRules
	if err := decodeStrictJSON(raw, &value); err != nil {
		return fmt.Errorf("naming rules value must be typed naming rules")
	}
	if err := naming.ValidateSavableRulesWithVars(NamingRulesToDomain(value), vars); err != nil {
		return fmt.Errorf("naming rules are incomplete: %w", err)
	}
	return nil
}

// ValidateSectionValue enforces typed bounds on a complete replace value.
func ValidateSectionValue(section string, raw json.RawMessage) error {
	return ValidateSectionValueWithVars(section, raw, nil)
}

// ValidateSectionValueWithVars is the save-time form: naming templates are
// checked against the injected Vars snapshot. nil leaves `vars` references
// unvalidated (read-back of already-stored configuration); a non-nil snapshot,
// including an empty one, is enforced.
func ValidateSectionValueWithVars(section string, raw json.RawMessage, vars map[string]map[string]string) error {
	switch section {
	case SectionSubgroups:
		var value SubgroupsSection
		if err := decodeStrictJSON(raw, &value); err != nil {
			return fmt.Errorf("subgroups value must be a typed section")
		}
		if value.MaxRss < 0 || value.MaxRss > 100 {
			return fmt.Errorf("max_rss must be between 0 and 100")
		}
		if value.Entries == nil {
			return fmt.Errorf("entries is required in a complete subgroups section")
		}
		for index, entry := range value.Entries {
			if strings.TrimSpace(entry.GroupRef) == "" || len(entry.GroupRef) > 256 {
				return fmt.Errorf("entries[%d].group_ref must be non-empty and bounded", index)
			}
			if strings.TrimSpace(entry.DisplayName) == "" || len(entry.DisplayName) > 256 {
				return fmt.Errorf("entries[%d].display_name must be non-empty and bounded", index)
			}
			if entry.Provider != ProviderKindAnibt && entry.Provider != ProviderKindMikan {
				return fmt.Errorf("entries[%d].provider is invalid", index)
			}
			if entry.SelectionMode != SelectionModeAuto && entry.SelectionMode != SelectionModeManual {
				return fmt.Errorf("entries[%d].selection_mode is invalid", index)
			}
			if entry.SelectionMode == SelectionModeAuto && (entry.Score < 1 || entry.Score > 100) {
				return fmt.Errorf("entries[%d].score must be between 1 and 100 for ranked selection", index)
			}
		}
		if value.NameKeywords != nil {
			for provider, rulesPtr := range map[ProviderKind]*[]NameKeywordRule{
				ProviderKindAnibt: value.NameKeywords.Anibt,
				ProviderKindMikan: value.NameKeywords.Mikan,
			} {
				if rulesPtr == nil {
					continue
				}
				rules := *rulesPtr
				seen := make(map[string]struct{}, len(rules))
				for index, rule := range rules {
					keyword := strings.TrimSpace(rule.Keyword)
					if keyword == "" || len(keyword) > 256 {
						return fmt.Errorf("name_keywords.%s[%d].keyword must be non-empty and bounded", provider, index)
					}
					if rule.Score < -100 || rule.Score > 100 {
						return fmt.Errorf("name_keywords.%s[%d].score must be between -100 and 100", provider, index)
					}
					key := strings.ToLower(keyword)
					if _, exists := seen[key]; exists {
						return fmt.Errorf("name_keywords.%s[%d].keyword is duplicated", provider, index)
					}
					seen[key] = struct{}{}
				}
			}
		}
		if value.ProviderDefaults != nil {
			for provider, item := range map[ProviderKind]*ProviderGroupDefault{
				ProviderKindAnibt: value.ProviderDefaults.Anibt,
				ProviderKindMikan: value.ProviderDefaults.Mikan,
			} {
				if item == nil {
					continue
				}
				if item.Mode != ProviderGroupDefaultMode("blacklist") && item.Mode != ProviderGroupDefaultMode("whitelist") {
					return fmt.Errorf("provider_defaults.%s.mode is invalid", provider)
				}
				if item.Score < 1 || item.Score > 100 {
					return fmt.Errorf("provider_defaults.%s.score must be between 1 and 100", provider)
				}
				if item.Groups == nil || len(item.Groups) > 512 {
					return fmt.Errorf("provider_defaults.%s.groups must be present and bounded", provider)
				}
				// Each entry is the provider's own group key, never a display name:
				// the policy build parses it with providerdomain.NewGroupRef and
				// fails the whole section when it cannot, so an entry that is not a
				// key would break every later discovery for that subscription
				// instead of being ignored. Refuse it here, on save, with the
				// expected format in the message. ProviderKind's values are the
				// providerdomain kinds, so the conversion below is the identity.
				seen := make(map[string]struct{}, len(item.Groups))
				for index, group := range item.Groups {
					name := strings.TrimSpace(group)
					if name == "" || len(name) > 256 {
						return fmt.Errorf("provider_defaults.%s.groups[%d] must be non-empty and bounded", provider, index)
					}
					ref, err := providerdomain.NewGroupRef(providerdomain.Kind(provider), name)
					if err != nil {
						return fmt.Errorf("provider_defaults.%s.groups[%d] must be the group's %s key, not a display name (%s)", provider, index, provider, err)
					}
					if _, exists := seen[ref.ExternalID]; exists {
						return fmt.Errorf("provider_defaults.%s.groups[%d] is duplicated", provider, index)
					}
					seen[ref.ExternalID] = struct{}{}
				}
				if err := validatePinnedGroupScores(provider, item.PinnedScores); err != nil {
					return err
				}
			}
		}
	case SectionParser:
		var value ParserRules
		if err := decodeStrictJSON(raw, &value); err != nil {
			return fmt.Errorf("parser value must be typed parser rules")
		}
		if value.EpisodePatterns == nil || len(value.EpisodePatterns) > 64 {
			return fmt.Errorf("episode_patterns must be present and bounded")
		}
		for _, pattern := range value.EpisodePatterns {
			if len(pattern) > 512 {
				return fmt.Errorf("episode pattern exceeds the 512 byte bound")
			}
		}
	case SectionReleaseRules:
		return ValidateRegexFilterJSON(raw)
	case SectionFileRules:
		return ValidateFileRulesJSON(raw)
	case SectionTagFilters:
		return ValidateTagFilterRulesJSON(raw)
	case SectionDownloadCompletion:
		return ValidateDownloadCompletionJSON(raw)
	case SectionDownloadTimeouts:
		return ValidateDownloadTimeoutsJSON(raw)
	case SectionMetadataRefresh:
		return ValidateMetadataRefreshJSON(raw)
	case SectionSubtitleRules:
		return fmt.Errorf("subtitle_rules section has been retired")
	case SectionNamingRules:
		var value NamingRules
		if err := decodeStrictJSON(raw, &value); err != nil {
			return fmt.Errorf("naming rules value must be typed naming rules")
		}
		if !naming.SupportedLanguage(string(value.Language)) {
			return fmt.Errorf("naming rules language must be %q", naming.Language)
		}
		if _, err := naming.NewEngine().CompileRulesWithVars(NamingRulesToDomain(value), vars); err != nil {
			return fmt.Errorf("naming rules do not compile: %w", err)
		}
	case SectionFileEffect:
		var value FileEffectRules
		if err := decodeStrictJSON(raw, &value); err != nil {
			return fmt.Errorf("file effect value must be typed file effect rules")
		}
		switch value.Operation {
		case FileEffectRulesOperationCopy, FileEffectRulesOperationMove, FileEffectRulesOperationHardlink:
		default:
			return fmt.Errorf("operation must be copy, move, or hardlink")
		}
		switch value.CollisionPolicy {
		case CollisionPolicyFail, CollisionPolicyVersioned, CollisionPolicyOverwrite:
		default:
			return fmt.Errorf("collision_policy must be fail, versioned, or overwrite")
		}
	default:
		return fmt.Errorf("unknown section %q", section)
	}
	return nil
}

// storedSectionValue returns the value a draft or instance stores for one
// per-anime section. A missing value fails closed: Global is never read for an
// existing draft or instance. Version 0 records that the value is owned by the
// draft or instance itself.
func storedSectionValue(section string, override OverrideValue) (json.RawMessage, int64, error) {
	if len(override.Value) == 0 {
		return nil, 0, fmt.Errorf("section %s has no stored value", section)
	}
	return override.Value, 0, nil
}

// globalSectionValue resolves a Global-only section (download_completion):
// the saved Global row, otherwise the code default with version 1.
func globalSectionValue(section string, globals map[string]GlobalSection) (json.RawMessage, int64, error) {
	if global, ok := globals[section]; ok {
		return global.Value, global.Version, nil
	}
	fallback, err := DefaultSectionValue(section)
	if err != nil {
		return nil, 0, err
	}
	raw, err := json.Marshal(fallback)
	if err != nil {
		return nil, 0, fmt.Errorf("encode default section %s: %w", section, err)
	}
	return raw, 1, nil
}

// EffectiveConfig is the resolved parser/evaluator/renderer input shared by
// preview and execution.
type EffectiveConfig struct {
	Selection SubgroupsSection
	Rules     EffectiveRules
	// Versions identifies each resolved section source version.
	Versions map[string]int64
	// TagFilters is the live-resolved tag_filters section. It is not part of
	// the six frozen sections (Rules/Versions) and never enters their JSON or
	// digests. A frozen envelope decodes it as the code default; only live
	// instance resolution loads the stored Global/override value.
	TagFilters ResolvedTagFilters `json:"-"`
	// DownloadCompletion is the live-resolved Global download_completion
	// setting, outside the six frozen sections for the same reason as
	// TagFilters. It is resolved at acceptance and frozen in the release's
	// batch plan.
	DownloadCompletion ResolvedDownloadCompletion `json:"-"`
}

// ResolveEffective resolves an instance's complete effective configuration
// from the section values it stores, tag_filters included. A missing section
// fails closed. DownloadCompletion is Global-only and left unset: a caller that
// needs it applies the Global value (animeconfig.LoadDownloadCompletion).
func ResolveEffective(subgroupsOverride OverrideValue, rulesOverrides map[string]OverrideValue) (EffectiveConfig, error) {
	result, err := resolveStoredEffective(subgroupsOverride, rulesOverrides)
	if err != nil {
		return EffectiveConfig{}, err
	}
	tagFilters, err := ResolveTagFilters(rulesOverrides[SectionTagFilters])
	if err != nil {
		return EffectiveConfig{}, err
	}
	result.TagFilters = tagFilters
	return result, nil
}

// resolveStoredEffective decodes the six frozen sections from stored values.
func resolveStoredEffective(subgroupsOverride OverrideValue, rulesOverrides map[string]OverrideValue) (EffectiveConfig, error) {
	result := EffectiveConfig{Versions: map[string]int64{}}
	decode := func(section string, override OverrideValue, target any) error {
		raw, version, err := storedSectionValue(section, override)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(raw, target); err != nil {
			return fmt.Errorf("decode effective section %s: %w", section, err)
		}
		result.Versions[section] = version
		return nil
	}
	if err := decode(SectionSubgroups, subgroupsOverride, &result.Selection); err != nil {
		return EffectiveConfig{}, err
	}
	if err := decode(SectionParser, rulesOverrides[SectionParser], &result.Rules.Parser); err != nil {
		return EffectiveConfig{}, err
	}
	if err := decode(SectionReleaseRules, rulesOverrides[SectionReleaseRules], &result.Rules.ReleaseFilter); err != nil {
		return EffectiveConfig{}, err
	}
	if err := decode(SectionFileRules, rulesOverrides[SectionFileRules], &result.Rules.FileFilter); err != nil {
		return EffectiveConfig{}, err
	}
	if err := decode(SectionNamingRules, rulesOverrides[SectionNamingRules], &result.Rules.Naming); err != nil {
		return EffectiveConfig{}, err
	}
	if err := decode(SectionFileEffect, rulesOverrides[SectionFileEffect], &result.Rules.FileEffect); err != nil {
		return EffectiveConfig{}, err
	}
	return result, nil
}

// ResolveDraftEffective resolves the complete section values a pre-subscribe
// draft stores. The draft's subgroups section always applies, and every rules
// section must carry its value; Global is never read. Download completion is
// Global-only and not part of a draft: the code default is reported.
func ResolveDraftEffective(draft AnimeDraft) (EffectiveConfig, error) {
	return ResolveDraftEffectiveWithVars(draft, nil)
}

// ResolveDraftEffectiveWithVars is the save-time form: naming templates are
// validated against the injected Vars snapshot.
func ResolveDraftEffectiveWithVars(draft AnimeDraft, vars map[string]map[string]string) (EffectiveConfig, error) {
	subgroupsOverride, violations := SubgroupsSectionOverride(draft.Subgroups)
	if len(violations) > 0 {
		return EffectiveConfig{}, fmt.Errorf("subgroups override: %s", violations[0].Message)
	}
	rulesOverrides, violations := RulesSectionOverridesWithVars(draft.Rules.Sections, vars)
	if len(violations) > 0 {
		violation := violations[0]
		if violation.Code != "OVERRIDE_MODE_INVALID" {
			return EffectiveConfig{}, &SectionValidationError{Code: violation.Code, Field: violation.Field, Message: violation.Message}
		}
		return EffectiveConfig{}, fmt.Errorf("rules override %s: %s", violation.Field, violation.Message)
	}
	result, err := resolveStoredEffective(subgroupsOverride, rulesOverrides)
	if err != nil {
		return EffectiveConfig{}, err
	}
	if result.TagFilters, err = ResolveDraftTagFilters(draft); err != nil {
		return EffectiveConfig{}, err
	}
	result.DownloadCompletion = DefaultResolvedDownloadCompletion()
	return result, nil
}

// MaxPinnedGroupScores bounds one provider's pinned scores, like its group list.
const MaxPinnedGroupScores = 512

// validatePinnedGroupScores checks one provider's pinned scores: each target is
// the provider's own normalized group key (a custom text key is refused, like
// the group list), pinned at most once, with a final score in the site default
// range.
func validatePinnedGroupScores(provider ProviderKind, pins []PinnedGroupScore) error {
	if len(pins) > MaxPinnedGroupScores {
		return fmt.Errorf("provider_defaults.%s.pinned_scores must be bounded", provider)
	}
	seen := make(map[string]struct{}, len(pins))
	for index, pin := range pins {
		if strings.TrimSpace(pin.GroupRef) == "" || len(pin.GroupRef) > 256 {
			return fmt.Errorf("provider_defaults.%s.pinned_scores[%d].group_ref must be non-empty and bounded", provider, index)
		}
		ref, err := providerdomain.NewGroupRef(providerdomain.Kind(provider), pin.GroupRef)
		if err != nil {
			return fmt.Errorf("provider_defaults.%s.pinned_scores[%d].group_ref must be the group's %s key, not a display name (%s)", provider, index, provider, err)
		}
		if ref.ExternalID != pin.GroupRef {
			return fmt.Errorf("provider_defaults.%s.pinned_scores[%d].group_ref must be the normalized key %q", provider, index, ref.ExternalID)
		}
		if _, exists := seen[ref.ExternalID]; exists {
			return fmt.Errorf("provider_defaults.%s.pinned_scores[%d] is duplicated", provider, index)
		}
		seen[ref.ExternalID] = struct{}{}
		if pin.Score < 1 || pin.Score > 100 {
			return fmt.Errorf("provider_defaults.%s.pinned_scores[%d].score must be between 1 and 100", provider, index)
		}
	}
	return nil
}
