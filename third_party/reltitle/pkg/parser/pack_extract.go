package parser

import (
	"sort"
	"strings"

	"github.com/wangkayu/reltitle/pkg/model"
)

func normalizePackPlusMarker(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", false
	}
	up := strings.ToUpper(s)
	switch up {
	case "TV":
		return "TV", true
	case "MOVIE", "FILM":
		return "MOVIE", true
	case "OVA", "OVAS":
		return "OVA", true
	case "OAD", "OADS":
		return "OAD", true
	case "ONA", "ONAS":
		return "ONA", true
	case "SP":
		return "SP", true
	case "EX":
		return "EX", true
	default:
		return "", false
	}
}

func extractPackPlusContentsAndRemoveFromTokens(tokens []model.Token) []model.Attribute {
	// Handle pack markers like:
	//   "TV+OAD", "TV + Movie", "OVA+SP".
	// Emit AttrContents for each marker and remove marker/plus tokens so title/aliases remain clean.
	if len(tokens) == 0 {
		return nil
	}
	attrs := make([]model.Attribute, 0, 4)
	add := func(norm string, start int, end int) {
		attrs = append(attrs, model.Attribute{Type: model.AttrContents, Normalized: norm, TokenStart: start, TokenEnd: end, Confidence: 0.85, Source: model.AttrSourcePackPlusContents})
	}

	for i := 0; i < len(tokens); i++ {
		if tokens[i].Removed {
			continue
		}
		t := strings.TrimSpace(tokens[i].Text)
		if t == "" {
			continue
		}

		// Combined token like "TV+OAD".
		if strings.Contains(t, "+") || strings.Contains(t, "＋") {
			parts := strings.FieldsFunc(t, func(r rune) bool { return r == '+' || r == '＋' })
			if len(parts) >= 2 {
				norms := make([]string, 0, len(parts))
				allOK := true
				for _, p := range parts {
					if norm, ok := normalizePackPlusMarker(p); ok {
						norms = append(norms, norm)
						continue
					}
					allOK = false
					break
				}
				if allOK && len(norms) >= 2 {
					for _, n := range norms {
						add(n, i, i+1)
					}
					tokens[i].Removed = true
					continue
				}
			}
		}

		first, ok := normalizePackPlusMarker(t)
		if !ok {
			continue
		}
		// Look for marker (+ marker)+ chain.
		markerIdx := []int{i}
		plusIdx := make([]int, 0, 4)
		j := i
		for {
			k := j + 1
			for k < len(tokens) {
				if tokens[k].Removed {
					k++
					continue
				}
				kt := strings.TrimSpace(tokens[k].Text)
				if kt == "" {
					k++
					continue
				}
				break
			}
			if k >= len(tokens) {
				break
			}
			sep := strings.TrimSpace(tokens[k].Text)
			if sep != "+" && sep != "＋" {
				break
			}
			m := k + 1
			for m < len(tokens) {
				if tokens[m].Removed {
					m++
					continue
				}
				mt := strings.TrimSpace(tokens[m].Text)
				if mt == "" {
					m++
					continue
				}
				break
			}
			if m >= len(tokens) {
				break
			}
			mt := strings.TrimSpace(tokens[m].Text)
			if _, ok := normalizePackPlusMarker(mt); !ok {
				break
			}
			plusIdx = append(plusIdx, k)
			markerIdx = append(markerIdx, m)
			j = m
		}
		if len(markerIdx) < 2 {
			_ = first
			continue
		}
		if i-1 >= 0 {
			prev := strings.TrimSpace(tokens[i-1].Text)
			if prev == "-" {
				tokens[i-1].Removed = true
			}
		}
		for _, mi := range markerIdx {
			norm, _ := normalizePackPlusMarker(strings.TrimSpace(tokens[mi].Text))
			add(norm, mi, mi+1)
			tokens[mi].Removed = true
		}
		for _, pi := range plusIdx {
			tokens[pi].Removed = true
		}
		i = markerIdx[len(markerIdx)-1]
	}
	if len(attrs) == 0 {
		return nil
	}
	return attrs
}

func extractPackAudioSubMetaAndRemoveFromTokens(tokens []model.Token) []model.Attribute {
	if len(tokens) == 0 {
		return nil
	}

	attrs := make([]model.Attribute, 0, 8)

	addAudio := func(langCodes []string, start int, end int, conf float64) {
		if len(langCodes) == 0 {
			return
		}
		norm := "AUDIO_" + strings.Join(langCodes, "+")
		attrs = append(attrs, model.Attribute{Type: model.AttrAudioFlag, Normalized: norm, TokenStart: start, TokenEnd: end, Confidence: conf, Source: model.AttrSourcePackAudio})
	}
	addSubFlag := func(flag string, start int, end int, conf float64) {
		flag = strings.TrimSpace(flag)
		if flag == "" {
			return
		}
		attrs = append(attrs, model.Attribute{Type: model.AttrSubFlag, Normalized: flag, TokenStart: start, TokenEnd: end, Confidence: conf, Source: model.AttrSourcePackSub})
	}
	addSubLang := func(code string, start int, end int, conf float64) {
		code = strings.TrimSpace(code)
		if code == "" {
			return
		}
		attrs = append(attrs, model.Attribute{Type: model.AttrSubLang, Normalized: code, TokenStart: start, TokenEnd: end, Confidence: conf, Source: model.AttrSourcePackSub})
	}
	addNote := func(note string, start int, end int, conf float64) {
		note = strings.TrimSpace(note)
		if note == "" {
			return
		}
		attrs = append(attrs, model.Attribute{Type: model.AttrNote, Normalized: note, TokenStart: start, TokenEnd: end, Confidence: conf, Source: model.AttrSourcePackNote})
	}

	normLangCodes := func(codes []string) []string {
		seen := map[string]bool{}
		out := make([]string, 0, len(codes))
		for _, c := range codes {
			c = strings.TrimSpace(strings.ToUpper(c))
			if c == "" {
				continue
			}
			if !seen[c] {
				seen[c] = true
				out = append(out, c)
			}
		}
		sort.Strings(out)
		return out
	}

	parseAudioLangCodes := func(s string) []string {
		low := strings.ToLower(strings.TrimSpace(s))
		low = strings.Trim(low, ",，、;；")
		codes := make([]string, 0, 4)
		if strings.Contains(low, "日英") {
			codes = append(codes, "JPN", "ENG")
		}
		if strings.Contains(low, "日语") || strings.Contains(low, "日語") || strings.Contains(low, "日文") {
			codes = append(codes, "JPN")
		}
		if strings.Contains(low, "英语") || strings.Contains(low, "英語") || strings.Contains(low, "英文") {
			codes = append(codes, "ENG")
		}
		if strings.Contains(low, "华语") || strings.Contains(low, "華語") || strings.Contains(low, "国语") || strings.Contains(low, "國語") || strings.Contains(low, "中文") {
			codes = append(codes, "CHN")
		}
		if strings.Contains(low, "粤语") || strings.Contains(low, "粵語") || strings.Contains(low, "cantonese") {
			codes = append(codes, "YUE")
		}
		if strings.Contains(low, "韩语") || strings.Contains(low, "韓語") || strings.Contains(low, "韩文") || strings.Contains(low, "韓文") {
			codes = append(codes, "KOR")
		}
		for _, r := range []rune(s) {
			switch r {
			case '华', '華', '中':
				codes = append(codes, "CHN")
			case '日':
				codes = append(codes, "JPN")
			case '英':
				codes = append(codes, "ENG")
			case '韩', '韓':
				codes = append(codes, "KOR")
			case '粤', '粵':
				codes = append(codes, "YUE")
			}
		}
		return normLangCodes(codes)
	}

	parseSubLangCodes := func(s string) []string {
		low := strings.ToLower(strings.TrimSpace(s))
		codes := make([]string, 0, 4)
		if strings.Contains(low, "简繁") || strings.Contains(low, "簡繁") || strings.Contains(low, "繁简") || strings.Contains(low, "繁簡") {
			codes = append(codes, "SC", "TC")
		}
		if strings.Contains(low, "简中") || strings.Contains(low, "簡中") || strings.Contains(low, "简体") || strings.Contains(low, "簡體") || strings.Contains(low, "gb") {
			codes = append(codes, "SC")
		}
		if strings.Contains(low, "繁中") || strings.Contains(low, "繁體") || strings.Contains(low, "繁体") || strings.Contains(low, "big5") {
			codes = append(codes, "TC")
		}
		if strings.Contains(low, "英文") || strings.Contains(low, "英语") || strings.Contains(low, "英語") {
			codes = append(codes, "ENG")
		}
		if strings.Contains(low, "日文") || strings.Contains(low, "日语") || strings.Contains(low, "日語") {
			codes = append(codes, "JPN")
		}
		if strings.Contains(low, "韩文") || strings.Contains(low, "韩语") || strings.Contains(low, "韓文") || strings.Contains(low, "韓語") {
			codes = append(codes, "KOR")
		}
		return normLangCodes(codes)
	}

	segmentText := func(a int, b int) string {
		parts := make([]string, 0, b-a)
		for i := a; i < b; i++ {
			if i < 0 || i >= len(tokens) {
				continue
			}
			if tokens[i].Removed {
				continue
			}
			t := strings.TrimSpace(tokens[i].Text)
			if t == "" {
				continue
			}
			if t == "(" || t == ")" || t == "（" || t == "）" {
				continue
			}
			parts = append(parts, t)
		}
		return strings.TrimSpace(strings.Join(parts, " "))
	}

	removeRange := func(a int, b int) {
		if a < 0 {
			a = 0
		}
		if b > len(tokens) {
			b = len(tokens)
		}
		for i := a; i < b; i++ {
			tokens[i].Removed = true
		}
	}

	for i := 0; i < len(tokens); i++ {
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
		innerTrim := strings.TrimSpace(inner)
		if innerTrim == "" {
			continue
		}
		if strings.Contains(innerTrim, "整理时间") || strings.Contains(innerTrim, "整理時間") {
			addNote(innerTrim, i, closeIdx+1, 0.7)
			removeRange(i, closeIdx+1)
			i = closeIdx
			continue
		}
	}

	hasSemi := false
	for i := 0; i < len(tokens); i++ {
		if tokens[i].Removed {
			continue
		}
		t := strings.TrimSpace(tokens[i].Text)
		if t == ";" || t == "；" {
			hasSemi = true
			break
		}
	}
	if !hasSemi {
		return attrs
	}

	segStart := 0
	for i := 0; i <= len(tokens); i++ {
		isDelim := false
		if i < len(tokens) {
			if tokens[i].Removed {
				continue
			}
			t := strings.TrimSpace(tokens[i].Text)
			if t == ";" || t == "；" {
				isDelim = true
			}
		}
		if i == len(tokens) {
			isDelim = true
		}
		if !isDelim {
			continue
		}

		segEnd := i
		for segStart < segEnd {
			if segStart < 0 || segStart >= len(tokens) {
				break
			}
			if tokens[segStart].Removed {
				segStart++
				continue
			}
			if strings.TrimSpace(tokens[segStart].Text) == "" {
				segStart++
				continue
			}
			break
		}
		for segEnd > segStart {
			if segEnd-1 < 0 || segEnd-1 >= len(tokens) {
				break
			}
			if tokens[segEnd-1].Removed {
				segEnd--
				continue
			}
			if strings.TrimSpace(tokens[segEnd-1].Text) == "" {
				segEnd--
				continue
			}
			break
		}
		if segEnd <= segStart {
			segStart = i + 1
			continue
		}

		text := segmentText(segStart, segEnd)
		low := strings.ToLower(text)
		if strings.Contains(low, "音轨") || strings.Contains(low, "音軌") {
			langs := parseAudioLangCodes(text)
			addAudio(langs, segStart, segEnd, 0.75)
			removeRange(segStart, segEnd)
		} else if strings.Contains(low, "字幕") || strings.Contains(low, "sub") {
			if strings.Contains(text, "外挂") || strings.Contains(text, "外掛") {
				addSubFlag("ex", segStart, segEnd, 0.75)
			}
			if strings.Contains(text, "内嵌") || strings.Contains(text, "內嵌") {
				addSubFlag("hard", segStart, segEnd, 0.75)
			}
			if strings.Contains(text, "内封") || strings.Contains(text, "內封") {
				addSubFlag("soft", segStart, segEnd, 0.75)
			}
			langs := parseSubLangCodes(text)
			for _, c := range langs {
				addSubLang(c, segStart, segEnd, 0.75)
			}
			removeRange(segStart, segEnd)
		}

		segStart = i + 1
	}

	return attrs
}

func extractPackContentsAndRemoveFromTokens(tokens []model.Token) []model.Attribute {
	// Handle pack titles like:
	//   "...：TV动画 (2009年版)+剧场版+ONA篇+小说+漫画+音乐会+CD+其他；日语音轨; 外挂简中字幕 ..."
	// by extracting the "TV动画 ... + ..." segment into AttrContents and removing it from tokens,
	// so title/aliases remain the series name only.
	if len(tokens) == 0 {
		return nil
	}

	colonIdx := -1
	semiIdx := -1
	plusSeen := false
	for i := 0; i < len(tokens); i++ {
		if tokens[i].Removed {
			continue
		}
		t := strings.TrimSpace(tokens[i].Text)
		if t != ":" && t != "：" {
			continue
		}
		// Require a near "TV动画" indicator after colon.
		nearTV := false
		for j := i + 1; j < len(tokens) && j <= i+10; j++ {
			if tokens[j].Removed {
				continue
			}
			jt := strings.TrimSpace(tokens[j].Text)
			if jt == "" {
				continue
			}
			if strings.Contains(jt, "TV动画") || strings.Contains(strings.ToLower(jt), "tv") {
				nearTV = true
				break
			}
		}
		if !nearTV {
			continue
		}
		colonIdx = i
		break
	}
	if colonIdx < 0 {
		return nil
	}

	for i := colonIdx + 1; i < len(tokens); i++ {
		if tokens[i].Removed {
			continue
		}
		t := strings.TrimSpace(tokens[i].Text)
		if t == "+" || t == "＋" {
			plusSeen = true
			continue
		}
		if t == ";" || t == "；" {
			semiIdx = i
			break
		}
	}
	if semiIdx < 0 || !plusSeen {
		return nil
	}

	// Split [colonIdx+1, semiIdx) by '+' at top-level paren depth.
	items := make([]model.Attribute, 0, 8)
	curStart := colonIdx + 1
	parenDepth := 0
	flush := func(a, b int) {
		if b <= a {
			return
		}
		parts := make([]string, 0, b-a)
		for k := a; k < b; k++ {
			if tokens[k].Removed {
				continue
			}
			kt := strings.TrimSpace(tokens[k].Text)
			if kt == "" {
				continue
			}
			if kt == "+" || kt == "＋" {
				continue
			}
			parts = append(parts, kt)
		}
		inner := strings.TrimSpace(strings.Join(parts, " "))
		inner = normalizeParenSpacing(inner)
		inner = strings.TrimSpace(strings.Trim(inner, "·"))
		if inner == "" {
			return
		}
		items = append(items, model.Attribute{Type: model.AttrContents, Normalized: inner, TokenStart: a, TokenEnd: b, Confidence: 0.7, Source: model.AttrSourcePackContents})
	}

	for i := colonIdx + 1; i < semiIdx; i++ {
		if tokens[i].Removed {
			continue
		}
		t := strings.TrimSpace(tokens[i].Text)
		if t == "(" || t == "（" {
			parenDepth++
			continue
		}
		if t == ")" || t == "）" {
			if parenDepth > 0 {
				parenDepth--
			}
			continue
		}
		if parenDepth == 0 && (t == "+" || t == "＋") {
			flush(curStart, i)
			curStart = i + 1
			continue
		}
	}
	flush(curStart, semiIdx)
	if len(items) == 0 {
		return nil
	}

	// Remove the whole contents section from the token stream.
	// Keep the ';'/'；' delimiter token (semiIdx) so we can segment subsequent audio/sub/meta parts.
	for i := colonIdx; i < semiIdx && i < len(tokens); i++ {
		tokens[i].Removed = true
	}
	return items
}
