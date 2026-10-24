package naming

import (
	"fmt"
	"regexp"
	"strings"

	"bansoko/internal/templatefacts"
)

const languageResolverKey = "parsed.language"

// languageDefinition is one declarative language rule: code, description,
// and the ordered regex list. The BCP 47 output is derived from the language
// codes through the one shared table (templatefacts.LanguageTags). The
// priority order ends with ENG. There is no UNK-to-zh-CN default; unknown
// tokens resolve to und in the subtitle token rules instead.
type languageDefinition struct {
	Code        string
	Description string
	Languages   []string // complete structured language set; nil for single-code rules
	Patterns    []string
}

// languagePriority is the priority order with ENG last.
var languageDefinitions = []languageDefinition{
	// JPSC combo word (complete set jpsc+jptc; the title parser emits
	// JPSC+JPTC), primary code jpsc.
	{Code: "jpsc", Description: "简繁日", Languages: []string{"jpsc", "jptc"}, Patterns: []string{
		`\[JA?PN?[_&＆\x20]+(CHS|GB|SC)[_&＆\x20]+(CHT|BIG5|TC)\]`,
		`(简繁日内封|简繁日)`,
	}},
	// SC combo word (complete set sc+tc; the title parser emits SC+TC),
	// primary code sc.
	{Code: "sc", Description: "简繁内封", Languages: []string{"sc", "tc"}, Patterns: []string{
		`\[(CHS|GB|SC)[_&＆\x20]+(CHT|BIG5|TC)\]`,
		`\[CHS_JP[_&＆\x20]+CHT_JP\]`,
		`(CHS|GB|SC)[_&＆\x20]+(CHT|BIG5|TC)`,
		`(CHT|BIG5|TC)[_&＆\x20]+(CHS|GB|SC)`,
		`(简繁内封|简繁中文)`,
	}},
	{Code: "jpsc", Description: "简日双语", Patterns: []string{
		`(CHS|GB|SC)[_&＆\x20]+JA?PN?`,
		`\[JA?PN?[_&＆\x20]+(SC|CHS|GB)\]`,
		`\bJPSC\b`,
	}},
	{Code: "jptc", Description: "繁日双语", Patterns: []string{
		`(CHT|BIG5|TC)[_&＆\x20]+JA?PN?`,
		`\[JA?PN?[_&＆\x20]+(CHT|BIG5|TC)\]`,
		`\bJPTC\b`,
	}},
	{Code: "jpn", Description: "日语", Patterns: []string{
		`\b(JPN|JAP|JAPANESE|track5|jp)\b`,
	}},
	{Code: "tc", Description: "繁体中文", Patterns: []string{
		`\b(BIG5|CHT|TC|zh-hant|zh-tw|zh-hk|track3)\b`,
		`(繁中字幕|繁体中文)`,
	}},
	{Code: "sc", Description: "简体中文", Patterns: []string{
		`\b(GB|SC|CHS|zh-hans|zh-cn|track4)\b`,
		`(簡中字幕|简中字幕|简体中文)`,
	}},
	{Code: "eng", Description: "英语", Patterns: []string{
		`\b(ENG|ENGLISH|en-us|track2)\b`,
	}},
}

// compiledLanguageDefinitions precompiles the distribution regexes at package
// init; an invalid distribution regex is a startup gate failure.
var compiledLanguageDefinitions = func() []struct {
	languageDefinition
	patterns []*regexp.Regexp
} {
	compiled := make([]struct {
		languageDefinition
		patterns []*regexp.Regexp
	}, 0, len(languageDefinitions))
	for _, definition := range languageDefinitions {
		entry := struct {
			languageDefinition
			patterns []*regexp.Regexp
		}{languageDefinition: definition}
		for _, pattern := range definition.Patterns {
			entry.patterns = append(entry.patterns, regexp.MustCompile("(?i)"+pattern))
		}
		compiled = append(compiled, entry)
	}
	return compiled
}()

// languageTokenRule maps one subtitle suffix token to a language code via a
// case-insensitive full-match pattern; the code's BCP 47 tag comes from the
// shared table (templatefacts.LanguageTag). Unknown tokens resolve to "und"
// (there is no UNK-to-zh-CN default).
type languageTokenRule struct {
	Pattern string
	Code    string
}

var languageTokenRules = []languageTokenRule{
	{Pattern: `(?i)^(chst|简繁|简繁内封)$`, Code: "sc"},
	{Pattern: `(?i)^(jpchst|简繁日|简繁日内封)$`, Code: "jpsc"},
	{Pattern: `(?i)^(jpsc|简日|简日双语)$`, Code: "jpsc"},
	{Pattern: `(?i)^(jptc|繁日|繁日双语)$`, Code: "jptc"},
	{Pattern: `(?i)^(chs|gb|sc|zh[-_.]?(cn|hans)|简中|简体|簡中|简体中文)$`, Code: "sc"},
	{Pattern: `(?i)^(cht|big5|tc|zh[-_.]?(tw|hant|hk)|繁中|繁体|繁體|繁體中文)$`, Code: "tc"},
	{Pattern: `(?i)^(jpn|ja|jp|日语|日語)$`, Code: "jpn"},
	{Pattern: `(?i)^(eng|en|english|en[-_.]?us)$`, Code: "eng"},
}

var compiledLanguageTokenRules = func() []struct {
	rule    languageTokenRule
	pattern *regexp.Regexp
} {
	compiled := make([]struct {
		rule    languageTokenRule
		pattern *regexp.Regexp
	}, 0, len(languageTokenRules))
	for _, rule := range languageTokenRules {
		compiled = append(compiled, struct {
			rule    languageTokenRule
			pattern *regexp.Regexp
		}{rule: rule, pattern: regexp.MustCompile(rule.Pattern)})
	}
	return compiled
}()

// matchLanguageToken resolves one subtitle suffix token to a BCP47 tag. The
// second return value reports the matched rule index, or -1 for und.
func matchLanguageToken(token string) (string, int) {
	token = strings.TrimSpace(token)
	if token == "" {
		return "", -1
	}
	for index, entry := range compiledLanguageTokenRules {
		if entry.pattern.MatchString(token) {
			tag, _ := templatefacts.LanguageTag(entry.rule.Code)
			return tag, index
		}
	}
	return "und", -1
}

// languageValue is the parsed.language output. Languages carries the complete
// structured language set (title-parser JSON shape: combined embedded sets are
// several codes, never a synthesized name); Code is the normalized primary
// code derived from that set (the "compromise" single code templates default
// to; users map it further with template rules, e.g. if "sc" in Languages →
// zh-hans).
type languageValue struct {
	Code        string
	Description string
	BCP47       []string
	Languages   []string
}

func (v languageValue) String() string {
	return fmt.Sprintf("code=%s languages=%s bcp47=%s", v.Code, strings.Join(v.Languages, ","), strings.Join(v.BCP47, ","))
}

// languageResolver detects the release language from the release title by
// priority-ordered first match. No match stays null and never defaults to
// Chinese.
//
// The release namespace reads only the release: a standalone subtitle's own
// filename language is a file fact and reaches templates as sub.* (file
// first, then the release's rt.sub_lang, with sub.lang_source naming which).
// release.lang never reads the file name, so the two namespaces cannot
// disagree about which source supplied a language.
type languageResolver struct{}

func (languageResolver) Definition() ResolverDefinition {
	return ResolverDefinition{
		Key:        languageResolverKey,
		OutputType: "object",
		Nullable:   true,
	}
}

func (languageResolver) Resolve(input ResolverInput) ResolverResult {
	result := ResolverResult{Key: languageResolverKey, State: ResolverStateNull}
	title := strings.TrimSpace(input.Facts.Release.OriginalTitle)
	if title == "" {
		result.Diagnostics = append(result.Diagnostics, NamingDiagnostic{
			Code: "language_input_missing", Message: "release title is empty", Path: "Release.OriginalTitle"})
		return result
	}
	for _, definition := range compiledLanguageDefinitions {
		for index, pattern := range definition.patterns {
			span := pattern.FindStringIndex(title)
			if span == nil {
				continue
			}
			result.State = ResolverStateValue
			value := languageValue{
				Code:        definition.Code,
				Description: definition.Description,
				Languages:   append([]string(nil), definition.Languages...),
			}
			if len(value.Languages) == 0 {
				value.Languages = []string{definition.Code}
			}
			value.BCP47 = templatefacts.LanguageTags(value.Languages)
			result.Value = value
			result.Evidence = append(result.Evidence, ResolverEvidence{
				ResolverKey: languageResolverKey,
				RuleID:      fmt.Sprintf("language_%s_%d", strings.ToLower(definition.Code), index),
				RuleIndex:   index,
				InputPath:   "Release.OriginalTitle",
				MatchedSpan: &SourceSpan{Start: span[0], End: span[1]},
			})
			return result
		}
	}
	result.Diagnostics = append(result.Diagnostics, NamingDiagnostic{
		Code: "language_unmatched", Message: "no language rule matched the release title", Path: "Release.OriginalTitle"})
	return result
}
