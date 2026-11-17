package naming

import (
	"fmt"
	"strings"

	"bansoko/internal/templatefacts"
)

const languageResolverKey = "parsed.language"

// languageValue is the parsed.language output drawn from the release title's
// frozen rt.sub_lang vocabulary: the normalized primary code, the complete
// language list and the BCP 47 tags of that list. There is no second parse:
// the file name and the AniBT tag set never supply release.lang.
type languageValue struct {
	Code      string
	BCP47     []string
	Languages []string
}

func (v languageValue) String() string {
	return fmt.Sprintf("code=%s languages=%s bcp47=%s", v.Code, strings.Join(v.Languages, ","), strings.Join(v.BCP47, ","))
}

// languageResolver projects the release title's language codes onto
// parsed.language. The codes are the reltitle sub_lang values naming a
// language; no match stays null and never defaults to Chinese.
//
// The release namespace reads only the release: a standalone subtitle's own
// filename language is a file fact and reaches templates as sub.* (file
// first, then the release's rt.lang, with sub.lang_source naming which).
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
	subLang, _ := input.Facts.Rt.Get("SubLang")
	codes := releaseLanguageCodes(subLang)
	if len(codes) == 0 {
		result.Diagnostics = append(result.Diagnostics, NamingDiagnostic{
			Code: "language_unmatched", Message: "the release title names no language", Path: "Rt.SubLang"})
		return result
	}
	result.State = ResolverStateValue
	result.Value = languageValue{
		Code:      subtitleLangCode(codes),
		Languages: codes,
		BCP47:     templatefacts.LanguageTags(codes),
	}
	result.Evidence = append(result.Evidence, ResolverEvidence{
		ResolverKey: languageResolverKey,
		RuleID:      "release_sub_lang",
		InputPath:   "Rt.SubLang",
		Captures:    map[string]string{"languages": strings.Join(codes, "+")},
	})
	return result
}

// releaseLanguageCodes is the release's language list: the reltitle sub_lang
// values that name a language (a vocabulary code with a BCP 47 tag), trimmed,
// lowercased and de-duplicated in their frozen order. RAW, MULTI and any other
// token without a language tag name no language and are left out.
func releaseLanguageCodes(joined string) []string {
	var codes []string
	seen := map[string]bool{}
	for _, value := range templatefacts.SplitValues(joined) {
		code := strings.ToLower(strings.TrimSpace(value))
		if code == "" || seen[code] {
			continue
		}
		if _, ok := templatefacts.LanguageTag(code); !ok {
			continue
		}
		seen[code] = true
		codes = append(codes, code)
	}
	return codes
}
