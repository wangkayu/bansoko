package naming

import (
	"fmt"
	"regexp"
	"strings"

	"bansoko/internal/parser/reltitletags"
	"bansoko/internal/templatefacts"
)

const subtitleResolverKey = "parsed.subtitle"

// subtitleValue is the atomic parsed.subtitle output.
type subtitleValue struct {
	Format          string
	OriginalSuffix  string
	Languages       []string
	PrimaryLanguage string
	// TagLanguages are the reltitle sub_lang codes read from the suffix (the
	// vocabulary of rt.lang); nil when reltitle reads none.
	TagLanguages []string
	Flags        SubtitleFlags
}

func (v subtitleValue) String() string {
	return fmt.Sprintf("format=%s suffix=%q languages=%s codes=%s flags=%+v", v.Format, v.OriginalSuffix, strings.Join(v.Languages, ","), strings.Join(v.TagLanguages, "+"), v.Flags)
}

// subtitleFormats are the normalized standalone subtitle extensions.
var subtitleFormats = map[string]bool{"ass": true, "ssa": true, "srt": true, "vtt": true}

// subtitleLanguageTags maps one subtitle suffix token to its BCP 47 tags
// through reltitle's sub_lang vocabulary — the same dictionary the release
// title is parsed with. A token may carry several languages (`简繁` is SC and
// TC), and a code without a BCP 47 tag is left out; a spelling the dictionary
// does not read has no language.
func subtitleLanguageTags(token string) []string {
	var tags []string
	for _, code := range reltitletags.SubtitleLanguages(token) {
		if tag, ok := templatefacts.LanguageTag(code); ok {
			tags = append(tags, tag)
		}
	}
	return tags
}

// subtitleTokenSplitter splits one original suffix into chunks at the
// separators that always delimit qualifiers. A hyphen stays inside a chunk:
// it may join a BCP 47 tag, and subtitleTokens decides whether it separates
// languages instead.
var subtitleTokenSplitter = regexp.MustCompile(`[_&+．.]`)

// subtitleTokens splits one original suffix into qualifier tokens. A
// hyphenated chunk stays one token when reltitle reads the whole as a
// language and at least one half is not a language on its own — the shape of
// a BCP 47 tag (`zh-Hans`, `zh-CN`, `en-US`). When every half is a language
// itself (`chs-jpn`, `sc-tc`) the hyphen lists languages, so the halves stay
// separate tokens in their original order.
func subtitleTokens(suffix string) []string {
	var tokens []string
	for _, chunk := range subtitleTokenSplitter.Split(suffix, -1) {
		chunk = strings.TrimSpace(chunk)
		if chunk == "" {
			continue
		}
		parts := strings.Split(chunk, "-")
		if len(parts) > 1 && len(reltitletags.SubtitleLanguages(chunk)) > 0 {
			allPartsRead := true
			for _, part := range parts {
				if len(reltitletags.SubtitleLanguages(strings.TrimSpace(part))) == 0 {
					allPartsRead = false
					break
				}
			}
			if !allPartsRead {
				tokens = append(tokens, chunk)
				continue
			}
		}
		for _, part := range parts {
			if part = strings.TrimSpace(part); part != "" {
				tokens = append(tokens, part)
			}
		}
	}
	return tokens
}

// subtitleFlagTokens maps flag tokens (case-insensitive contains) to flags.
var subtitleFlagTokens = []struct {
	token string
	apply func(*SubtitleFlags)
}{
	{"forced", func(f *SubtitleFlags) { f.Forced = true }},
	{"sdh", func(f *SubtitleFlags) { f.SDH = true }},
	{"commentary", func(f *SubtitleFlags) { f.Commentary = true }},
	{"default", func(f *SubtitleFlags) { f.Default = true }},
}

// subtitleResolver classifies standalone subtitle files: format, original
// suffix, suffix languages, and qualifier flags. Non-subtitle files resolve
// to null without diagnostics.
type subtitleResolver struct{}

func (subtitleResolver) Definition() ResolverDefinition {
	return ResolverDefinition{
		Key:          subtitleResolverKey,
		OutputType:   "object",
		Dependencies: []string{languageResolverKey},
		Nullable:     true,
	}
}

func (subtitleResolver) Resolve(input ResolverInput) ResolverResult {
	result := ResolverResult{Key: subtitleResolverKey, State: ResolverStateNull}
	if strings.ToLower(strings.TrimSpace(input.Facts.File.Kind)) != "subtitle" {
		return result
	}
	extension := strings.ToLower(strings.TrimSpace(input.Facts.File.Extension))
	if !subtitleFormats[extension] {
		result.Diagnostics = append(result.Diagnostics, NamingDiagnostic{
			Code: "subtitle_format_unknown", Message: "subtitle extension is not a known text subtitle format", Path: "File.Extension"})
		return result
	}
	basename := input.Facts.File.Basename
	suffix := ""
	var suffixSpan *SourceSpan
	if index := strings.LastIndex(basename, "."); index >= 0 && index+1 < len(basename) {
		suffix = basename[index+1:]
		// The span is expressed against File.OriginalName; the basename is a
		// prefix of the original name, so offsets carry over directly.
		suffixSpan = &SourceSpan{Start: index + 1, End: index + 1 + len(suffix)}
	}

	value := subtitleValue{Format: extension, OriginalSuffix: suffix, TagLanguages: reltitletags.SubtitleLanguages(suffix)}
	if suffix != "" {
		result.Evidence = append(result.Evidence, ResolverEvidence{
			ResolverKey: subtitleResolverKey,
			RuleID:      "subtitle_suffix",
			RuleIndex:   0,
			InputPath:   "File.OriginalName",
			MatchedSpan: suffixSpan,
			Captures:    map[string]string{"suffix": suffix},
		})
		seen := map[string]bool{}
		for tokenIndex, token := range subtitleTokens(suffix) {
			lower := strings.ToLower(token)
			isFlag := false
			for _, flag := range subtitleFlagTokens {
				if strings.Contains(lower, flag.token) {
					flag.apply(&value.Flags)
					isFlag = true
				}
			}
			if isFlag {
				result.Evidence = append(result.Evidence, ResolverEvidence{
					ResolverKey: subtitleResolverKey,
					RuleID:      fmt.Sprintf("subtitle_flag_%d", tokenIndex),
					RuleIndex:   tokenIndex,
					InputPath:   "File.OriginalName",
					Captures:    map[string]string{"token": token},
				})
				continue
			}
			tags := subtitleLanguageTags(token)
			unknown := len(tags) == 0
			if unknown {
				tags = []string{"und"}
			}
			for _, tag := range tags {
				if !seen[tag] {
					seen[tag] = true
					value.Languages = append(value.Languages, tag)
				}
			}
			ruleID := fmt.Sprintf("subtitle_language_token_%d", tokenIndex)
			if unknown {
				ruleID = fmt.Sprintf("subtitle_language_token_%d_und", tokenIndex)
			}
			result.Evidence = append(result.Evidence, ResolverEvidence{
				ResolverKey: subtitleResolverKey,
				RuleID:      ruleID,
				RuleIndex:   tokenIndex,
				InputPath:   "File.OriginalName",
				Captures:    map[string]string{"token": token, "bcp47": strings.Join(tags, "+")},
			})
		}
	}
	if len(value.Languages) > 0 {
		value.PrimaryLanguage = value.Languages[0]
	}
	result.State = ResolverStateValue
	result.Value = value
	return result
}
