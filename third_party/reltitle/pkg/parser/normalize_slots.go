package parser

import (
	"regexp"
	"strings"

	"github.com/wangkayu/reltitle/pkg/model"
)

func extractPackSeasonExtrasAndRemoveFromTokens(tokens []model.Token, out *[]model.Attribute, seasonRange string) {
	if out == nil || len(tokens) == 0 {
		return
	}

	forcePack := strings.TrimSpace(seasonRange) != ""

	isExtraMarker := func(t string) (string, bool) {
		t = strings.TrimSpace(t)
		if t == "" {
			return "", false
		}
		m := packExtraMarkerPattern.FindStringSubmatch(t)
		if len(m) == 2 {
			return strings.ToUpper(strings.TrimSpace(m[1])), true
		}
		return "", false
	}

	for i := 0; i < len(tokens); i++ {
		if tokens[i].Removed {
			continue
		}
		open := tokens[i].Text
		close := ""
		if open == "[" {
			close = "]"
		} else if open == "【" {
			close = "】"
		} else {
			continue
		}

		depth := 0
		end := -1
		for j := i; j < len(tokens); j++ {
			if tokens[j].Removed {
				continue
			}
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

		// Decide whether this bracket block is a season pack marker block.
		hasSeasonToken := false
		for k := i + 1; k < end; k++ {
			if tokens[k].Removed {
				continue
			}
			if packSeasonTokenPattern.MatchString(strings.TrimSpace(tokens[k].Text)) {
				hasSeasonToken = true
				break
			}
		}
		isSeasonPackBlock := forcePack || hasSeasonToken
		if !isSeasonPackBlock {
			i = end
			continue
		}

		// Extract patterns like "+ OVA" / "+ MOVIE" inside the block.
		for k := i + 1; k < end; k++ {
			if tokens[k].Removed {
				continue
			}
			tk := strings.TrimSpace(tokens[k].Text)
			if tk == "" {
				continue
			}
			if tk == "+" || tk == "＋" {
				// Lookahead to the next meaningful token.
				n := -1
				for m := k + 1; m < end; m++ {
					if tokens[m].Removed {
						continue
					}
					mt := strings.TrimSpace(tokens[m].Text)
					if mt == "" {
						continue
					}
					n = m
					break
				}
				if n < 0 {
					continue
				}
				if norm, ok := isExtraMarker(tokens[n].Text); ok {
					*out = append(*out, model.Attribute{Type: model.AttrContents, Normalized: norm, TokenStart: k, TokenEnd: n + 1, Confidence: 0.85, Source: model.AttrSourcePackExtras})
					tokens[k].Removed = true
					tokens[n].Removed = true
					k = n
				}
				continue
			}
			// Also support combined tokens like "+OVA".
			if norm, ok := isExtraMarker(tk); ok {
				// If previous token is '+', remove it as well.
				if k-1 >= i+1 {
					prev := strings.TrimSpace(tokens[k-1].Text)
					if prev == "+" || prev == "＋" {
						tokens[k-1].Removed = true
					}
				}
				*out = append(*out, model.Attribute{Type: model.AttrContents, Normalized: norm, TokenStart: k, TokenEnd: k + 1, Confidence: 0.85, Source: model.AttrSourcePackExtras})
				tokens[k].Removed = true
				continue
			}
		}
		i = end
	}

	// Also handle non-bracket patterns like: "Title S01 + OVA 1080p".
	for i := 0; i < len(tokens); i++ {
		if tokens[i].Removed {
			continue
		}
		if isInBracket(tokens, i) || isInParen(tokens, i) {
			continue
		}
		if !packSeasonTokenPattern.MatchString(strings.TrimSpace(tokens[i].Text)) {
			continue
		}
		// Lookahead a small window for "+ <extra>".
		for k := i + 1; k < len(tokens) && k <= i+8; k++ {
			if tokens[k].Removed {
				continue
			}
			if isInBracket(tokens, k) || isInParen(tokens, k) {
				continue
			}
			tk := strings.TrimSpace(tokens[k].Text)
			if tk != "+" && tk != "＋" {
				continue
			}
			// Find next meaningful token.
			n := -1
			for m := k + 1; m < len(tokens) && m <= i+10; m++ {
				if tokens[m].Removed {
					continue
				}
				if isInBracket(tokens, m) || isInParen(tokens, m) {
					continue
				}
				mt := strings.TrimSpace(tokens[m].Text)
				if mt == "" {
					continue
				}
				n = m
				break
			}
			if n < 0 {
				continue
			}
			if norm, ok := isExtraMarker(tokens[n].Text); ok {
				*out = append(*out, model.Attribute{Type: model.AttrContents, Normalized: norm, TokenStart: k, TokenEnd: n + 1, Confidence: 0.85, Source: model.AttrSourcePackExtras})
				tokens[k].Removed = true
				tokens[n].Removed = true
				k = n
			}
		}
	}
}

func (p *Parser) normalizePlatformAttrs(attrs []model.Attribute) {
	for i := range attrs {
		if attrs[i].Type != model.AttrPlatform {
			continue
		}
		attrs[i].Normalized = p.normalizePlatformCode(attrs[i].Normalized)
	}
}

func classifyAndEmitTailMetaNote(part string, attrs *[]model.Attribute) bool {
	if attrs == nil {
		return false
	}
	p := strings.TrimSpace(part)
	if p == "" {
		return false
	}
	low := strings.ToLower(p)
	note := ""
	switch {
	case low == "weekly":
		note = "WEEKLY"
	case low == "batch":
		note = "BATCH"
	case low == "reseed":
		note = "RESEED"
	case low == "complete" || low == "fin" || low == "end":
		note = "COMPLETE"
	case strings.HasPrefix(low, "final season"):
		note = "FINAL_SEASON"
	}
	if note == "" {
		return false
	}
	*attrs = append(*attrs, model.Attribute{Type: model.AttrNote, Normalized: note, Confidence: 0.6, Source: model.AttrSourceTailMeta})
	return true
}

func (p *Parser) normalizeSourceTypeAttrs(attrs []model.Attribute) {
	for i := range attrs {
		if attrs[i].Type != model.AttrSourceType {
			continue
		}
		attrs[i].Normalized = p.normalizeSourceType(attrs[i].Normalized)
	}
}

func normalizeSubLangToken(v string) string {
	s := strings.ToUpper(strings.TrimSpace(v))
	switch s {
	case "CHS", "ZH-HANS", "ZH_CN", "ZH-CN", "SC":
		return "SC"
	case "CHT", "ZH-HANT", "ZH_TW", "ZH-TW", "BIG5", "TC":
		return "TC"
	case "ZH", "CHI", "ZHO":
		return "ZH"
	case "CHS+JPN", "JPN+CHS", "JPSC":
		return "JPSC"
	case "CHT+JPN", "JPN+CHT", "JPTC":
		return "JPTC"
	case "RAW", "NOSUB", "NO_SUB", "NO-SUB":
		return "RAW"
	default:
		return s
	}
}

func normalizeSubLangGroupToken(v string) string {
	s := strings.ToUpper(strings.TrimSpace(v))
	switch s {
	case "CHS+JPN", "JPN+CHS", "JPSC":
		return "JPSC"
	case "CHT+JPN", "JPN+CHT", "JPTC":
		return "JPTC"
	default:
		s = strings.ReplaceAll(s, "CHS", "SC")
		s = strings.ReplaceAll(s, "CHT", "TC")
		return s
	}
}

func normalizeSubFlagToken(v string) (string, bool, bool) {
	s := strings.ToUpper(strings.TrimSpace(v))
	switch s {
	case "内嵌", "內嵌", "HARDSUB", "HARD":
		return "hard", false, false
	case "DUAL_SUBS", "MULTI_SUBS", "MULTI":
		return "soft", false, true
	case "BILINGUAL", "双语", "雙語":
		return "", false, true
	case "内封", "內封", "内挂", "內掛", "SOFTSUB", "SOFT", "SUBS", "SUBBED", "VOSTFR", "VOSTEN", "VF", "ENGSUB", "FANSUB", "字幕":
		return "soft", false, false
	case "外挂", "外掛", "EX", "EXTERNAL", "EXTERNAL_SUBS":
		return "ex", false, false
	case "RAW", "NOSUB", "NO-SUB", "NO_SUB":
		return "", true, false
	default:
		return "", false, false
	}
}

func normalizeAudioLangToken(v string) string {
	s := strings.ToUpper(strings.TrimSpace(v))
	switch s {
	case "CHN", "CMN", "MANDARIN", "PUTONGHUA", "GUOYU", "ZH", "ZH-CN", "ZH_CN":
		return "CMN"
	default:
		return s
	}
}

func (p *Parser) normalizeAudioAttrs(attrs []model.Attribute) {
	for i := range attrs {
		switch attrs[i].Type {
		case model.AttrAudioLang:
			attrs[i].Normalized = normalizeAudioLangToken(attrs[i].Normalized)
		case model.AttrAudioCodec:
			if v, ok := p.normalizeAudioCodecToken(attrs[i].Normalized); ok {
				attrs[i].Normalized = v
			}
		case model.AttrAudioFlag:
			if v, ok := p.normalizeAudioFlagToken(attrs[i].Normalized); ok {
				attrs[i].Normalized = v
			}
		}
	}
}

func normalizeAudioSlotLanguages(slot *model.AudioSlot) {
	if slot == nil || len(slot.Tracks) == 0 {
		return
	}
	for i := range slot.Tracks {
		slot.Tracks[i].Language = normalizeAudioLangToken(slot.Tracks[i].Language)
	}
}

func subtitleSpanText(tokens []model.Token, a model.Attribute) string {
	if len(tokens) == 0 || a.TokenStart < 0 || a.TokenEnd <= a.TokenStart || a.TokenEnd > len(tokens) {
		return ""
	}
	parts := make([]string, 0, a.TokenEnd-a.TokenStart)
	for i := a.TokenStart; i < a.TokenEnd; i++ {
		t := strings.TrimSpace(tokens[i].Text)
		if t == "" {
			continue
		}
		parts = append(parts, t)
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}

func shouldUseGenericZHFromSpan(span string) bool {
	s := strings.TrimSpace(span)
	if s == "" {
		return false
	}
	// Must contain generic subtitle markers.
	hasGeneric := strings.Contains(s, "中字") || strings.Contains(s, "字幕") || strings.Contains(strings.ToUpper(s), "SUB")
	if !hasGeneric {
		return false
	}
	// Explicit script markers should keep SC/TC precision.
	up := strings.ToUpper(s)
	if strings.Contains(s, "简") || strings.Contains(s, "簡") || strings.Contains(s, "繁") || strings.Contains(up, "CHS") || strings.Contains(up, "CHT") || strings.Contains(up, "SC") || strings.Contains(up, "TC") || strings.Contains(up, "BIG5") || strings.Contains(up, "GB") || strings.Contains(up, "HANS") || strings.Contains(up, "HANT") {
		return false
	}
	return true
}

func (p *Parser) normalizeSubtitleAttrs(attrs []model.Attribute, tokens []model.Token) []model.Attribute {
	if len(attrs) == 0 {
		return attrs
	}
	out := make([]model.Attribute, 0, len(attrs))
	langSet := map[string]bool{}
	groupSet := map[string]bool{}
	flagSet := map[string]bool{}
	rawMode := false
	multiHint := false
	for _, a := range attrs {
		switch a.Type {
		case model.AttrSubLang:
			n := normalizeSubLangToken(a.Normalized)
			if n == "" {
				continue
			}
			if (n == "SC" || n == "TC") && shouldUseGenericZHFromSpan(subtitleSpanText(tokens, a)) {
				n = "ZH"
			}
			if n == "RAW" {
				rawMode = true
			}
			langSet[n] = true
		case model.AttrSubLangGroup:
			n := normalizeSubLangGroupToken(a.Normalized)
			if n == "" {
				continue
			}
			groupSet[n] = true
		case model.AttrSubFlag:
			n, isRaw, isMulti := normalizeSubFlagToken(a.Normalized)
			if isRaw {
				rawMode = true
				continue
			}
			if isMulti {
				multiHint = true
			}
			if n == "" {
				continue
			}
			flagSet[n] = true
		default:
			out = append(out, a)
		}
	}

	if groupSet["JPSC"] {
		langSet["JPSC"] = true
		delete(langSet, "SC")
		delete(langSet, "JPN")
	}
	if groupSet["JPTC"] {
		langSet["JPTC"] = true
		delete(langSet, "TC")
		delete(langSet, "JPN")
	}
	if groupSet["JPSC"] && groupSet["JPTC"] {
		delete(langSet, "SC")
		delete(langSet, "TC")
		delete(langSet, "JPN")
	}
	if rawMode {
		langSet = map[string]bool{"RAW": true}
		groupSet = map[string]bool{}
		flagSet = map[string]bool{}
	} else if len(langSet) == 0 && len(groupSet) == 0 && multiHint {
		langSet["MULTI"] = true
	}
	if len(langSet) > 1 {
		delete(langSet, "MULTI")
	}

	langOrder := []string{"RAW", "MULTI", "ZH", "SC", "TC", "JPSC", "JPTC", "JPN", "ENG", "KOR", "FRA", "SPA", "CAT", "RUS", "DEU", "ITA", "POR", "ARA", "IND", "THA", "POL", "MSA", "VIE"}
	for _, v := range langOrder {
		if !langSet[v] {
			continue
		}
		out = append(out, model.Attribute{
			Type:       model.AttrSubLang,
			Normalized: v,
			TokenStart: -1,
			TokenEnd:   -1,
			Confidence: 0.8,
			Source:     model.AttrSourceSubtitleNormalize,
		})
	}
	groupOrder := []string{"JPSC", "JPTC", "SC+ENG", "TC+ENG"}
	for _, v := range groupOrder {
		if !groupSet[v] || rawMode {
			continue
		}
		out = append(out, model.Attribute{
			Type:       model.AttrSubLangGroup,
			Normalized: v,
			TokenStart: -1,
			TokenEnd:   -1,
			Confidence: 0.75,
			Source:     model.AttrSourceSubtitleNormalize,
		})
	}
	flagOrder := []string{"hard", "soft", "ex"}
	for _, v := range flagOrder {
		if !flagSet[v] || rawMode {
			continue
		}
		out = append(out, model.Attribute{
			Type:       model.AttrSubFlag,
			Normalized: v,
			TokenStart: -1,
			TokenEnd:   -1,
			Confidence: 0.8,
			Source:     model.AttrSourceSubtitleNormalize,
		})
	}
	return out
}

func (p *Parser) enrichBatchSourceTypeFromPackKind(attrs []model.Attribute, contentKind model.ContentKind) []model.Attribute {
	if contentKind.Canonical() != model.ContentKindBatch {
		return attrs
	}
	hasSource := false
	packKind := ""
	for _, a := range attrs {
		if a.Type == model.AttrSourceType && strings.TrimSpace(a.Normalized) != "" {
			hasSource = true
			break
		}
		if packKind == "" && a.Type == model.AttrPackKind && strings.TrimSpace(a.Normalized) != "" {
			packKind = strings.ToLower(strings.TrimSpace(a.Normalized))
		}
	}
	if hasSource || packKind == "" {
		return attrs
	}
	sourceType := ""
	if p != nil && len(p.packKindSourceType) > 0 {
		sourceType = strings.TrimSpace(p.packKindSourceType[packKind])
	}
	if sourceType == "" {
		return attrs
	}
	out := make([]model.Attribute, 0, len(attrs)+1)
	out = append(out, attrs...)
	out = append(out, model.Attribute{
		Type:       model.AttrSourceType,
		Normalized: sourceType,
		TokenStart: -1,
		TokenEnd:   -1,
		Confidence: 0.65,
		Source:     model.AttrSourcePackKindFallback,
	})
	return out
}

func (p *Parser) normalizeSourceType(s string) string {
	ss := strings.TrimSpace(s)
	if ss == "" {
		return ""
	}
	if p != nil && len(p.sourceTypeCanonical) > 0 {
		if v, ok := p.sourceTypeCanonical[strings.ToLower(ss)]; ok {
			return v
		}
	}
	return ss
}

func (p *Parser) normalizeVideoCodecToken(s string) (string, bool) {
	ss := strings.TrimSpace(strings.ToLower(s))
	if ss == "" {
		return "", false
	}
	if p != nil && len(p.videoCodecCanonical) > 0 {
		if v, ok := p.videoCodecCanonical[ss]; ok {
			return v, true
		}
		dotless := strings.ReplaceAll(ss, ".", "")
		if v, ok := p.videoCodecCanonical[dotless]; ok {
			return v, true
		}
	}
	return "", false
}

func (p *Parser) normalizeAudioCodecToken(s string) (string, bool) {
	ss := strings.TrimSpace(strings.ToLower(s))
	if ss == "" {
		return "", false
	}
	if p != nil && len(p.audioCodecCanonical) > 0 {
		if v, ok := p.audioCodecCanonical[ss]; ok {
			return v, true
		}
		dashless := strings.ReplaceAll(ss, "-", "")
		if v, ok := p.audioCodecCanonical[dashless]; ok {
			return v, true
		}
	}
	return "", false
}

func (p *Parser) normalizeContainerToken(s string) (string, bool) {
	ss := strings.TrimSpace(strings.ToLower(s))
	if ss == "" || p == nil || len(p.containerCanonical) == 0 {
		return "", false
	}
	if v, ok := p.containerCanonical[ss]; ok {
		return v, true
	}
	return "", false
}

func (p *Parser) normalizeAudioFlagToken(s string) (string, bool) {
	ss := strings.TrimSpace(strings.ToLower(s))
	if ss == "" || p == nil || len(p.audioFlagCanonical) == 0 {
		return "", false
	}
	if v, ok := p.audioFlagCanonical[ss]; ok {
		return v, true
	}
	return "", false
}

func (p *Parser) isAliasMetaOnlyToken(s string) bool {
	ss := strings.ToLower(strings.TrimSpace(s))
	if ss == "" {
		return false
	}
	switch ss {
	case "tv", "audio", "sub", "subs", "srt", "ass", "pgs", "mkv", "mp4", "avi":
		return true
	}
	if reAliasResolutionToken.MatchString(ss) ||
		reAliasResolutionMetaPhrase.MatchString(ss) ||
		reAliasSourceResolutionPhrase.MatchString(ss) ||
		reAliasPlatformResolutionPhrase.MatchString(ss) ||
		reAliasSourceMetaPhrase.MatchString(ss) ||
		reAliasSubtitleMetaPhrase.MatchString(ss) ||
		reAliasBitDepthMetaPhrase.MatchString(ss) ||
		reAliasAudioCountMetaPhrase.MatchString(ss) ||
		reAliasSubtitleCJKMetaPhrase.MatchString(strings.TrimSpace(s)) {
		return true
	}
	if p == nil {
		return false
	}
	if _, ok := p.platformCanonical[ss]; ok {
		return true
	}
	if _, ok := p.sourceTypeCanonical[ss]; ok {
		return true
	}
	if _, ok := p.normalizeVideoCodecToken(ss); ok {
		return true
	}
	if _, ok := p.normalizeAudioCodecToken(ss); ok {
		return true
	}
	if _, ok := p.normalizeContainerToken(ss); ok {
		return true
	}
	if _, ok := p.normalizeAudioFlagToken(ss); ok {
		return true
	}
	if _, ok := p.subFlagCanonical[ss]; ok {
		return true
	}
	if _, ok := p.subLangCanonical[ss]; ok {
		return true
	}
	if _, ok := p.audioLangCanonical[ss]; ok {
		return true
	}
	return false
}

func (p *Parser) isAliasUploaderAtPlatform(s string) bool {
	ss := strings.TrimSpace(s)
	if ss == "" {
		return false
	}
	parts := strings.Split(ss, "@")
	if len(parts) != 2 {
		return false
	}
	uploader := strings.TrimSpace(parts[0])
	platform := strings.ToLower(strings.TrimSpace(parts[1]))
	if !reAliasUploaderName.MatchString(uploader) {
		return false
	}
	if platform == "" {
		return false
	}
	if strings.HasPrefix(platform, "b-global") || strings.HasPrefix(platform, "bglobal") {
		return true
	}
	if p == nil {
		return false
	}
	_, ok := p.platformCanonical[platform]
	return ok
}

func (p *Parser) normalizePlatformCode(s string) string {
	ss := strings.TrimSpace(s)
	if ss == "" {
		return ""
	}
	if p != nil && len(p.platformCanonical) > 0 {
		if v, ok := p.platformCanonical[strings.ToLower(ss)]; ok {
			return v
		}
	}
	return strings.ToUpper(ss)
}

func convertRegionAttrsToPlatformRegion(attrs []model.Attribute) {
	for i := range attrs {
		if attrs[i].Type != model.AttrType("region") {
			continue
		}
		attrs[i].Type = model.AttrPlatformRegion
	}
}

func detectTailParenBlock(tokens []model.Token) string {
	if len(tokens) == 0 {
		return ""
	}
	last := -1
	for i := len(tokens) - 1; i >= 0; i-- {
		if tokens[i].Removed {
			continue
		}
		t := strings.TrimSpace(tokens[i].Text)
		if t == "" {
			continue
		}
		last = i
		break
	}
	if last < 0 {
		return ""
	}
	close := strings.TrimSpace(tokens[last].Text)
	var openTok string
	if close == ")" {
		openTok = "("
	} else if close == "）" {
		openTok = "（"
	} else {
		return ""
	}
	depth := 0
	openIdx := -1
	for i := last; i >= 0; i-- {
		if tokens[i].Removed {
			continue
		}
		t := strings.TrimSpace(tokens[i].Text)
		if t == "" {
			continue
		}
		if t == close {
			depth++
			continue
		}
		if t == openTok {
			depth--
			if depth == 0 {
				openIdx = i
				break
			}
		}
	}
	if openIdx < 0 {
		return ""
	}
	end := last
	if end <= openIdx {
		return ""
	}
	parts := make([]string, 0, end-openIdx)
	for k := openIdx + 1; k < end; k++ {
		t := strings.TrimSpace(tokens[k].Text)
		if t == "" {
			continue
		}
		parts = append(parts, t)
	}
	inner := strings.TrimSpace(strings.Join(parts, " "))
	if inner == "" {
		return ""
	}
	return inner
}

func hasAttrType(attrs []model.Attribute, t model.AttrType) bool {
	for _, a := range attrs {
		if a.Type == t {
			return true
		}
	}
	return false
}

func hasAttrNormalized(attrs []model.Attribute, t model.AttrType, normalized string) bool {
	want := strings.TrimSpace(normalized)
	if want == "" {
		return false
	}
	for _, a := range attrs {
		if a.Type != t {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(a.Normalized), want) {
			return true
		}
	}
	return false
}

func isStreamingRelease(tokens []model.Token, attrs []model.Attribute) bool {
	if !hasAttrType(attrs, model.AttrPlatform) {
		return false
	}
	for _, a := range attrs {
		if a.Type != model.AttrSourceType {
			continue
		}
		n := strings.ToUpper(strings.TrimSpace(a.Normalized))
		if n == "WEB-DL" || n == "WEBRIP" || n == "WEB-RIP" {
			return true
		}
	}
	return false
}

var reStreamMetaFlag = regexp.MustCompile(`(?i)\b(?:multi[-_ ]?subs?|multisub|dual[-_ ]?audio|multi[-_ ]?audio|english\s*dub|dub|vostfr|vosten|vf)\b`)

var reTailMetaToken = regexp.MustCompile(`(?i)^(?:weekly|batch|reseed|complete|fin|end|final\s+season|season|season\s*\d{1,2}|s\d{1,2}|\d{1,2}(?:st|nd|rd|th))$`)

var reHintPrefix = regexp.MustCompile(`(?i)^(检索用|检索|檢索用|檢索|索引|搜索用|搜尋用|搜索|搜尋|search)\s*[:：]\s*(.+)$`)
var reSearchHintKeyword = regexp.MustCompile(`(?i)(?:检索|檢索|索引|搜索|搜尋|search|网盘|網盤|下載|下载|download|招募|招校对|附\d+|下载地址|下載地址)`)
var reAliasMetaSemanticOnly = regexp.MustCompile(`(?i)^(?:全集|合集|特典(?:映像)?|正片(?:\s*[+＋]\s*特典(?:映像)?)?|第[一二三四五六七八九十0-9]+季|season\s*\d{1,2}|s\d{1,2}|movie|movies|specials?|sp(?:é|e)ciaux|tv\s*全集|part|cour)$`)

var reAliasTrimTVSeasonTail = regexp.MustCompile(`(?i)(?:^|\s+)(?:tv|tb|тv|тв)\s*[-_ ]\s*\d{1,2}\s*$`)
var reAliasTrimPlatformTail = regexp.MustCompile(`(?i)\s+(?:b-?global(?:\s+donghua)?|baha|abema|cr|amzn|amazon|netflix|nf|unext|at-?x|bs11|bs-?ntv|bs-?fuji|france\s*tv|tv)\s*$`)
var reAliasTrimTVAnimePrefix = regexp.MustCompile(`(?i)^(?:tv\s*アニメ|tv\s*anime|tv\s*animation)\s+`)
var reAliasTrimSeasonWordTail = regexp.MustCompile(`(?i)\s+(?:season|saison)\s*$`)
var reAliasTrimEpisodeWordTail = regexp.MustCompile(`(?i)\s+(?:ep|episode)\s*$`)
var reAliasTVContentPhrase = regexp.MustCompile(`(?i)\btv\b.*(?:season|series|ver(?:sion)?|reproduction|release|ova|oad|ona|movie|sp|s\d{1,2}|剪辑版|剪輯版|版|系列)`)
var reAliasMovieIndexSingle = regexp.MustCompile(`(?i)^m\d{1,3}$`)
var reAliasResolutionToken = regexp.MustCompile(`(?i)^(?:\d{3,4}p|[248]k|fhd|uhd|hd|sd|\d{3,4}\s*[x×]\s*\d{3,4}(?:[ip])?)$`)
var reAliasResolutionMetaPhrase = regexp.MustCompile(`(?i)^(?:[248]k|fhd|uhd|hd|sd)(?:\s*(?:ver(?:sion)?|reproduction|restore(?:d)?|remaster(?:ed)?|upscal(?:e|ed)|收藏级画质|收藏級畫質|修复版|修復版|畫質|画质))?$`)
var reAliasSourceResolutionPhrase = regexp.MustCompile(`(?i)^(?:ld|jdvd|dvd|bd|bdrip|webrip|web[- ]?dl)\s+(?:\d{3,4}p|\d{3,4}\s*[x×]\s*\d{3,4}(?:[ip])?)$`)
var reAliasPlatformResolutionPhrase = regexp.MustCompile(`(?i)^(?:[a-z0-9][a-z0-9._-]*)(?:\s+[a-z]{2,4})?\s+\d{3,4}\s*[x×]\s*\d{3,4}(?:[ip])?(?:\s+(?:avc|hevc|mpeg2|x26[45]|h\.?26[45]|aac|ac3|e-?ac-?3|ddp|flac|opus|mp3))?$`)
var reAliasSourceMetaPhrase = regexp.MustCompile(`(?i)^(?:b[-_ ]?)?(?:j[-_ ]?dvd|dvd|bd|bdrip|bluray|blu[-_ ]?ray|webrip|web[-_ ]?dl)(?:\s+raw)?$`)
var reAliasSubtitleMetaPhrase = regexp.MustCompile(`(?i)^(?:srt|ass|pgs)(?:\b|[\s\-_/+].*)$`)
var reAliasBitDepthMetaPhrase = regexp.MustCompile(`(?i)^(?:ma(?:8|10|12)p|(?:8|10|12)\s*[-_ ]?bit)$`)
var reAliasAudioCountMetaPhrase = regexp.MustCompile(`(?i)^(?:\d+\s*audio|audio\s*\d+)$`)
var reAliasSubtitleCJKMetaPhrase = regexp.MustCompile(`(?i)^(?:[\p{Han}A-Za-z0-9&+／/\-_\s]*?(?:双语|雙語|中字|字幕|內封|内封|內嵌|内嵌|外挂|外掛))$`)
var reAliasTrailingEpisodeTail = regexp.MustCompile(`(?i)^(.*?)(?:\s*[-:：]\s*0*(\d{1,4}))$`)
var reTitleTrimTVSeasonTail = regexp.MustCompile(`(?i)\s+(?:tv|tb|тv|тв)\s*[-_ ]?\s*\d{1,3}\s*$`)
var reTitleTrimTVTail = regexp.MustCompile(`(?i)\s+(?:tv|tb|тv|тв)\s*$`)
var reTitleTrimTVSeriesTail = regexp.MustCompile(`(?i)\s+tv\s+series\s*$`)
var reTitleTrimTVEpisodeListTail = regexp.MustCompile(`(?i)\s+tv\s+[\d,，\-~\s]{2,}\s*$`)
var reTitleTrimSpecialTail = regexp.MustCompile(`(?i)\s+(?:ncop|nced|pv)(?:\d{1,3})?\s*$`)
var reTitleStripTVAnimeSegmentPrefix = regexp.MustCompile(`(?i)(^|/\s*)tv\s*(?:アニメ|anime|animation)\s+`)
var reTitleTrimEpisodeWordTail = regexp.MustCompile(`(?i)\s+(?:ep|episode)\s*$`)
var reTitleTrimPackSpecialTail = regexp.MustCompile(`(?i)\s+pack(?:\s*\+\s*(?:sp[eé]ciaux|specials?|sp|ova|oad|ona|ex))+(?:\s+(?:web|webrip|web[- ]?dl))?\s*$`)
var reTitleTrimPlainPackTail = regexp.MustCompile(`(?i)\s+pack\s*$`)
var reTitleTrimMetaTail = regexp.MustCompile(`(?i)\s+(?:hd|sd|fhd|uhd|retail|webrip|web[- ]?dl|bdrip|bluray|blu-ray|remux|dvd|hdtv|mkv|mp4|avi|x26[45]|h\.?26[45]|hevc|av1|aac|ac3|e-?ac-?3|flac|dts(?:-?hd|-?hdma)?|hdma|truehd|ma\d{1,2}p|10[- ]?bit|8[- ]?bit)\s*$`)
var reTitleTrimVolTail = regexp.MustCompile(`(?i)\s+(?:vol\.?(?:\s*\d{1,3})?|volume\s*\d{1,3})\s*$`)
var reTitleTrimDVDBundleTail = regexp.MustCompile(`(?i)\s+(?:dvd(?:\s*[- ]?\s*isos?)?|dvd\s*set)\s*$`)
var reTitleTrimDVDEpisodesTail = regexp.MustCompile(`(?i)\s+(?:(?:r\d+\s+)?dvd\s+)?episodes?\s*$`)
var reAliasTechSingle = regexp.MustCompile(`(?i)^(?:aac(?:×\d+)?|ac3|e-?ac-?3|ddp|flac|opus|mp3|dts(?:-?hd|-?hdma)?|truehd|atmos|avc|hevc|x26[45]|h\.?26[45]|mkv|mp4|dual)$`)
var reAliasTechList = regexp.MustCompile(`(?i)^(?:\.?\s*)?(?:aac(?:×\d+)?|ac3|e-?ac-?3|ddp|flac|opus|mp3|dts(?:-?hd|-?hdma)?|truehd|atmos|avc|hevc|x26[45]|h\.?26[45]|mkv|mp4|dual)(?:\s*[,/&+]\s*(?:aac(?:×\d+)?|ac3|e-?ac-?3|ddp|flac|opus|mp3|dts(?:-?hd|-?hdma)?|truehd|atmos|avc|hevc|x26[45]|h\.?26[45]|mkv|mp4|dual))+\.?$`)
var reAliasUploaderName = regexp.MustCompile(`(?i)^[a-z0-9._-]{2,40}$`)

func normalizeStreamMetaText(s string) string {
	ss := strings.TrimSpace(s)
	if ss == "" {
		return ""
	}
	// Collapse tokenization artifacts like "Multi - Audio" => "Multi-Audio".
	ss = strings.ReplaceAll(ss, " - ", "-")
	ss = strings.ReplaceAll(ss, " _ ", "_")
	// Collapse repeated whitespace.
	ss = reNormalizeStreamMetaText1.ReplaceAllString(ss, " ")
	return strings.TrimSpace(ss)
}

func isStreamMetaText(s string) bool {
	ss := normalizeStreamMetaText(s)
	if ss == "" {
		return false
	}
	return reStreamMetaFlag.MatchString(ss)
}

func classifyAndEmitStreamMeta(part string, attrs *[]model.Attribute) {
	if attrs == nil {
		return
	}
	p := normalizeStreamMetaText(part)
	if p == "" {
		return
	}
	up := strings.ToUpper(p)
	if strings.Contains(up, "SUB") {
		if strings.Contains(up, "MULTI") || strings.Contains(up, "BILINGUAL") {
			*attrs = append(*attrs, model.Attribute{Type: model.AttrSubLang, Normalized: "MULTI", Confidence: 0.6, Source: model.AttrSourceRouteStreamingReleaseV1})
		}
		*attrs = append(*attrs, model.Attribute{Type: model.AttrSubFlag, Normalized: "soft", Confidence: 0.6, Source: model.AttrSourceRouteStreamingReleaseV1})
	}
	if strings.Contains(up, "DUB") {
		if strings.Contains(up, "ENGLISH") {
			*attrs = append(*attrs, model.Attribute{Type: model.AttrAudioFlag, Normalized: "ENGLISH_DUB", Confidence: 0.6, Source: model.AttrSourceRouteStreamingReleaseV1})
		} else {
			*attrs = append(*attrs, model.Attribute{Type: model.AttrAudioFlag, Normalized: "DUB", Confidence: 0.6, Source: model.AttrSourceRouteStreamingReleaseV1})
		}
		return
	}
	if strings.Contains(up, "AUDIO") {
		if strings.Contains(up, "DUAL") {
			*attrs = append(*attrs, model.Attribute{Type: model.AttrAudioFlag, Normalized: "DUAL_AUDIO", Confidence: 0.6, Source: model.AttrSourceRouteStreamingReleaseV1})
			return
		}
		if strings.Contains(up, "MULTI") {
			*attrs = append(*attrs, model.Attribute{Type: model.AttrAudioFlag, Normalized: "MULTI_AUDIO", Confidence: 0.6, Source: model.AttrSourceRouteStreamingReleaseV1})
			return
		}
		*attrs = append(*attrs, model.Attribute{Type: model.AttrAudioFlag, Normalized: "AUDIO", Confidence: 0.6, Source: model.AttrSourceRouteStreamingReleaseV1})
	}
}

func filterOutStreamMetaAliases(aliases []string) []string {
	if len(aliases) == 0 {
		return nil
	}
	out := make([]string, 0, len(aliases))
	for _, a := range aliases {
		if isStreamMetaText(a) {
			continue
		}
		if reTailMetaToken.MatchString(strings.TrimSpace(a)) {
			continue
		}
		// Catch tokenization artifacts like "- Subs" / "Dual - Audio".
		aa := strings.TrimSpace(strings.ReplaceAll(a, " ", ""))
		if strings.EqualFold(aa, "-subs") || strings.EqualFold(aa, "-audio") {
			continue
		}
		out = append(out, a)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (p *Parser) applyStreamingReleaseRouteV1(tailParenBlock string, aliases *[]string, attrs *[]model.Attribute) {
	if aliases == nil {
		return
	}
	*aliases = filterOutStreamMetaAliases(*aliases)

	block := strings.TrimSpace(tailParenBlock)
	if block == "" {
		return
	}
	// Split by common delimiters (including fullwidth variants).
	spl := reApplyStreamingReleaseRouteV11.Split(block, -1)
	aliasCands := make([]string, 0, len(spl))
	for _, it := range spl {
		it = strings.TrimSpace(it)
		if it == "" {
			continue
		}
		if isStreamMetaText(it) {
			classifyAndEmitStreamMeta(it, attrs)
			continue
		}
		if reTailMetaToken.MatchString(it) {
			continue
		}
		it = cleanAliasToken(it)
		if !p.isValidAliasToken(it) {
			continue
		}
		aliasCands = append(aliasCands, it)
	}
	if len(aliasCands) == 0 {
		return
	}
	*aliases = mergeAliases(*aliases, aliasCands)
}

var reSxxExxToken = regexp.MustCompile(`(?i)^s\d{1,2}e\d{1,4}$`)

func hasSxxExxInTokens(tokens []model.Token) bool {
	for i := 0; i < len(tokens); i++ {
		t := strings.TrimSpace(tokens[i].Text)
		if t == "" {
			continue
		}
		if reSxxExxToken.MatchString(t) {
			return true
		}
	}
	return false
}

func stripSeasonOrPartSuffix(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	// Guard: numeric ranges like "17-26" are often part of title/alias (e.g. SweetSub releases),
	// not a season/part suffix.
	if reStripSeasonOrPartSuffix1.FindStringIndex(s) != nil {
		return ""
	}
	if !looksLikeSeasonOrPartSuffix(s) {
		return ""
	}
	// Remove explicit season expressions.
	out := s
	out = reStripSeasonOrPartSuffix2.ReplaceAllString(out, "")
	out = reStripSeasonOrPartSuffix3.ReplaceAllString(out, "")
	out = reStripSeasonOrPartSuffix4.ReplaceAllString(out, "")
	out = reStripSeasonOrPartSuffix5.ReplaceAllString(out, "")
	out = strings.TrimSpace(out)
	// Remove common sequel suffixes at end.
	out = reStripSeasonOrPartSuffix6.ReplaceAllString(out, "")
	out = reStripSeasonOrPartSuffix7.ReplaceAllString(out, "")
	out = reStripSeasonOrPartSuffix8.ReplaceAllString(out, "")
	out = strings.TrimSpace(out)
	if out == "" || out == s {
		return ""
	}
	return normalizeTitleMain(out)
}

var reExplicitSeasonMarker = regexp.MustCompile(`(?i)\b(?:season\b|s\d{1,2}\b)`)
var reExplicitSeasonMarkerCJK = regexp.MustCompile(`第\s*[一二三四五六七八九十0-9]{1,3}\s*(?:季|期|部)`)

func stripExplicitSeasonSuffix(s string) string {
	ss := strings.TrimSpace(s)
	if ss == "" {
		return ""
	}
	if !reExplicitSeasonMarker.MatchString(ss) && !reExplicitSeasonMarkerCJK.MatchString(ss) {
		return ""
	}
	if stripped := stripSeasonOrPartSuffix(ss); stripped != "" {
		return stripped
	}
	return ""
}

func looksLikeSeasonOrPartSuffix(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	// Common patterns: title + " 2" / " II" / "贰" / "第2季" etc.
	if reLooksLikeSeasonOrPartSuffix1.MatchString(s) {
		return true
	}
	if reLooksLikeSeasonOrPartSuffix2.MatchString(s) {
		return true
	}
	if reLooksLikeSeasonOrPartSuffix3.MatchString(s) {
		return true
	}
	if reLooksLikeSeasonOrPartSuffix4.FindStringIndex(s) != nil {
		return true
	}
	if reLooksLikeSeasonOrPartSuffix5.FindStringIndex(s) != nil {
		return true
	}
	return false
}

var reTitlePartNumber = regexp.MustCompile(`(?i)\bpart\.?\s*([0-9]{1,2})\b`)

func restorePartSuffixFromOriginalTitle(originalTitleSpan string, main string, aliases []string) (string, []string) {
	main = strings.TrimSpace(main)
	if main == "" || len(aliases) == 0 {
		return main, aliases
	}
	hasPartAlias := false
	kept := make([]string, 0, len(aliases))
	for _, a := range aliases {
		if strings.EqualFold(strings.TrimSpace(a), "part") {
			hasPartAlias = true
			continue
		}
		kept = append(kept, a)
	}
	if !hasPartAlias {
		return main, aliases
	}
	orig := strings.TrimSpace(originalTitleSpan)
	if orig == "" {
		// No structural evidence from titleslot; keep original aliases.
		return main, aliases
	}
	mm := reTitlePartNumber.FindStringSubmatch(orig)
	if len(mm) < 2 {
		// "Part" without numeric suffix is ambiguous; keep original aliases.
		return main, aliases
	}
	if !reTitlePartNumber.MatchString(main) {
		main = normalizeTitleMain(strings.TrimSpace(main + " Part " + mm[1]))
	}
	if len(kept) == 0 {
		return main, nil
	}
	return main, kept
}

func splitByStrongTitleDelims(s string) []string {
	if s == "" {
		return nil
	}
	if !strings.ContainsAny(s, "/／|｜;；\\") {
		return []string{s}
	}
	norm := strings.ReplaceAll(s, "｜", "|")
	norm = strings.ReplaceAll(norm, "／", "/")
	norm = strings.ReplaceAll(norm, "；", ";")
	parts := strings.FieldsFunc(norm, func(r rune) bool {
		return r == '/' || r == '|' || r == ';' || r == '\\'
	})
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	if len(out) == 0 {
		return []string{s}
	}
	return out
}

func chooseMainTitlePart(parts []string) string {
	best := ""
	bestScore := -1
	for idx, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		score := 0
		if hasCJK(p) {
			score += 20
		}
		if hasLatin(p) {
			score += 10
		}
		l := len([]rune(p))
		if l >= 2 {
			score += l
		}
		if idx == 0 {
			score += 2
		}
		if score > bestScore {
			bestScore = score
			best = p
		}
	}
	if best == "" && len(parts) > 0 {
		return strings.TrimSpace(parts[0])
	}
	return best
}

func extractPlatformRegionAttrsAndRemoveFromTokens(tokens []model.Token, out *[]model.Attribute) {
	if out == nil || len(tokens) == 0 {
		return
	}
	for i := 0; i < len(tokens); i++ {
		if tokens[i].Removed {
			continue
		}
		orig := strings.TrimSpace(tokens[i].Text)
		if orig == "" {
			continue
		}
		if regionHKMOTWEmbeddedPattern.FindStringIndex(orig) == nil {
			continue
		}
		cleaned := regionHKMOTWRemovalPattern.ReplaceAllString(orig, "")
		cleaned = strings.TrimSpace(cleaned)
		cleaned = strings.Trim(cleaned, "（）()[]【】")
		cleaned = strings.TrimSpace(cleaned)
		tokenStart, tokenEnd := i, i+1
		// Embedded region-restriction phrase should not anchor to title span.
		// Keep positional indices only when this token is pure region meta.
		if cleaned != "" {
			tokenStart, tokenEnd = -1, -1
		}
		*out = append(*out, model.Attribute{
			Type:       model.AttrPlatformRegion,
			Normalized: "HK+MO+TW",
			TokenStart: tokenStart,
			TokenEnd:   tokenEnd,
			Confidence: 0.8,
			Source:     model.AttrSourceRegionRestriction,
		})
		if cleaned == "" {
			tokens[i].Removed = true
			continue
		}
		tokens[i].Text = cleaned
	}
}

// Regular expressions hoisted out of function bodies so they are compiled once at init.
var (
	reNormalizeStreamMetaText1      = regexp.MustCompile(`\s+`)
	reApplyStreamingReleaseRouteV11 = regexp.MustCompile(`[,，;；\|/]+`)
	reStripSeasonOrPartSuffix1      = regexp.MustCompile(`\b\d{1,4}\s*-\s*\d{1,4}\s*$`)
	reStripSeasonOrPartSuffix2      = regexp.MustCompile(`(?i)\b\d{1,2}(?:st|nd|rd|th)?\s*season\b`)
	reStripSeasonOrPartSuffix3      = regexp.MustCompile(`(?i)\bseason\s*\d{1,2}\b`)
	reStripSeasonOrPartSuffix4      = regexp.MustCompile(`(?i)\bs\s*\d{1,2}\b`)
	reStripSeasonOrPartSuffix5      = regexp.MustCompile(`第\s*[一二三四五六七八九十0-9]{1,3}\s*(?:季|期|部)`)
	reStripSeasonOrPartSuffix6      = regexp.MustCompile(`\s+[0-9]{1,2}\s*$`)
	reStripSeasonOrPartSuffix7      = regexp.MustCompile(`(?i)\s+(?:ii|iii|iv|v|vi|vii|viii|ix|x)\s*$`)
	reStripSeasonOrPartSuffix8      = regexp.MustCompile(`\s*[贰貳弐二三四五六七八九十]\s*$`)
	reLooksLikeSeasonOrPartSuffix1  = regexp.MustCompile(`(?i)\bseason\b`)
	reLooksLikeSeasonOrPartSuffix2  = regexp.MustCompile(`(?i)\bs\d{1,2}\b`)
	reLooksLikeSeasonOrPartSuffix3  = regexp.MustCompile(`(?i)\b\d{1,2}(?:st|nd|rd|th)?\s*season\b`)
	reLooksLikeSeasonOrPartSuffix4  = regexp.MustCompile(`第\s*[一二三四五六七八九十0-9]{1,3}\s*(?:季|期|部)`)
	reLooksLikeSeasonOrPartSuffix5  = regexp.MustCompile(`\s(?:[0-9]{1,2}|[一二三四五六七八九十]|[贰貳弐参參三四五六七八九十])\s*$`)
)
