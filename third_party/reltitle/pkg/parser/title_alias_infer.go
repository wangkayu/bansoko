package parser

import (
	"regexp"
	"strings"

	"github.com/wangkayu/reltitle/pkg/model"
)

var titleAdjunctPattern = regexp.MustCompile(`(?i)\bthe[\s_.]+(?:anime|animation)\b`)

func normalizeTitleAdjunct(s string) string {
	if !titleAdjunctPattern.MatchString(s) {
		return s
	}
	s = strings.NewReplacer("「 ", "「", " 」", "」", "『 ", "『", " 』", "』").Replace(s)
	return strings.NewReplacer(" 「", "「", " 『", "『").Replace(s)
}

func inferTitleAndAliasesFromTitleCore(title string, aliasValid func(string) bool) (string, []string) {
	s := strings.TrimSpace(title)
	if s == "" {
		return "", nil
	}
	s = normalizeTitleAdjunct(normalizeSpacedUnderscore(s))
	if strings.Contains(s, "_") && !strings.ContainsAny(s, "/／") {
		if main, aliases, ok := inferTitleAliasFromUnderscoreBridge(s, aliasValid); ok {
			return main, aliases
		}
		return s, nil
	}
	rawHasSlash := strings.ContainsAny(s, "/／")

	parts := splitByStrongTitleDelims(s)
	if rawHasSlash && len(parts) >= 2 {
		first := strings.TrimSpace(parts[0])
		cjkParts := make([]string, 0, len(parts))
		kanaParts := make([]string, 0, len(parts))
		latinParts := make([]string, 0, len(parts))
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			if hasKana(p) {
				kanaParts = append(kanaParts, p)
				continue
			}
			if hasCJK(p) {
				cjkParts = append(cjkParts, p)
				continue
			}
			if hasLatin(p) {
				latinParts = append(latinParts, p)
				continue
			}
		}
		addAlias := func(out *[]string, seen map[string]bool, cand string) {
			cand = cleanTitleToken(cand)
			if !aliasValid(cand) {
				return
			}
			k := strings.ToLower(strings.TrimSpace(cand))
			if k == "" {
				return
			}
			if !seen[k] {
				seen[k] = true
				*out = append(*out, cand)
			}
			if stripped := stripSeasonOrPartSuffix(cand); stripped != "" {
				ks := strings.ToLower(strings.TrimSpace(stripped))
				if ks != "" && !seen[ks] {
					seen[ks] = true
					*out = append(*out, stripped)
				}
			}
		}
		if hasKana(first) {
			main := normalizeTitleMain(cleanTitleToken(first))
			if main == "" {
				main = normalizeTitleMain(first)
			}
			aliases := make([]string, 0, len(parts)-1)
			seen := map[string]bool{}
			seen[strings.ToLower(strings.TrimSpace(main))] = true
			for _, a := range parts[1:] {
				addAlias(&aliases, seen, a)
			}
			if len(aliases) == 0 {
				return main, nil
			}
			return main, aliases
		}
		if hasCJK(first) {
			main := normalizeTitleMain(cleanTitleToken(first))
			if main == "" {
				main = normalizeTitleMain(first)
			}
			aliases := make([]string, 0, len(parts))
			seen := map[string]bool{}
			seen[strings.ToLower(strings.TrimSpace(main))] = true
			for _, a := range cjkParts {
				addAlias(&aliases, seen, a)
			}
			for _, a := range kanaParts {
				addAlias(&aliases, seen, a)
			}
			for _, a := range latinParts {
				addAlias(&aliases, seen, a)
			}
			if len(aliases) == 0 {
				return main, nil
			}
			return main, aliases
		}
		if isMostlyLatinToken(first) {
			firstKey := latinSemanticKeyForAlias(first)
			if firstKey != "" {
				for _, p := range parts[1:] {
					p = strings.TrimSpace(p)
					if p == "" {
						continue
					}
					// Prefer keeping the first latin segment as the primary title when
					// a later segment is a series-bridge form that already contains the same latin core.
					// Example: "Delicos Nursery / TRUMP 系列 Delico’s Nursery".
					if !hasCJK(p) || !hasLatin(p) || !looksLikeSeriesBridgeText(p) {
						continue
					}
					if !strings.Contains(latinSemanticKeyForAlias(p), firstKey) {
						continue
					}
					main := normalizeTitleMain(cleanTitleToken(first))
					if main == "" {
						main = normalizeTitleMain(first)
					}
					aliases := make([]string, 0, len(parts))
					seen := map[string]bool{}
					seen[strings.ToLower(strings.TrimSpace(main))] = true
					for _, a := range parts[1:] {
						addAlias(&aliases, seen, a)
					}
					if len(aliases) == 0 {
						return main, nil
					}
					return main, aliases
				}
			}
			firstHasSeasonBridge := extractExplicitSeasonNumber(first) != "" || stripExplicitSeasonSuffix(first) != ""
			if firstHasSeasonBridge {
				for _, p := range parts[1:] {
					p = strings.TrimSpace(p)
					if p == "" || !hasCJK(p) || hasLatin(p) {
						continue
					}
					if extractExplicitSeasonNumber(p) == "" && stripExplicitSeasonSuffix(p) == "" {
						continue
					}
					main := normalizeTitleMain(cleanTitleToken(first))
					if stripped := stripExplicitSeasonSuffix(main); stripped != "" {
						main = stripped
					}
					if main == "" {
						main = normalizeTitleMain(first)
					}
					aliases := make([]string, 0, len(parts))
					seen := map[string]bool{}
					seen[strings.ToLower(strings.TrimSpace(main))] = true
					for _, a := range parts[1:] {
						addAlias(&aliases, seen, a)
					}
					if len(aliases) == 0 {
						return main, nil
					}
					return main, aliases
				}
			}
			for _, p := range parts[1:] {
				p = strings.TrimSpace(p)
				if p == "" {
					continue
				}
				if hasCJK(p) || hasKana(p) {
					main := normalizeTitleMain(cleanTitleToken(p))
					if main == "" {
						main = normalizeTitleMain(p)
					}
					aliases := make([]string, 0, len(parts))
					seen := map[string]bool{}
					seen[strings.ToLower(strings.TrimSpace(main))] = true
					addAlias(&aliases, seen, first)
					addAlias(&aliases, seen, p)
					for _, a := range cjkParts {
						addAlias(&aliases, seen, a)
					}
					for _, a := range kanaParts {
						addAlias(&aliases, seen, a)
					}
					for _, a := range latinParts {
						addAlias(&aliases, seen, a)
					}
					if len(aliases) == 0 {
						return main, nil
					}
					return main, aliases
				}
			}
		}
		if len(cjkParts) >= 2 {
			mainParts := make([]string, 0, len(cjkParts)+1)
			mainParts = append(mainParts, cjkParts...)
			latinKeep := ""
			for _, lp := range latinParts {
				if isShortLatinTitleCandidate(lp) {
					latinKeep = lp
					break
				}
			}
			if latinKeep != "" {
				mainParts = append(mainParts, latinKeep)
			}
			main := normalizeTitleMain(strings.TrimSpace(strings.Join(mainParts, " / ")))
			aliases := make([]string, 0, len(parts))
			seen := map[string]bool{}
			seen[strings.ToLower(strings.TrimSpace(main))] = true
			for _, a := range cjkParts {
				addAlias(&aliases, seen, a)
			}
			for _, a := range latinParts {
				if latinKeep != "" && strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(latinKeep)) {
					continue
				}
				addAlias(&aliases, seen, a)
			}
			if len(aliases) == 0 {
				return main, nil
			}
			return main, aliases
		}
	}
	if len(parts) >= 2 {
		main := normalizeTitleMain(chooseMainTitlePart(parts))
		aliases := make([]string, 0, len(parts)-1)
		seen := map[string]bool{}
		seen[strings.ToLower(main)] = true
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			if strings.EqualFold(p, main) {
				continue
			}
			p = cleanAliasToken(p)
			if !aliasValid(p) {
				continue
			}
			key := strings.ToLower(p)
			if !seen[key] {
				seen[key] = true
				aliases = append(aliases, p)
			}
		}
		if len(aliases) == 0 {
			return main, nil
		}
		return main, aliases
	}

	if hasCJK(s) && hasLatin(s) {
		// A title adjunct belongs to its segment, not to a separate script alias.
		if titleAdjunctPattern.MatchString(s) || !isSimpleMixedScriptAliasCandidate(s) {
			return normalizeTitleMain(s), nil
		}
		fields := strings.Fields(s)
		cjkParts := make([]string, 0, 8)
		latinParts := make([]string, 0, 8)
		for _, f := range fields {
			ff := strings.TrimSpace(f)
			if ff == "" {
				continue
			}
			if hasCJK(ff) {
				cjkParts = append(cjkParts, ff)
				continue
			}
			if hasLatin(ff) {
				latinParts = append(latinParts, ff)
				continue
			}
		}
		if len(cjkParts) > 0 && len(latinParts) > 0 {
			main := normalizeTitleMain(strings.TrimSpace(strings.Join(cjkParts, " ")))
			mergedLatin := mergeConsecutiveLatin(latinParts)
			outAliases := make([]string, 0, len(mergedLatin))
			seen := map[string]bool{}
			seen[strings.ToLower(main)] = true
			for _, p := range mergedLatin {
				p = cleanAliasToken(p)
				if !aliasValid(p) {
					continue
				}
				k := strings.ToLower(p)
				if !seen[k] {
					seen[k] = true
					outAliases = append(outAliases, p)
				}
			}
			if len(outAliases) == 0 {
				return main, nil
			}
			return main, outAliases
		}
	}

	return normalizeTitleMain(s), nil
}

var underscoreAliasTechTailPattern = regexp.MustCompile(`(?i)\b(?:\d{3,4}p|x26[45]|h\.?26[45]|hevc|avc|aac|flac|mp4|mkv|webrip|web[- ]?dl|bdrip|dvdrip)\b`)

func inferTitleAliasFromUnderscoreBridge(s string, aliasValid func(string) bool) (string, []string, bool) {
	if strings.Count(s, "_") == 0 {
		return "", nil, false
	}
	parts := strings.Split(s, "_")
	if len(parts) < 2 {
		return "", nil, false
	}
	left := cleanTitleToken(parts[0])
	if left == "" || !hasCJK(left) {
		return "", nil, false
	}

	rightRaw := strings.TrimSpace(strings.Join(parts[1:], " "))
	rightRaw = strings.TrimSpace(strings.Trim(rightRaw, "_- "))
	if rightRaw == "" || !hasLatin(rightRaw) {
		return "", nil, false
	}
	if underscoreAliasTechTailPattern.MatchString(rightRaw) {
		return "", nil, false
	}
	alias := cleanAliasToken(rightRaw)
	if !aliasValid(alias) {
		return "", nil, false
	}

	main := normalizeTitleMain(left)
	if strings.TrimSpace(main) == "" {
		main = strings.TrimSpace(left)
	}
	if main == "" {
		return "", nil, false
	}
	return main, []string{alias}, true
}

func looksLikeSeriesBridgeText(s string) bool {
	ss := strings.TrimSpace(s)
	if ss == "" {
		return false
	}
	low := strings.ToLower(ss)
	return strings.Contains(ss, "系列") ||
		strings.Contains(ss, "シリーズ") ||
		strings.Contains(low, "series") ||
		strings.Contains(low, "franchise")
}

func latinSemanticKeyForAlias(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func isSimpleMixedScriptAliasCandidate(s string) bool {
	fields := strings.Fields(strings.TrimSpace(s))
	if len(fields) < 2 {
		return false
	}
	prevClass := 0
	switchCount := 0
	cjkCount := 0
	latinCount := 0
	for _, f := range fields {
		cur := scriptClassForAliasSplit(f)
		if cur == 0 {
			continue
		}
		if cur == 1 {
			cjkCount++
		}
		if cur == 2 {
			latinCount++
		}
		if prevClass != 0 && prevClass != cur {
			switchCount++
		}
		prevClass = cur
	}
	if cjkCount == 0 || latinCount == 0 {
		return false
	}
	// Guard against interleaved latin-cjk-latin titles (e.g. "TRUMP 系列 Delico’s Nursery").
	return switchCount <= 1
}

func scriptClassForAliasSplit(s string) int {
	switch {
	case hasCJK(s) || hasKana(s):
		return 1
	case hasLatin(s):
		return 2
	default:
		return 0
	}
}

func inferTitleAndAliasesFromTitleCanonical(title string, aliasValid func(string) bool) (string, []string) {
	main, aliases := inferTitleAndAliasesFromTitleCore(title, aliasValid)
	return canonicalizeInferredAliases(main, aliases, aliasValid)
}

func canonicalizeInferredAliases(main string, aliases []string, aliasValid func(string) bool) (string, []string) {
	if len(aliases) == 0 {
		return main, nil
	}
	mainKey := strings.ToLower(strings.TrimSpace(main))
	seen := map[string]bool{}
	if mainKey != "" {
		seen[mainKey] = true
	}
	out := make([]string, 0, len(aliases))
	for _, a := range aliases {
		a = cleanAliasToken(a)
		if stripped := stripExplicitSeasonSuffix(a); stripped != "" {
			a = stripped
		}
		if !aliasValid(a) {
			continue
		}
		k := strings.ToLower(strings.TrimSpace(a))
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, a)
	}
	if len(out) == 0 {
		return main, nil
	}
	return main, out
}

func inferPostEpisodeSearchHintAliasesFromParens(tokens []model.Token, episodeIdx int, episodeRangeIdx int, attrs *[]model.Attribute) []string {
	epStart := len(tokens)
	if episodeRangeIdx >= 0 && episodeRangeIdx < epStart {
		epStart = episodeRangeIdx
	}
	if episodeIdx >= 0 && episodeIdx < epStart {
		epStart = episodeIdx
	}
	if epStart < 0 || epStart >= len(tokens) {
		return nil
	}
	for i := epStart; i < len(tokens); i++ {
		if tokens[i].Removed {
			continue
		}
		if tokens[i].Text != "(" && tokens[i].Text != "（" {
			continue
		}
		_, closeIdx, inner, ok := parseParenBlock(tokens, i, len(tokens))
		if !ok {
			continue
		}
		inner = strings.TrimSpace(inner)
		if inner == "" {
			i = closeIdx
			continue
		}
		cands, isSearchHint, hintKeyword := splitAliasCandidatesFromParenContent(inner)
		if !isSearchHint {
			i = closeIdx
			continue
		}
		if attrs != nil {
			hk := strings.TrimSpace(hintKeyword)
			if hk != "" {
				*attrs = append(*attrs, model.Attribute{Type: model.AttrNote, Normalized: hk, Confidence: 0.6, Source: model.AttrSourceHintKeyword})
			}
		}
		for _, c := range cands {
			c = cleanAliasToken(c)
			if c == "" {
				continue
			}
			appendSearchHintAttribute(attrs, c, model.AttrSourceEPTailSearchHint)
		}
		i = closeIdx
	}
	return nil
}
