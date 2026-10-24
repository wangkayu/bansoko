package parser

import (
	"regexp"
	"strings"

	"github.com/wangkayu/reltitle/pkg/model"
	"github.com/wangkayu/reltitle/pkg/tokenize"
)

func (p *Parser) inferAliasesFromAdjacentBrackets(tokens []model.Token, titleEnd int, episodeIdx int, episodeRangeIdx int) []string {
	if titleEnd < 0 {
		return nil
	}
	epStart := len(tokens)
	if episodeRangeIdx >= 0 && episodeRangeIdx < epStart {
		epStart = episodeRangeIdx
	}
	if episodeIdx >= 0 && episodeIdx < epStart {
		epStart = episodeIdx
	}
	if epStart <= titleEnd {
		return nil
	}
	aliases := make([]string, 0, 4)
	for i := titleEnd; i < epStart; i++ {
		open, close, ok := headAliasBracketPair(tokens[i].Text)
		if !ok {
			continue
		}
		end := -1
		depth := 0
		for j := i; j < epStart; j++ {
			if tokens[j].Text == open {
				depth++
			} else if tokens[j].Text == close {
				if depth > 0 {
					depth--
					if depth == 0 {
						end = j
						break
					}
				}
			}
		}
		if end < 0 {
			continue
		}
		parts := make([]string, 0, end-i)
		allRemoved := true
		for k := i + 1; k < end; k++ {
			allRemoved = allRemoved && tokens[k].Removed
			t := strings.TrimSpace(tokens[k].Text)
			if t == "" {
				continue
			}
			parts = append(parts, t)
		}
		inner := strings.TrimSpace(strings.Join(parts, " "))
		if inner == "" || allRemoved && (open == "[" || open == "【") {
			i = end
			continue
		}
		if isLikelyMetaBracketText(inner) || looksLikeDateOrTimeText(inner) {
			i = end
			continue
		}
		splits := splitByStrongTitleDelims(inner)
		for _, s := range splits {
			s = cleanAliasToken(s)
			if stripped := stripExplicitSeasonSuffix(s); stripped != "" {
				s = stripped
			}
			if !p.isValidAliasToken(s) {
				continue
			}
			aliases = append(aliases, s)
		}
		i = end
	}
	if len(aliases) == 0 {
		return nil
	}
	return aliases
}

func (p *Parser) inferBracketTitleFallback(tokens []model.Token, scanStart int, episodeIdx int, episodeRangeIdx int) (string, []string, int, int) {
	if scanStart < 0 {
		scanStart = 0
	}
	if scanStart >= len(tokens) {
		return "", nil, -1, -1
	}
	epStart := len(tokens)
	if episodeRangeIdx >= 0 && episodeRangeIdx < epStart {
		epStart = episodeRangeIdx
	}
	if episodeIdx >= 0 && episodeIdx < epStart {
		epStart = episodeIdx
	}
	if epStart <= scanStart {
		return "", nil, -1, -1
	}

	for i := scanStart; i < epStart; i++ {
		open, close, ok := headAliasBracketPair(tokens[i].Text)
		if !ok {
			continue
		}
		end := -1
		depth := 0
		for j := i; j < epStart; j++ {
			if tokens[j].Text == open {
				depth++
				continue
			}
			if tokens[j].Text == close {
				if depth > 0 {
					depth--
					if depth == 0 {
						end = j
						break
					}
				}
			}
		}
		if end < 0 {
			continue
		}

		parts := make([]string, 0, end-i)
		for k := i + 1; k < end; k++ {
			t := strings.TrimSpace(tokens[k].Text)
			if t == "" {
				continue
			}
			if t == "_" || t == "-" || t == "." || t == "|" || t == "｜" {
				continue
			}
			parts = append(parts, t)
		}
		inner := strings.TrimSpace(strings.Join(parts, " "))
		if inner == "" || isLikelyMetaBracketText(inner) {
			i = end
			continue
		}
		main, aliases := inferTitleAndAliasesFromTitleCanonical(inner, p.isValidAliasToken)
		main = sanitizePrimaryTitleForOutput(main)
		if strings.TrimSpace(main) == "" {
			i = end
			continue
		}
		if isLikelyGroupLikeTitleFallback(main) {
			i = end
			continue
		}
		return main, aliases, i + 1, end
	}
	return "", nil, -1, -1
}

var bracketTitleFallbackGroupLikePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._&+\-]{1,31}$`)

func isLikelyGroupLikeTitleFallback(s string) bool {
	ss := strings.TrimSpace(s)
	if ss == "" {
		return true
	}
	if hasCJK(ss) {
		return false
	}
	if strings.ContainsAny(ss, " /|｜;；:\\") {
		return false
	}
	if strings.Contains(ss, " ") {
		return false
	}
	up := strings.ToUpper(ss)
	if strings.Contains(up, "RAWS") || strings.Contains(up, "SUBS") {
		return true
	}
	return bracketTitleFallbackGroupLikePattern.MatchString(ss)
}

var tailGroupFromRawTitlePattern = regexp.MustCompile(`(?i)(?:^|\s)-(?:\s*)?([A-Za-z][A-Za-z0-9]{1,24}(?:-[A-Za-z0-9]{1,24}){0,4}-(?:Raws|Subs))\b`)
var tailGroupFromRawTitleAnyPattern = regexp.MustCompile(`(?i)-(\s*)?([A-Za-z][A-Za-z0-9]{2,20})\s*$`)
var tailGroupFromRawTitleBracketPattern = regexp.MustCompile(`(?:\[([^\[\]]{1,80})\]|【([^【】]{1,80})】)\s*$`)

func (p *Parser) detectTailGroupFromRawTitle(rawTitle string) string {
	s := strings.TrimSpace(rawTitle)
	if s == "" {
		return ""
	}
	if reDetectTailGroupFromRawTitle1.MatchString(s) {
		return ""
	}
	if reDetectTailGroupFromRawTitle2.MatchString(s) {
		return ""
	}
	if m := tailGroupFromRawTitlePattern.FindStringSubmatch(s); len(m) == 2 {
		g := strings.TrimSpace(m[1])
		if g != "" {
			return g
		}
	}
	// Generic release tail group like "...-HDSWEB" / "...-VARYG".
	if m := tailGroupFromRawTitleAnyPattern.FindStringSubmatch(s); len(m) == 3 {
		g := strings.TrimSpace(m[2])
		if g != "" && isTailGroupToken(g) {
			return g
		}
	}
	// Tail bracket group like "... [SnC-Fansub]".
	if m := tailGroupFromRawTitleBracketPattern.FindStringSubmatch(s); len(m) == 3 {
		cand := strings.TrimSpace(m[1])
		if cand == "" {
			cand = strings.TrimSpace(m[2])
		}
		cand = normalizeGroupText(cand)
		if p.isTailBracketGroupCandidate(cand) {
			return cand
		}
	}
	return ""
}

func (p *Parser) isTailBracketGroupCandidate(s string) bool {
	s = normalizeGroupText(s)
	if s == "" {
		return false
	}
	if !isGroupCandidateText(s) {
		return false
	}
	if isLikelySubtitleBracketGroupText(s) {
		return false
	}
	if isLikelyMetaBracketText(s) || isEpisodeLikeBracketText(s) {
		return false
	}
	if _, known := p.isKnownGroup(s); known {
		return true
	}
	return hasExplicitGroupIdentityMarker(s)
}

var explicitGroupIdentitySuffixPattern = regexp.MustCompile(`(?i)(?:fansub|subs?|raws?|team|studio|group)$`)

func hasExplicitGroupIdentityMarker(s string) bool {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return false
	}
	for _, suffix := range []string{"字幕组", "字幕組", "字幕社", "汉化组", "漢化組", "压制组", "壓制組", "制作组", "製作組", "搬运组", "搬運組", "组", "組"} {
		if strings.HasSuffix(trimmed, suffix) {
			return true
		}
	}
	return explicitGroupIdentitySuffixPattern.MatchString(trimmed)
}

func isLikelySubtitleBracketGroupText(s string) bool {
	low := strings.ToLower(strings.TrimSpace(s))
	if low == "" {
		return false
	}
	if reAliasSubtitleCJKMetaPhrase.MatchString(strings.TrimSpace(s)) {
		return true
	}
	// Multi-language subtitle markers like "Multi language subs", "Multi subs", etc.
	if reIsLikelySubtitleBracketGroupText1.MatchString(strings.TrimSpace(s)) {
		return true
	}
	if reIsLikelySubtitleBracketGroupText2.MatchString(strings.TrimSpace(s)) {
		return true
	}
	return reIsLikelySubtitleBracketGroupText3.MatchString(low)
}

func detectHeadBracketGroupFromRawTitle(rawTitle string) string {
	rawTitle = strings.TrimSpace(rawTitle)
	if rawTitle == "" {
		return ""
	}
	rawTokens := tokenize.Tokenize(rawTitle)
	if len(rawTokens) == 0 {
		return ""
	}
	tokens := make([]model.Token, 0, len(rawTokens))
	for _, t := range rawTokens {
		tokens = append(tokens, model.Token{Text: t})
	}
	g, _ := extractHeadBracketGroup(tokens)
	return strings.TrimSpace(g)
}

func promoteFrenchMultiNoteToAudioFlag(attrs []model.Attribute) {
	if len(attrs) == 0 {
		return
	}
	hasFrench := false
	for i := range attrs {
		if attrs[i].Type == model.AttrSubFlag {
			if strings.EqualFold(strings.TrimSpace(attrs[i].Normalized), "VOSTFR") {
				hasFrench = true
				break
			}
		}
		if attrs[i].Type == model.AttrAudioLang {
			if strings.EqualFold(strings.TrimSpace(attrs[i].Normalized), "FRA") {
				hasFrench = true
				break
			}
		}
		if attrs[i].Type == model.AttrNote {
			n := strings.ToUpper(strings.TrimSpace(attrs[i].Normalized))
			if n == "VF" || n == "FRENCH" || n == "FRANCAIS" || n == "TRUEFRENCH" {
				hasFrench = true
				break
			}
		}
	}
	if !hasFrench {
		return
	}
	for i := range attrs {
		if attrs[i].Type != model.AttrNote {
			continue
		}
		if strings.ToUpper(strings.TrimSpace(attrs[i].Normalized)) != "MULTI" {
			continue
		}
		attrs[i].Type = model.AttrAudioFlag
		attrs[i].Normalized = "MULTI_AUDIO"
		if attrs[i].Confidence < 0.65 {
			attrs[i].Confidence = 0.65
		}
		attrs[i].Source = model.AttrSourceHeuristicMultiAudio
	}
}

// Regular expressions hoisted out of function bodies so they are compiled once at init.
var (
	reDetectTailGroupFromRawTitle1      = regexp.MustCompile(`(?i)\bcustom\b\s*$`)
	reDetectTailGroupFromRawTitle2      = regexp.MustCompile(`(?i)\bhi[\s_-]*res\b\s*$`)
	reIsLikelySubtitleBracketGroupText1 = regexp.MustCompile(`(?i)^multi\s+language\s+subs?$`)
	reIsLikelySubtitleBracketGroupText2 = regexp.MustCompile(`(?i)^sub(?:title|titles)?(?:[_ .-]+[\p{L}]{2,16})+$`)
	reIsLikelySubtitleBracketGroupText3 = regexp.MustCompile(`(?i)^sub(?:title|titles)?[_ .-]?[a-z]{2,8}$`)
)

func normalizeGroupText(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	s = strings.Join(strings.Fields(s), " ")
	s = groupHyphenSpacePattern.ReplaceAllString(s, `$1-$2`)
	// Normalize '&' / '＆' without surrounding spaces.
	s = groupAmpSpacePattern.ReplaceAllString(s, "&")
	// Normalize ',' / '，' without surrounding spaces.
	s = groupCommaSpacePattern.ReplaceAllString(s, ",")
	return s
}

func detectTechPrefixedGroup(tokens []model.Token) (string, int) {
	tech := map[string]bool{
		"pcm":  true,
		"x264": true,
		"x265": true,
		"h264": true,
		"h265": true,
	}
	for i := 0; i+3 < len(tokens); i++ {
		if tokens[i].Removed || tokens[i+1].Removed || tokens[i+2].Removed {
			continue
		}
		if isInBracket(tokens, i) || isInParen(tokens, i) {
			continue
		}
		k := strings.ToLower(strings.TrimSpace(tokens[i].Text))
		if !tech[k] {
			continue
		}
		if strings.TrimSpace(tokens[i+1].Text) != "-" {
			continue
		}
		g := strings.TrimSpace(tokens[i+2].Text)
		if !isTailGroupToken(g) {
			continue
		}
		if !hasUpperASCII(g) {
			continue
		}
		if strings.TrimSpace(tokens[i+3].Text) != "-" {
			continue
		}
		return g, i + 2
	}
	return "", -1
}

func extractInlineStarGroup(tokens []model.Token) string {
	if len(tokens) == 0 {
		return ""
	}
	if tokens[0].Removed {
		return ""
	}
	validLeft := func(left string) bool {
		left = strings.TrimSpace(left)
		if left == "" {
			return false
		}
		if looksLikeLeadingTitleMeta(left) {
			return false
		}
		if len([]rune(left)) > 24 {
			return false
		}
		// must contain at least one non-ascii rune or '&'
		hasNonASCII := false
		for _, r := range left {
			if r == '&' {
				hasNonASCII = true
				break
			}
			if r > 127 {
				hasNonASCII = true
				break
			}
		}
		return hasNonASCII
	}

	// Case A: tokenizer split the delimiter into its own token.
	// Example: ["六四位元字幕組", "★", "..."].
	if len(tokens) >= 2 {
		sep := strings.TrimSpace(tokens[1].Text)
		if sep == "★" || sep == "☆" {
			left := strings.TrimSpace(tokens[0].Text)
			if validLeft(left) {
				hasRight := false
				maxLookahead := 16
				if maxLookahead > len(tokens) {
					maxLookahead = len(tokens)
				}
				for j := 2; j < maxLookahead; j++ {
					if tokens[j].Removed {
						continue
					}
					if strings.TrimSpace(tokens[j].Text) != "" {
						hasRight = true
						break
					}
				}
				if hasRight {
					tokens[0].Removed = true
					tokens[1].Removed = true
					return left
				}
			}
		}
	}
	// Case A2: two-token group prefix before star.
	// Example: ["SFSub&", "六四位元字幕組", "★", "..."].
	if len(tokens) >= 3 {
		sep := strings.TrimSpace(tokens[2].Text)
		if sep == "★" || sep == "☆" {
			leftA := strings.TrimSpace(tokens[0].Text)
			leftB := strings.TrimSpace(tokens[1].Text)
			joined := strings.TrimSpace(leftA + " " + leftB)
			if validLeft(joined) || (strings.HasSuffix(leftA, "&") && hasStrongGroupKeyword(leftB)) {
				hasRight := false
				maxLookahead := 20
				if maxLookahead > len(tokens) {
					maxLookahead = len(tokens)
				}
				for j := 3; j < maxLookahead; j++ {
					if tokens[j].Removed {
						continue
					}
					if strings.TrimSpace(tokens[j].Text) != "" {
						hasRight = true
						break
					}
				}
				if hasRight {
					tokens[0].Removed = true
					tokens[1].Removed = true
					tokens[2].Removed = true
					return joined
				}
			}
		}
	}

	// Case B: delimiter is embedded in the first token.
	text := tokens[0].Text
	idx := strings.IndexAny(text, "★☆")
	if idx <= 0 {
		return ""
	}
	left := strings.TrimSpace(text[:idx])
	right := strings.TrimSpace(text[idx+len("★"):])
	if left == "" || right == "" {
		return ""
	}
	if !validLeft(left) {
		return ""
	}
	tokens[0].Text = right
	return left
}

var groupHyphenSpacePattern = regexp.MustCompile(`([A-Za-z0-9])\s*-\s*([A-Za-z0-9])`)

var groupCommaSpacePattern = regexp.MustCompile(`\s*[,，]\s*`)

var groupAmpSpacePattern = regexp.MustCompile(`\s*[&＆]\s*`)
