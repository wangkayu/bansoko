package parser

import (
	"regexp"
	"strings"

	"github.com/wangkayu/reltitle/pkg/model"
)

// Regular expressions hoisted out of function bodies so they are compiled once at init.
var (
	reExtractTechAudioSlotAndRemoveFromTokens1 = regexp.MustCompile(`^\d\.\d(?:\.\d)?$`)
	reExtractTechAudioSlotAndRemoveFromTokens2 = regexp.MustCompile(`^\d$`)
	reExtractTechAudioSlotAndRemoveFromTokens3 = regexp.MustCompile(`^\d$`)
	reExtractTechAudioSlotAndRemoveFromTokens4 = regexp.MustCompile(`(?i)^(aac|flac|opus|ac3|eac3|ddp|dd|truehd|dts(?:-hdma)?|dtshdma|pcm)x(\d{1,2})$`)
	reExtractTechAudioSlotAndRemoveFromTokens5 = regexp.MustCompile(`(?i)^(aac|flac|opus|ac3|eac3|ddp|dd|truehd|dts(?:-hdma)?|dtshdma|pcm)(\d\.\d)?$`)
	reExtractTechAudioSlotAndRemoveFromTokens6 = regexp.MustCompile(`^\d\.\d(?:\.\d)?$`)
)

// audioSlotExtract carries the shared state of
// extractTechAudioSlotAndRemoveFromTokens: the token stream, the attribute
// sink, the codec canonicalization table, and the tech-section start. The
// methods below were closures of the original monolithic function; behavior
// is unchanged.
type audioSlotExtract struct {
	tokens              []model.Token
	attrs               *[]model.Attribute
	audioCodecCanonical map[string]string
	techStart           int
	hasTechStart        bool
}

func (e *audioSlotExtract) canonCodec(s string) string {
	low := strings.ToLower(strings.TrimSpace(s))
	low = strings.ReplaceAll(low, "_", "-")
	if e.audioCodecCanonical != nil {
		if canonical, ok := e.audioCodecCanonical[low]; ok {
			return canonical
		}
	}
	return strings.ToUpper(strings.TrimSpace(s))
}

func (e *audioSlotExtract) canonLang(s string) string {
	low := strings.ToLower(strings.TrimSpace(s))
	low = strings.ReplaceAll(low, "_", "-")
	switch low {
	case "jp", "jpn", "jap", "japanese", "日语", "日語", "日文":
		return "JPN"
	case "en", "eng", "english", "英语", "英語", "英文":
		return "ENG"
	case "zh", "zho", "chi", "chn", "chinese", "中文", "国语", "國語", "华语", "華語":
		return "CHN"
	case "yue", "cantonese", "粤语", "粵語":
		return "YUE"
	case "kor", "kr", "korean", "韩语", "韓語", "韩文", "韓文":
		return "KOR"
	default:
		return ""
	}
}

func (e *audioSlotExtract) parseChannelsFromTokens(i int, end int) (string, int) {
	tokens := e.tokens
	if i < 0 {
		return "", i
	}
	if i >= end || i >= len(tokens) {
		return "", i
	}
	t0 := strings.TrimSpace(tokens[i].Text)
	if reExtractTechAudioSlotAndRemoveFromTokens1.MatchString(t0) {
		return t0, i + 1
	}
	if i+2 < end && i+2 < len(tokens) {
		a := strings.TrimSpace(tokens[i].Text)
		b := strings.TrimSpace(tokens[i+1].Text)
		c := strings.TrimSpace(tokens[i+2].Text)
		if reExtractTechAudioSlotAndRemoveFromTokens2.MatchString(a) && b == "." && reExtractTechAudioSlotAndRemoveFromTokens3.MatchString(c) {
			return a + "." + c, i + 3
		}
	}
	return "", i
}

func (e *audioSlotExtract) parseCodecWithCount(s string) (string, int) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", 0
	}
	low := strings.ToLower(s)
	low = strings.ReplaceAll(low, "_", "-")
	m := reExtractTechAudioSlotAndRemoveFromTokens4.FindStringSubmatch(low)
	if len(m) != 3 {
		return "", 0
	}
	codec := e.canonCodec(m[1])
	cnt := 0
	// count is intentionally limited; invalid values fall back to 0.
	for _, r := range m[2] {
		if r < '0' || r > '9' {
			return "", 0
		}
		cnt = cnt*10 + int(r-'0')
	}
	if cnt <= 0 {
		return "", 0
	}
	if cnt > 8 {
		cnt = 8
	}
	return codec, cnt
}

func (e *audioSlotExtract) isAudioCodecToken(s string) (string, string) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", ""
	}
	low := strings.ToLower(s)
	low = strings.ReplaceAll(low, "_", "-")
	m := reExtractTechAudioSlotAndRemoveFromTokens5.FindStringSubmatch(low)
	if len(m) == 3 {
		codec := e.canonCodec(m[1])
		ch := strings.TrimSpace(m[2])
		return codec, ch
	}
	codecSet := map[string]bool{"aac": true, "flac": true, "opus": true, "ac3": true, "eac3": true, "ddp": true, "dd": true, "truehd": true, "dts": true, "dts-hdma": true, "dtshdma": true, "pcm": true}
	if codecSet[low] {
		return e.canonCodec(low), ""
	}
	return "", ""
}

func (e *audioSlotExtract) isAudioMarkerToken(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	low := strings.ToLower(s)
	if c, cnt := e.parseCodecWithCount(s); c != "" && cnt > 0 {
		return true
	}
	if _, _ = e.isAudioCodecToken(s); true {
		c, _ := e.isAudioCodecToken(s)
		if c != "" {
			return true
		}
	}
	if reExtractTechAudioSlotAndRemoveFromTokens6.MatchString(strings.TrimSpace(s)) {
		return true
	}
	if strings.Contains(low, "atmos") || strings.Contains(low, "dtsx") || strings.Contains(low, "dts:x") {
		return true
	}
	if e.canonLang(s) != "" {
		return true
	}
	if strings.Contains(low, "audio") || strings.Contains(low, "dual") || strings.Contains(low, "multi") || strings.Contains(low, "dub") {
		return true
	}
	return false
}

func (e *audioSlotExtract) isSubtitleMarkerToken(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	low := strings.ToLower(s)
	switch low {
	case "sub", "subs", "subbed", "subtitle", "subtitles", "esub", "engsub":
		return true
	}
	return strings.Contains(low, "sub")
}

func (e *audioSlotExtract) isRelatedCodec(existingCodec, newCodec string) bool {
	existing := strings.ToLower(existingCodec)
	newLow := strings.ToLower(newCodec)
	if existing == newLow {
		return true
	}
	relatedGroups := [][]string{
		{"eac3", "ddp", "ac3", "dd"},
		{"dtshdma", "dts-hdma", "dtshd", "dts-hd", "dts"},
		{"truehd", "true-hd"},
	}
	for _, group := range relatedGroups {
		hasExisting := false
		hasNew := false
		for _, c := range group {
			if strings.Contains(existing, c) {
				hasExisting = true
			}
			if strings.Contains(newLow, c) {
				hasNew = true
			}
		}
		if hasExisting && hasNew {
			return true
		}
	}
	return false
}

// tokenClaimedBySubtitle reports whether token i is covered by an attribute
// emitted by the subtitle side (sub_lang / sub_flag / sub_lang_group /
// sub_format), regardless of which pass produced it.
func (e *audioSlotExtract) tokenClaimedBySubtitle(i int) bool {
	for _, attr := range *e.attrs {
		switch attr.Type {
		case model.AttrSubLang, model.AttrSubFlag, model.AttrSubLangGroup, model.AttrSubFormat:
			if attr.TokenStart <= i && i < attr.TokenEnd {
				return true
			}
		}
	}
	return false
}

func (e *audioSlotExtract) hasLexiconExtractedRelatedCodec(tokenIdx int, codec string) bool {
	attrs := e.attrs
	for _, attr := range *attrs {
		if attr.Type == model.AttrAudioCodec && attr.Source == model.AttrSourceLexicon {
			// Check if the lexicon attribute's token range overlaps with nearby removed tokens.
			// Lexicon may mark multiple tokens as removed but emit with a narrower token range.
			// We check if the current token is within a reasonable distance (±2 tokens) of the lexicon emit.
			if attr.TokenStart-2 <= tokenIdx && tokenIdx < attr.TokenEnd+2 {
				if e.isRelatedCodec(attr.Normalized, codec) {
					return true
				}
			}
		}
	}
	return false
}

func (e *audioSlotExtract) addAttr(t model.AttrType, val string, start int, end int, conf float64) {
	attrs := e.attrs
	val = strings.TrimSpace(val)
	if val == "" {
		return
	}
	*attrs = append(*attrs, model.Attribute{Type: t, Normalized: val, TokenStart: start, TokenEnd: end, Confidence: conf, Source: model.AttrSourceTechAudioSlot})
}

// emitFromSegment extracts audio tracks from the token range [start, end),
// appends the corresponding attributes, marks the range removed on success,
// and reports whether any track was found.
func (e *audioSlotExtract) emitFromSegment(start int, end int, conf float64) ([]model.AudioTrack, bool) {
	tokens := e.tokens
	if start < 0 {
		start = 0
	}
	if end > len(tokens) {
		end = len(tokens)
	}
	if end <= start {
		return nil, false
	}

	containsSubtitleMarker := false
	containsAudioCodecOrDub := false
	for k := start; k < end && k < len(tokens); k++ {
		t := strings.TrimSpace(tokens[k].Text)
		if t == "" {
			continue
		}
		if e.isSubtitleMarkerToken(t) {
			containsSubtitleMarker = true
		}
		if codec, cnt := e.parseCodecWithCount(t); codec != "" && cnt > 0 {
			containsAudioCodecOrDub = true
			continue
		}
		if codec, _ := e.isAudioCodecToken(t); codec != "" {
			containsAudioCodecOrDub = true
			continue
		}
		low := strings.ToLower(t)
		if strings.Contains(low, "dub") || strings.Contains(low, "audio") {
			containsAudioCodecOrDub = true
		}
	}
	if containsSubtitleMarker && !containsAudioCodecOrDub {
		return nil, false
	}

	isBracketOrParenBlock := false
	if tokens[start].Text == "[" || tokens[start].Text == "【" || tokens[start].Text == "(" || tokens[start].Text == "（" {
		isBracketOrParenBlock = true
	}

	cutStart := start
	if !isBracketOrParenBlock {
		foundMarker := false
		for k := start; k < end; k++ {
			if tokens[k].Removed {
				// Allow removed audio markers (often removed by lexicon) to act as anchors.
				if !e.isAudioMarkerToken(tokens[k].Text) {
					continue
				}
			}
			if e.isAudioMarkerToken(tokens[k].Text) {
				cutStart = k
				foundMarker = true
				break
			}
		}
		if !foundMarker {
			return nil, false
		}
	}

	// If we already have a techStart, avoid scanning arbitrary plain segments before it.
	// But allow bracket/paren blocks (common for tech/audio segments).
	if e.hasTechStart && start < e.techStart && !isBracketOrParenBlock {
		inBr := false
		for k := start; k < end; k++ {
			t := strings.TrimSpace(tokens[k].Text)
			if t == "[" || t == "【" {
				inBr = true
				break
			}
		}
		if !inBr {
			return nil, false
		}
	}

	tracks := make([]model.AudioTrack, 0, 4)
	curLang := ""
	curLangStart := -1
	pendingObj := ""
	pendingObjStart := -1
	for i := cutStart; i < end; i++ {
		if i < 0 || i >= len(tokens) {
			break
		}
		t := strings.TrimSpace(tokens[i].Text)
		if isInBracket(tokens, i) {
			t = strings.TrimRight(t, ",")
		}
		if tokens[i].Removed {
			// Many tech/audio tokens are removed early by lexicon or other passes.
			// We still want to extract structured audio tracks, but only from tokens that
			// clearly look like audio markers.
			if !e.isAudioMarkerToken(t) {
				if c, cnt := e.parseCodecWithCount(t); c == "" || cnt <= 0 {
					continue
				}
			}
			// Skip if lexicon already extracted a related audio_codec from this token.
			if c, _ := e.isAudioCodecToken(t); c != "" {
				if e.hasLexiconExtractedRelatedCodec(i, c) {
					continue
				}
			}
		}
		if t == "" {
			continue
		}
		if t == "[" || t == "]" || t == "【" || t == "】" || t == "(" || t == ")" || t == "（" || t == "）" {
			continue
		}
		if t == "+" || t == "＋" || t == "/" || t == "／" || t == "|" || t == "｜" || t == ";" || t == "；" || t == "," {
			continue
		}

		if isToonsHubCN(tokens) {
			subtitleToken := false
			for _, attr := range *e.attrs {
				if attr.Source == model.AttrSourceTechSubs && attr.TokenStart <= i && i < attr.TokenEnd {
					subtitleToken = true
					break
				}
			}
			if subtitleToken {
				continue
			}
		}
		if l := e.canonLang(t); l != "" {
			// 语言码已被字幕侧认领（removed 且被 sub_* 属性区间覆盖），且本段
			// 没有任何编解码/配音证据时，不把它当作音轨语言（如 [CHS_JP]、[GB_JP]）。
			if tokens[i].Removed && !containsAudioCodecOrDub && e.tokenClaimedBySubtitle(i) {
				continue
			}
			curLang = l
			curLangStart = i
			e.addAttr(model.AttrAudioLang, l, i, i+1, conf)
			continue
		}
		low := strings.ToLower(t)
		if strings.Contains(low, "atmos") {
			pendingObj = "ATMOS"
			pendingObjStart = i
			e.addAttr(model.AttrAudioObject, "ATMOS", i, i+1, conf)
			continue
		}
		if low == "dtsx" || low == "dts:x" {
			pendingObj = "DTSX"
			pendingObjStart = i
			e.addAttr(model.AttrAudioObject, "DTSX", i, i+1, conf)
			continue
		}

		if codec, cnt := e.parseCodecWithCount(t); codec != "" && cnt > 0 {
			// Common shorthand: AACx2/DDPx2 => multiple audio tracks.
			ch := ""
			j := i + 1
			if cc, nj := e.parseChannelsFromTokens(j, end); cc != "" {
				ch = cc
				j = nj
			}
			obj := ""
			for k := j; k < end && k < j+3; k++ {
				kk := strings.ToLower(strings.TrimSpace(tokens[k].Text))
				if strings.Contains(kk, "atmos") {
					obj = "ATMOS"
					j = k + 1
					break
				}
				if kk == "dtsx" || kk == "dts:x" {
					obj = "DTSX"
					j = k + 1
					break
				}
			}
			if obj == "" && pendingObj != "" && pendingObjStart >= 0 {
				if pendingObjStart >= cutStart && pendingObjStart >= i-3 {
					obj = pendingObj
				}
			}
			trStart := i
			if curLangStart >= cutStart && curLangStart < trStart {
				trStart = curLangStart
			}
			if pendingObjStart >= cutStart && pendingObjStart < trStart {
				trStart = pendingObjStart
			}
			for c := 0; c < cnt; c++ {
				tracks = append(tracks, model.AudioTrack{Language: curLang, Codec: codec, Channels: ch, Object: obj, TokenStart: trStart, TokenEnd: j, Source: model.AudioSourceTechAudioSlot})
			}
			e.addAttr(model.AttrAudioCodec, codec, i, j, conf)
			if ch != "" {
				e.addAttr(model.AttrAudioChannels, ch, i, j, conf)
			}
			if obj != "" {
				e.addAttr(model.AttrAudioObject, obj, i, j, conf)
			}
			if pendingObj != "" && pendingObjStart >= cutStart && pendingObjStart <= i {
				pendingObj = ""
				pendingObjStart = -1
			}
			i = j - 1
			continue
		}

		codec, inlineCh := e.isAudioCodecToken(t)
		if codec == "" {
			continue
		}
		ch := strings.TrimSpace(inlineCh)
		j := i + 1
		if ch == "" {
			if cc, nj := e.parseChannelsFromTokens(j, end); cc != "" {
				ch = cc
				j = nj
			}
		}
		obj := ""
		for k := j; k < end && k < j+3; k++ {
			kk := strings.ToLower(strings.TrimSpace(tokens[k].Text))
			if strings.Contains(kk, "atmos") {
				obj = "ATMOS"
				j = k + 1
				break
			}
			if kk == "dtsx" || kk == "dts:x" {
				obj = "DTSX"
				j = k + 1
				break
			}
		}
		if obj == "" && pendingObj != "" && pendingObjStart >= 0 {
			if pendingObjStart >= cutStart && pendingObjStart >= i-3 {
				obj = pendingObj
			}
		}

		trStart := i
		if curLangStart >= cutStart && curLangStart < trStart {
			trStart = curLangStart
		}
		if pendingObjStart >= cutStart && pendingObjStart < trStart {
			trStart = pendingObjStart
		}

		tr := model.AudioTrack{Language: curLang, Codec: codec, Channels: ch, Object: obj, TokenStart: trStart, TokenEnd: j, Source: model.AudioSourceTechAudioSlot}
		tracks = append(tracks, tr)
		e.addAttr(model.AttrAudioCodec, codec, i, j, conf)
		if ch != "" {
			e.addAttr(model.AttrAudioChannels, ch, i, j, conf)
		}
		if obj != "" {
			e.addAttr(model.AttrAudioObject, obj, i, j, conf)
		}
		if pendingObj != "" && pendingObjStart >= cutStart && pendingObjStart <= i {
			pendingObj = ""
			pendingObjStart = -1
		}
		i = j - 1
	}

	if len(tracks) == 0 {
		return nil, false
	}
	for k := cutStart; k < end; k++ {
		tokens[k].Removed = true
	}
	return tracks, true
}

// collectSegments scans the token stream for bracket blocks, paren blocks,
// and (when a tech section exists) plain segments, and extracts audio tracks
// from each via emitFromSegment.
func (e *audioSlotExtract) collectSegments() []model.AudioTrack {
	tokens := e.tokens
	allTracks := make([]model.AudioTrack, 0, 8)
	for i := 0; i < len(tokens); i++ {
		if e.hasTechStart && i < e.techStart {
			// Allow blocks that start before techStart but may contain tech/audio markers.
			if tokens[i].Text != "[" && tokens[i].Text != "【" && tokens[i].Text != "(" && tokens[i].Text != "（" {
				continue
			}
		}
		if tokens[i].Text == "[" || tokens[i].Text == "【" {
			open := tokens[i].Text
			close := "]"
			if open == "【" {
				close = "】"
			}
			depth := 0
			end := -1
			for j := i; j < len(tokens); j++ {
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
			if end >= 0 {
				if tr, ok := e.emitFromSegment(i, end+1, 0.75); ok {
					allTracks = append(allTracks, tr...)
				}
				i = end
				continue
			}
		}
		if tokens[i].Text == "(" || tokens[i].Text == "（" {
			_, closeIdx, _, ok := parseParenBlock(tokens, i, len(tokens))
			if ok {
				if tr, ok2 := e.emitFromSegment(i, closeIdx+1, 0.75); ok2 {
					allTracks = append(allTracks, tr...)
				}
				i = closeIdx
				continue
			}
		}
		if !e.hasTechStart {
			continue
		}
		if isInBracket(tokens, i) || isInParen(tokens, i) {
			continue
		}
		t := strings.TrimSpace(tokens[i].Text)
		if t == "" {
			continue
		}
		if t == "|" || t == "｜" || t == ";" || t == "；" {
			continue
		}
		start := i
		j := i
		for j < len(tokens) {
			if isInBracket(tokens, j) || isInParen(tokens, j) {
				break
			}
			jt := strings.TrimSpace(tokens[j].Text)
			if jt == "" {
				j++
				continue
			}
			if jt == "[" || jt == "【" || jt == "(" || jt == "（" {
				break
			}
			if jt == "|" || jt == "｜" || jt == ";" || jt == "；" {
				break
			}
			j++
		}
		if j > start {
			if tr, ok := e.emitFromSegment(start, j, 0.75); ok {
				allTracks = append(allTracks, tr...)
			}
			i = j - 1
		}
	}
	return allTracks
}

// backfillLanguage applies the conservative language backfill: if audio
// tracks lack language but we have exactly one audio_lang attribute (often
// derived from group/tag rules), apply it.
func (e *audioSlotExtract) backfillLanguage(allTracks []model.AudioTrack) {
	attrs := e.attrs
	allEmptyLang := true
	for i := range allTracks {
		if strings.TrimSpace(allTracks[i].Language) != "" {
			allEmptyLang = false
			break
		}
	}
	if allEmptyLang {
		langSet := map[string]bool{}
		langOrder := make([]string, 0, 4)
		for _, a := range *attrs {
			if a.Type != model.AttrAudioLang {
				continue
			}
			v := strings.TrimSpace(strings.ToUpper(a.Normalized))
			if v == "" {
				continue
			}
			if !langSet[v] {
				langSet[v] = true
				langOrder = append(langOrder, v)
			}
		}
		if len(langOrder) == 1 {
			for i := range allTracks {
				allTracks[i].Language = langOrder[0]
			}
		}
	}
}
