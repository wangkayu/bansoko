package parser

import (
	"regexp"
	"strings"

	"github.com/wangkayu/reltitle/pkg/model"
)

func attrValues(res *model.ParseResult, t model.AttrType) []string {
	if res == nil {
		return nil
	}
	out := make([]string, 0, 4)
	for _, a := range res.Attributes {
		if a.Type != t {
			continue
		}
		v := strings.TrimSpace(a.Normalized)
		if v == "" {
			continue
		}
		out = append(out, v)
	}
	return out
}

func firstAttrValue(res *model.ParseResult, t model.AttrType) string {
	for _, v := range attrValues(res, t) {
		return v
	}
	return ""
}

func canonicalTitle(res *model.ParseResult) string {
	return firstAttrValue(res, model.AttrTitle)
}

func canonicalGroup(res *model.ParseResult) string {
	return firstAttrValue(res, model.AttrGroup)
}

func canonicalEpisode(res *model.ParseResult) string {
	if res == nil {
		return ""
	}
	return strings.TrimSpace(res.CanonicalEpisode())
}

func canonicalEPSemantics(res *model.ParseResult) *model.EPSemantics {
	if res == nil {
		return nil
	}
	return res.CanonicalEPSemantics()
}

func canonicalEPKind(res *model.ParseResult) string {
	if sem := canonicalEPSemantics(res); sem != nil && sem.Primary != nil {
		return string(sem.Primary.Kind.Canonical())
	}
	return ""
}

func canonicalEPAxis(res *model.ParseResult) string {
	if sem := canonicalEPSemantics(res); sem != nil && sem.Primary != nil {
		return string(sem.Primary.Axis.Canonical())
	}
	return ""
}

func canonicalEPSource(res *model.ParseResult) string {
	if sem := canonicalEPSemantics(res); sem != nil && sem.Primary != nil {
		return strings.TrimSpace(string(sem.Primary.Source))
	}
	return ""
}

func canonicalEpisodeList(res *model.ParseResult) []string {
	if sem := canonicalEPSemantics(res); sem != nil {
		if sem.Primary != nil && len(sem.Primary.EpisodeList) > 0 {
			return append([]string(nil), sem.Primary.EpisodeList...)
		}
	}
	return nil
}

func canonicalEPAlternates(res *model.ParseResult) []model.EPReference {
	if sem := canonicalEPSemantics(res); sem != nil && len(sem.Alternates) > 0 {
		return append([]model.EPReference(nil), sem.Alternates...)
	}
	return nil
}

func canonicalSeasonRange(res *model.ParseResult) string {
	if res == nil {
		return ""
	}
	return strings.TrimSpace(res.CanonicalSeasonRange())
}

func canonicalEpisodeRange(res *model.ParseResult) string {
	if res == nil {
		return ""
	}
	return strings.TrimSpace(res.CanonicalEpisodeRange())
}

func canonicalSeason(res *model.ParseResult) string {
	if res == nil {
		return ""
	}
	return strings.TrimSpace(res.CanonicalSeason())
}

func canonicalVersion(res *model.ParseResult) string {
	return firstAttrValue(res, model.AttrVersion)
}

func canonicalSPKind(res *model.ParseResult) string {
	if res == nil {
		return ""
	}
	return strings.TrimSpace(res.CanonicalSPKind())
}

func canonicalAliases(res *model.ParseResult) []string {
	return attrValues(res, model.AttrTitleAlias)
}

func canonicalPackKind(res *model.ParseResult) string {
	return firstAttrValue(res, model.AttrPackKind)
}

func canonicalPackProfile(res *model.ParseResult) string {
	return firstAttrValue(res, model.AttrPackProfile)
}

var testCJKEpisodeTokenPattern = regexp.MustCompile(`^第?0*(\d{1,4})(?:集|话|話|回|章)$`)

func episodeTokenStart(res *model.ParseResult) int {
	if res == nil {
		return -1
	}
	episode := strings.TrimSpace(res.CanonicalEpisode())
	if episode == "" {
		return -1
	}
	season := strings.TrimSpace(res.CanonicalSeason())
	for i, tok := range res.Tokens {
		if tokenMatchesEpisode(tok.Text, episode, season) {
			return i
		}
	}
	return -1
}

func tokenMatchesEpisode(text string, episode string, season string) bool {
	episode = strings.TrimSpace(episode)
	if episode == "" {
		return false
	}
	t := strings.TrimSpace(text)
	if t == "" {
		return false
	}
	compact := strings.ToLower(strings.ReplaceAll(t, " ", ""))
	if compact == "" {
		return false
	}
	if m := testCJKEpisodeTokenPattern.FindStringSubmatch(t); len(m) == 2 {
		return strings.TrimLeft(m[1], "0") == strings.TrimLeft(episode, "0")
	}
	plain := regexp.MustCompile(`(?i)^(?:e|ep)?0*` + regexp.QuoteMeta(episode) + `(?:v\d+)?$`)
	if plain.MatchString(compact) {
		return true
	}
	if season != "" {
		sxxexx := regexp.MustCompile(`(?i)^s0*` + regexp.QuoteMeta(season) + `e0*` + regexp.QuoteMeta(episode) + `(?:v\d+)?$`)
		if sxxexx.MatchString(compact) {
			return true
		}
	}
	return false
}
