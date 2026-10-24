package parser

import (
	"strings"
	"unicode"

	"github.com/wangkayu/reltitle/pkg/model"
)

func hasTechAttr(attrs []model.Attribute) bool {
	for _, a := range attrs {
		s := string(a.Type)
		if s == string(model.AttrResolution) || s == string(model.AttrVideoCodec) || s == string(model.AttrAudioCodec) || s == string(model.AttrAudioChannels) || s == string(model.AttrAudioObject) || s == string(model.AttrAudioLang) || s == string(model.AttrSourceType) || s == string(model.AttrContainer) || s == string(model.AttrPlatform) || s == string(model.AttrCRC) || s == string(model.AttrTech) || s == string(model.AttrAudioFlag) || s == string(model.AttrSubFlag) || s == string(model.AttrSubLang) || s == string(model.AttrSubFormat) || s == string(model.AttrSubLangGroup) {
			return true
		}
	}
	return false
}

func firstTechTokenIndexFromAttrs(attrs []model.Attribute) (int, bool) {
	minIdx := 1 << 30
	for _, a := range attrs {
		s := string(a.Type)
		if s != string(model.AttrResolution) && s != string(model.AttrVideoCodec) && s != string(model.AttrAudioCodec) && s != string(model.AttrAudioChannels) && s != string(model.AttrAudioObject) && s != string(model.AttrAudioLang) && s != string(model.AttrSourceType) && s != string(model.AttrContainer) && s != string(model.AttrPlatform) && s != string(model.AttrCRC) && s != string(model.AttrTech) && s != string(model.AttrAudioFlag) && s != string(model.AttrSubFlag) && s != string(model.AttrSubLang) && s != string(model.AttrSubFormat) && s != string(model.AttrSubLangGroup) {
			continue
		}
		if a.TokenStart >= 0 && a.TokenStart < minIdx {
			minIdx = a.TokenStart
		}
	}
	if minIdx == 1<<30 {
		return -1, false
	}
	return minIdx, true
}

func aliasChainSemanticKey(s string) string {
	s = normalizeTitleMain(cleanAliasToken(strings.TrimSpace(s)))
	if s == "" {
		return ""
	}
	s = strings.ToLower(s)
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func aliasChainKeysContain(keys map[string]struct{}, key string) bool {
	if key == "" || len(keys) == 0 {
		return false
	}
	if _, ok := keys[key]; ok {
		return true
	}
	for k := range keys {
		if k == "" {
			continue
		}
		if k == key {
			return true
		}
		if len(k) >= 5 && len(key) >= 5 {
			if strings.Contains(k, key) || strings.Contains(key, k) {
				return true
			}
		}
	}
	return false
}

func finalTitleValueFromAttributes(attrs []model.Attribute, fallback string) string {
	for i := len(attrs) - 1; i >= 0; i-- {
		if attrs[i].Type != model.AttrTitle {
			continue
		}
		v := strings.TrimSpace(attrs[i].Normalized)
		if v != "" {
			return v
		}
	}
	return strings.TrimSpace(fallback)
}

func finalTitleAliasValuesFromAttributes(attrs []model.Attribute) []string {
	if len(attrs) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, 4)
	out := make([]string, 0, 4)
	for _, a := range attrs {
		if a.Type != model.AttrTitleAlias {
			continue
		}
		v := strings.TrimSpace(a.Normalized)
		if v == "" {
			continue
		}
		key := aliasSemanticKey(v)
		if key == "" {
			key = strings.ToLower(v)
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, v)
	}
	return out
}

func (p *Parser) consumeAliasChainTokensByFinalAliases(tokens []model.Token, groupSpan [2]int, title string, aliases []string, episodeIdx int, episodeRangeIdx int, seasonIdx int, attrs []model.Attribute) {
	if len(tokens) == 0 || strings.TrimSpace(title) == "" || len(aliases) == 0 {
		return
	}
	scanStart := titleStartAfterLeadingEmptyBracketBlocks(tokens)
	if len(groupSpan) == 2 && groupSpan[1] > scanStart {
		scanStart = groupSpan[1]
	}
	if scanStart < 0 {
		scanStart = 0
	}
	scanEnd := len(tokens)
	if episodeRangeIdx >= scanStart && episodeRangeIdx < scanEnd {
		scanEnd = episodeRangeIdx
	}
	if episodeIdx >= scanStart && episodeIdx < scanEnd {
		scanEnd = episodeIdx
	}
	if techStart, ok := firstTechTokenIndexFromAttrs(attrs); ok && techStart >= 0 && techStart < scanEnd {
		if techStart > scanStart {
			scanEnd = techStart
		}
	}
	if scanStart >= scanEnd {
		return
	}

	firstDelim := -1
	lastDelim := -1
	for i := scanStart; i < scanEnd; i++ {
		if tokens[i].Removed {
			continue
		}
		if isInBracket(tokens, i) || isInParen(tokens, i) {
			continue
		}
		if !isAliasChainDelimiterToken(tokens[i].Text) {
			continue
		}
		if firstDelim < 0 {
			firstDelim = i
		}
		lastDelim = i
	}
	if firstDelim < 0 || lastDelim < firstDelim {
		return
	}

	type seg struct {
		from int
		to   int
		key  string
	}
	segments := make([]seg, 0, 4)
	cur := scanStart
	for i := scanStart; i <= scanEnd; i++ {
		isSplit := i == scanEnd
		if !isSplit && !tokens[i].Removed && !isInBracket(tokens, i) && !isInParen(tokens, i) && isAliasChainDelimiterToken(tokens[i].Text) {
			isSplit = true
		}
		if !isSplit {
			continue
		}
		if cur < i {
			txt := extractTitleFromSpanAllowRemoved(tokens, cur, i, false)
			key := aliasChainSemanticKey(txt)
			if key != "" {
				segments = append(segments, seg{from: cur, to: i, key: key})
			}
		}
		cur = i + 1
	}
	if len(segments) < 2 {
		return
	}

	titleKey := aliasChainSemanticKey(title)
	if titleKey == "" {
		return
	}
	aliasKeys := make(map[string]struct{}, len(aliases))
	for _, a := range aliases {
		k := aliasChainSemanticKey(a)
		if k == "" {
			continue
		}
		aliasKeys[k] = struct{}{}
	}
	if len(aliasKeys) == 0 {
		return
	}

	titleSegIdx := -1
	titleScore := -1
	for i, s := range segments {
		score := -1
		if s.key == titleKey {
			score = 100 + len(titleKey)
		} else if len(s.key) >= 5 && len(titleKey) >= 5 {
			if strings.Contains(s.key, titleKey) || strings.Contains(titleKey, s.key) {
				score = len(s.key)
			}
		}
		if score > titleScore {
			titleScore = score
			titleSegIdx = i
		}
	}

	matched := false
	for i, s := range segments {
		if i == titleSegIdx {
			continue
		}
		if !aliasChainKeysContain(aliasKeys, s.key) {
			continue
		}
		for k := s.from; k < s.to && k < len(tokens); k++ {
			tokens[k].Removed = true
		}
		matched = true
	}
	if !matched {
		if titleSegIdx < 0 {
			return
		}
		titleSegKey := segments[titleSegIdx].key
		if titleSegKey == "" {
			return
		}
		otherSegExists := false
		for i, s := range segments {
			if i == titleSegIdx {
				continue
			}
			if s.key == "" || s.key == titleSegKey {
				continue
			}
			otherSegExists = true
			break
		}
		if !otherSegExists {
			return
		}
		// Fallback: delimiter-only cleanup for bilingual title chains where
		// aliases are not promoted but title is already finalized from one side.
		matched = true
	}
	for i := firstDelim; i <= lastDelim && i < len(tokens); i++ {
		if tokens[i].Removed {
			continue
		}
		if isAliasChainDelimiterToken(tokens[i].Text) {
			tokens[i].Removed = true
		}
	}
}

func (p *Parser) consumeTitleGapSeasonBridgeMarkers(tokens []model.Token, titleEnd int, episodeIdx int, episodeRangeIdx int, attrs []model.Attribute) {
	if len(tokens) == 0 || titleEnd < 0 || titleEnd >= len(tokens) {
		return
	}
	scanStart := titleEnd
	scanEnd := len(tokens)
	if episodeRangeIdx >= scanStart && episodeRangeIdx < scanEnd {
		scanEnd = episodeRangeIdx
	}
	if episodeIdx >= scanStart && episodeIdx < scanEnd {
		scanEnd = episodeIdx
	}
	if techStart, ok := firstTechTokenIndexFromAttrs(attrs); ok && techStart > scanStart && techStart < scanEnd {
		scanEnd = techStart
	}
	if scanStart >= scanEnd {
		return
	}

	for i := scanStart; i < scanEnd; i++ {
		if tokens[i].Removed {
			continue
		}
		txt := strings.TrimSpace(tokens[i].Text)
		if txt == "" {
			continue
		}
		if isSingleSlashSeasonSignalToken(txt) || isSingleSlashBridgeToken(txt) {
			tokens[i].Removed = true
		}
	}
}

func (p *Parser) consumeTitleGapLatinAliasRuns(tokens []model.Token, title string, aliases []string, titleEnd int, episodeIdx int, episodeRangeIdx int, attrs []model.Attribute) {
	if len(tokens) == 0 || titleEnd < 0 || titleEnd >= len(tokens) {
		return
	}
	if !hasCJK(title) || hasLatin(title) {
		return
	}
	aliasKeys := map[string]bool{}
	for _, a := range aliases {
		aa := strings.TrimSpace(a)
		if aa == "" || hasCJK(aa) || !hasLatin(aa) {
			continue
		}
		if key := aliasSemanticKey(aa); key != "" {
			aliasKeys[key] = true
		}
	}
	if len(aliasKeys) == 0 {
		return
	}

	scanStart := titleEnd
	scanEnd := len(tokens)
	if episodeRangeIdx >= scanStart && episodeRangeIdx < scanEnd {
		scanEnd = episodeRangeIdx
	}
	if episodeIdx >= scanStart && episodeIdx < scanEnd {
		scanEnd = episodeIdx
	}
	if techStart, ok := firstTechTokenIndexFromAttrs(attrs); ok && techStart > scanStart && techStart < scanEnd {
		scanEnd = techStart
	}
	if scanStart >= scanEnd {
		return
	}

	for i := scanStart; i < scanEnd; i++ {
		if tokens[i].Removed || !isTitleGapLatinAliasToken(tokens[i].Text) {
			continue
		}
		start := i
		for i < scanEnd && !tokens[i].Removed && isTitleGapLatinAliasToken(tokens[i].Text) {
			i++
		}
		stop := i
		if stop-start < 2 {
			continue
		}
		cand := cleanAliasToken(extractTitleFromSpanAllowRemoved(tokens, start, stop, false))
		if cand == "" {
			continue
		}
		if aliasKeys[aliasSemanticKey(cand)] {
			for k := start; k < stop && k < len(tokens); k++ {
				tokens[k].Removed = true
			}
		}
	}
}

func (p *Parser) consumeTitleGapAliasSemanticTokens(tokens []model.Token, aliases []string, titleEnd int, episodeIdx int, episodeRangeIdx int, attrs []model.Attribute) {
	if len(tokens) == 0 || len(aliases) == 0 || titleEnd < 0 || titleEnd >= len(tokens) {
		return
	}
	aliasKeys := map[string]bool{}
	for _, a := range aliases {
		k := aliasSemanticKey(a)
		if k != "" {
			aliasKeys[k] = true
		}
	}
	if len(aliasKeys) == 0 {
		return
	}

	scanStart := titleEnd
	scanEnd := len(tokens)
	if episodeRangeIdx >= scanStart && episodeRangeIdx < scanEnd {
		scanEnd = episodeRangeIdx
	}
	if episodeIdx >= scanStart && episodeIdx < scanEnd {
		scanEnd = episodeIdx
	}
	if techStart, ok := firstTechTokenIndexFromAttrs(attrs); ok && techStart > scanStart && techStart < scanEnd {
		scanEnd = techStart
	}
	if scanStart >= scanEnd {
		return
	}

	for i := scanStart; i < scanEnd; i++ {
		if tokens[i].Removed {
			continue
		}
		txt := strings.TrimSpace(tokens[i].Text)
		if txt == "" || isDashDelimiterToken(txt) || txt == "/" || txt == "／" {
			continue
		}
		switch txt {
		case "[", "]", "(", ")", "（", "）", "【", "】", "{", "}":
			continue
		}
		tk := aliasSemanticKey(txt)
		if tk == "" {
			continue
		}
		for ak := range aliasKeys {
			if tk == ak {
				tokens[i].Removed = true
				break
			}
			if strings.Contains(tk, ak) && hasEmbeddedSeasonMarkerToken(txt) {
				tokens[i].Removed = true
				break
			}
		}
	}
}

func (p *Parser) consumeTitleGapBatchTVMarkers(tokens []model.Token, titleEnd int, episodeIdx int, episodeRangeIdx int, attrs []model.Attribute) {
	if len(tokens) == 0 || titleEnd < 0 || titleEnd >= len(tokens) {
		return
	}
	// Scope to range-like outputs only; avoid touching regular TV title semantics.
	if episodeRangeIdx < 0 {
		return
	}
	scanStart := titleEnd
	scanEnd := len(tokens)
	if episodeRangeIdx >= scanStart && episodeRangeIdx < scanEnd {
		scanEnd = episodeRangeIdx
	}
	if episodeIdx >= scanStart && episodeIdx < scanEnd {
		scanEnd = episodeIdx
	}
	if techStart, ok := firstTechTokenIndexFromAttrs(attrs); ok && techStart > scanStart && techStart < scanEnd {
		scanEnd = techStart
	}
	if scanStart >= scanEnd {
		return
	}
	for i := scanStart; i < scanEnd; i++ {
		if tokens[i].Removed {
			continue
		}
		txt := strings.ToLower(strings.TrimSpace(tokens[i].Text))
		if txt != "tv" && txt != "tb" && txt != "тв" && txt != "тv" {
			continue
		}
		if isInBracket(tokens, i) || isInParen(tokens, i) || hasSeasonSignalAroundToken(tokens, i, 3) {
			tokens[i].Removed = true
		}
	}
}

func consumeTailGroupTokensByCanonicalGroup(tokens []model.Token, group string) {
	group = strings.TrimSpace(normalizeGroupText(group))
	if group == "" || len(tokens) == 0 {
		return
	}
	partsRaw := strings.Split(group, "-")
	parts := make([]string, 0, len(partsRaw))
	for _, p := range partsRaw {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		parts = append(parts, p)
	}
	if len(parts) == 0 {
		return
	}

	// Match split form near tail: "- Tsundere - Raws"
	if len(parts) >= 2 {
		last := parts[len(parts)-1]
		for i := len(tokens) - 1; i >= 0; i-- {
			if tokens[i].Removed || isInBracket(tokens, i) || isInParen(tokens, i) {
				continue
			}
			if !strings.EqualFold(strings.TrimSpace(tokens[i].Text), last) {
				continue
			}
			j := i - 1
			k := len(parts) - 2
			ok := true
			for k >= 0 {
				for j >= 0 && strings.TrimSpace(tokens[j].Text) == "" {
					j--
				}
				if j < 0 || strings.TrimSpace(tokens[j].Text) != "-" {
					ok = false
					break
				}
				j--
				for j >= 0 && strings.TrimSpace(tokens[j].Text) == "" {
					j--
				}
				if j < 0 || !strings.EqualFold(strings.TrimSpace(tokens[j].Text), parts[k]) {
					ok = false
					break
				}
				k--
				j--
			}
			if !ok {
				continue
			}
			start := j + 1
			leadDash := start - 1
			if leadDash >= 0 && strings.TrimSpace(tokens[leadDash].Text) == "-" && !isInBracket(tokens, leadDash) && !isInParen(tokens, leadDash) {
				tokens[leadDash].Removed = true
			}
			for x := start; x <= i && x < len(tokens); x++ {
				if isInBracket(tokens, x) || isInParen(tokens, x) {
					continue
				}
				tokens[x].Removed = true
			}
			return
		}
	}

	// Match single token near tail: "-VARYG" / "VARYG".
	for i := len(tokens) - 1; i >= 0; i-- {
		if tokens[i].Removed || isInBracket(tokens, i) || isInParen(tokens, i) {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(tokens[i].Text), group) {
			continue
		}
		tokens[i].Removed = true
		if i-1 >= 0 && strings.TrimSpace(tokens[i-1].Text) == "-" && !isInBracket(tokens, i-1) && !isInParen(tokens, i-1) {
			tokens[i-1].Removed = true
		}
		return
	}
}
