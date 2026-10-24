package naming

import (
	"fmt"
	"regexp"
	"strings"

	"bansoko/internal/parser/reltitletags"
)

const subtitleResolverKey = "parsed.subtitle"

// subtitleValue is the atomic parsed.subtitle output.
type subtitleValue struct {
	Format          string
	OriginalSuffix  string
	Languages       []string
	PrimaryLanguage string
	// TagLanguages are the reltitle sub_lang codes read from the suffix (the
	// vocabulary of rt.sub_lang); nil when reltitle reads none.
	TagLanguages []string
	Flags        SubtitleFlags
}

func (v subtitleValue) String() string {
	return fmt.Sprintf("format=%s suffix=%q languages=%s codes=%s flags=%+v", v.Format, v.OriginalSuffix, strings.Join(v.Languages, ","), strings.Join(v.TagLanguages, "+"), v.Flags)
}

// subtitleFormats are the normalized standalone subtitle extensions.
var subtitleFormats = map[string]bool{"ass": true, "ssa": true, "srt": true, "vtt": true}

// subtitleTokenSplitter splits one original suffix into qualifier tokens.
var subtitleTokenSplitter = regexp.MustCompile(`[_\-&+．.]`)

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
		for tokenIndex, token := range subtitleTokenSplitter.Split(suffix, -1) {
			token = strings.TrimSpace(token)
			if token == "" {
				continue
			}
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
			bcp47, ruleIndex := matchLanguageToken(token)
			if !seen[bcp47] {
				seen[bcp47] = true
				value.Languages = append(value.Languages, bcp47)
			}
			ruleID := fmt.Sprintf("subtitle_language_token_%d", tokenIndex)
			if ruleIndex < 0 {
				ruleID = fmt.Sprintf("subtitle_language_token_%d_und", tokenIndex)
			}
			result.Evidence = append(result.Evidence, ResolverEvidence{
				ResolverKey: subtitleResolverKey,
				RuleID:      ruleID,
				RuleIndex:   tokenIndex,
				InputPath:   "File.OriginalName",
				Captures:    map[string]string{"token": token, "bcp47": bcp47},
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
