package parser

import (
	"regexp"
	"strings"
	"unicode"

	"github.com/wangkayu/reltitle/pkg/model"
	"github.com/wangkayu/reltitle/pkg/tokenize"
)

var sxxExxVersionPattern = regexp.MustCompile(`(?i)^s\d{1,2}e\d{1,4}v(\d+)$`)

func extractVersionAttrsAndRemoveFromTokens(tokens []model.Token, out *[]model.Attribute) string {
	if out == nil || len(tokens) == 0 {
		return ""
	}
	best := ""
	for i := 0; i < len(tokens); i++ {
		if tokens[i].Removed {
			continue
		}
		// Standalone [v2] / (v2) style. Tokenizer usually makes it a single token "v2".
		text := strings.TrimSpace(tokens[i].Text)
		m := standaloneVersionPattern.FindStringSubmatch(strings.TrimRight(text, ","))
		compactEpisodeVersion := false
		if len(m) != 2 {
			m = sxxExxVersionPattern.FindStringSubmatch(text)
			compactEpisodeVersion = len(m) == 2
		}
		if len(m) != 2 {
			continue
		}
		v := strings.TrimLeft(m[1], "0")
		if v == "" {
			v = m[1]
		}
		norm := "v" + v
		*out = append(*out, model.Attribute{Type: model.AttrVersion, Normalized: norm, TokenStart: i, TokenEnd: i + 1, Confidence: 0.7, Source: model.AttrSourceVersionToken})
		// Only remove when it is clearly meta (most common inside brackets/parens).
		if !compactEpisodeVersion && (isInBracket(tokens, i) || isInParen(tokens, i)) {
			tokens[i].Removed = true
		}
		if best == "" {
			best = norm
		}
	}
	return best
}

func extractEditionAttrsAndRemoveFromTokens(tokens []model.Token, out *[]model.Attribute, allowUnderscoreRoute bool) {
	if out == nil || len(tokens) == 0 {
		return
	}
	extractRestoreEditionAttrsAndRemoveFromTokens(tokens, out)
	extractCompositeVersionEditionAttrsAndRemoveFromTokens(tokens, out)
	extractWebVersionEditionAttrsAndRemoveFromTokens(tokens, out)
	extractAudioCommentaryEditionAttrsAndRemoveFromTokens(tokens, out, allowUnderscoreRoute)
	extractUncutEditionAttrsAndRemoveFromTokens(tokens, out)
	extractExtendedCutEditionAttrsAndRemoveFromTokens(tokens, out)
	extractUncensoredEditionAttrsAndRemoveFromTokens(tokens, out)
	extractDirectorsCutEditionAttrsAndRemoveFromTokens(tokens, out)
	extractRemasteredEditionAttrsAndRemoveFromTokens(tokens, out)
	extractEncodeVerEditionAttrsAndRemoveFromTokens(tokens, out)
}

func editionVersionNoteForSegment(seg string) string {
	norm := strings.ToLower(strings.TrimSpace(seg))
	norm = strings.ReplaceAll(norm, " ", "")
	switch norm {
	case "tv版", "tv版本", "tvver", "tvversion":
		return "TV_VERSION"
	case "web版", "web版本", "webver", "webversion":
		return "WEB_VERSION"
	case "无修版", "無修版", "完全无修版", "完全無修版":
		return "UNCENSORED_VERSION"
	default:
		return ""
	}
}

func extractCompositeEditionNotes(text string) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	parts := reExtractCompositeEditionNotes1.Split(text, -1)
	if len(parts) <= 1 {
		return nil
	}
	out := make([]string, 0, len(parts))
	seen := map[string]bool{}
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		note := editionVersionNoteForSegment(part)
		if note == "" {
			return nil
		}
		if !seen[note] {
			out = append(out, note)
			seen[note] = true
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func extractCompositeVersionEditionAttrsAndRemoveFromTokens(tokens []model.Token, out *[]model.Attribute) {
	if out == nil || len(tokens) == 0 {
		return
	}
	findBlockEnd := func(start int, open, close string) int {
		depth := 0
		for j := start; j < len(tokens); j++ {
			if tokens[j].Removed {
				continue
			}
			cur := strings.TrimSpace(tokens[j].Text)
			if cur == open {
				depth++
				continue
			}
			if cur == close {
				depth--
				if depth == 0 {
					return j
				}
			}
		}
		return -1
	}
	for i := 0; i < len(tokens); i++ {
		if tokens[i].Removed {
			continue
		}
		open := strings.TrimSpace(tokens[i].Text)
		close := ""
		switch open {
		case "[":
			close = "]"
		case "【":
			close = "】"
		case "(":
			close = ")"
		case "（":
			close = "）"
		default:
			continue
		}
		end := findBlockEnd(i, open, close)
		if end <= i+1 {
			continue
		}

		var inner strings.Builder
		for k := i + 1; k < end; k++ {
			if tokens[k].Removed {
				continue
			}
			inner.WriteString(strings.TrimSpace(tokens[k].Text))
		}
		joined := strings.TrimSpace(inner.String())
		if joined == "" {
			continue
		}

		notes := extractCompositeEditionNotes(joined)
		if len(notes) == 0 {
			continue
		}

		for _, note := range notes {
			*out = append(*out, model.Attribute{
				Type:       model.AttrNote,
				Normalized: note,
				TokenStart: i,
				TokenEnd:   end + 1,
				Confidence: 0.74,
				Source:     model.AttrSourceEditionToken,
			})
		}
		for k := i; k <= end; k++ {
			tokens[k].Removed = true
		}
		i = end
	}
}

func extractRestoreEditionAttrsAndRemoveFromTokens(tokens []model.Token, out *[]model.Attribute) {
	if out == nil || len(tokens) == 0 {
		return
	}
	findBlockEnd := func(start int, open, close string) int {
		depth := 0
		for j := start; j < len(tokens); j++ {
			if tokens[j].Removed {
				continue
			}
			cur := strings.TrimSpace(tokens[j].Text)
			if cur == open {
				depth++
				continue
			}
			if cur == close {
				depth--
				if depth == 0 {
					return j
				}
			}
		}
		return -1
	}
	reRestore := reExtractRestoreEditionAttrsAndRemoveFromTokens1
	for i := 0; i < len(tokens); i++ {
		if tokens[i].Removed {
			continue
		}
		open := strings.TrimSpace(tokens[i].Text)
		close := ""
		switch open {
		case "[":
			close = "]"
		case "【":
			close = "】"
		case "(":
			close = ")"
		case "（":
			close = "）"
		default:
			continue
		}
		end := findBlockEnd(i, open, close)
		if end <= i+1 {
			continue
		}
		inner := make([]string, 0, end-i-1)
		for k := i + 1; k < end; k++ {
			if tokens[k].Removed {
				continue
			}
			t := strings.TrimSpace(tokens[k].Text)
			if t == "" {
				continue
			}
			inner = append(inner, t)
		}
		if len(inner) == 0 {
			continue
		}
		joined := strings.ToLower(strings.Join(inner, ""))
		has4k := strings.Contains(joined, "4k")
		has2k := strings.Contains(joined, "2k")
		if !has4k || !reRestore.MatchString(strings.Join(inner, "")) {
			continue
		}
		norm := "RESTORED_EDITION"
		if has2k {
			norm = "RESTORE_4K_2K_EDITION"
		}
		*out = append(*out, model.Attribute{
			Type:       model.AttrNote,
			Normalized: norm,
			TokenStart: i,
			TokenEnd:   end + 1,
			Confidence: 0.74,
			Source:     model.AttrSourceEditionToken,
		})
		for k := i; k <= end; k++ {
			tokens[k].Removed = true
		}
		i = end
	}
}

func extractWebVersionEditionAttrsAndRemoveFromTokens(tokens []model.Token, out *[]model.Attribute) {
	if out == nil || len(tokens) == 0 {
		return
	}
	add := func(start, end int) {
		*out = append(*out, model.Attribute{
			Type:       model.AttrNote,
			Normalized: "WEB_VERSION",
			TokenStart: start,
			TokenEnd:   end,
			Confidence: 0.72,
			Source:     model.AttrSourceEditionToken,
		})
	}
	for i := 0; i < len(tokens); i++ {
		if tokens[i].Removed {
			continue
		}
		txt := strings.TrimSpace(tokens[i].Text)
		if txt == "" {
			continue
		}
		low := strings.ToLower(txt)
		compact := strings.ReplaceAll(strings.ReplaceAll(low, "_", ""), "-", "")
		compact = strings.Trim(compact, ".。· ")
		// WEB版 / WEB版本 / webver / webversion in one token.
		if compact == "web版" || compact == "web版本" || compact == "webver" || compact == "webversion" {
			add(i, i+1)
			tokens[i].Removed = true
			continue
		}
		if compact != "web" {
			continue
		}
		j := nextAliveToken(tokens, i+1)
		if j < 0 {
			continue
		}
		nextRaw := strings.TrimSpace(tokens[j].Text)
		nextLow := strings.ToLower(strings.Trim(nextRaw, ".。"))
		if nextRaw != "版" && nextLow != "ver" && nextLow != "version" {
			continue
		}
		end := j + 1
		if (nextLow == "ver" || nextLow == "version") && j+1 < len(tokens) {
			if d := strings.TrimSpace(tokens[j+1].Text); d == "." || d == "。" {
				end = j + 2
				tokens[j+1].Removed = true
			}
		}
		add(i, end)
		tokens[i].Removed = true
		tokens[j].Removed = true
	}
}

func extractAudioCommentaryEditionAttrsAndRemoveFromTokens(tokens []model.Token, out *[]model.Attribute, allowUnderscoreRoute bool) {
	if out == nil || len(tokens) == 0 {
		return
	}
	for i := 0; i+2 < len(tokens); i++ {
		if tokens[i].Removed {
			continue
		}
		curNorm := normalizeEditionToken(tokens[i].Text, false)
		if curNorm != "audio" {
			// Underscore / hyphen collapsed route is intentionally gated to specific groups
			// to avoid polluting generic parsing.
			if !(allowUnderscoreRoute && normalizeEditionToken(tokens[i].Text, true) == "audiocommentary") {
				continue
			}
		}

		j := -1
		if curNorm == "audio" {
			j = nextAliveToken(tokens, i+1)
			if j < 0 {
				continue
			}
			if normalizeEditionToken(tokens[j].Text, false) != "commentary" {
				if allowUnderscoreRoute {
					if txt := strings.TrimSpace(tokens[j].Text); txt == "_" || txt == "-" {
						j2 := nextAliveToken(tokens, j+1)
						if j2 >= 0 && normalizeEditionToken(tokens[j2].Text, false) == "commentary" {
							j = j2
						} else {
							continue
						}
					} else {
						continue
					}
				} else {
					continue
				}
			}
		} else {
			// audio_commentary / audio-commentary (group-routed only)
			j = i
		}

		k := nextAliveToken(tokens, j+1)
		if k < 0 {
			continue
		}
		ver := normalizeEditionToken(tokens[k].Text, false)
		if ver != "ver" && ver != "version" {
			continue
		}

		end := k
		if n := nextAliveToken(tokens, end+1); n >= 0 {
			norm := strings.TrimSpace(tokens[n].Text)
			if norm == "." || norm == "~" {
				end = n
			}
		}
		if n := nextAliveToken(tokens, end+1); n >= 0 {
			norm := strings.TrimSpace(tokens[n].Text)
			if norm == "~" {
				end = n
			}
		}

		start := i
		if i-1 >= 0 {
			prev := strings.TrimSpace(tokens[i-1].Text)
			if prev == "~" {
				start = i - 1
			}
		}

		*out = append(*out, model.Attribute{
			Type:       model.AttrNote,
			Normalized: "AUDIO_COMMENTARY",
			TokenStart: i,
			TokenEnd:   k + 1,
			Confidence: 0.75,
			Source:     model.AttrSourceEditionToken,
		})

		for idx := start; idx <= end; idx++ {
			tokens[idx].Removed = true
		}
	}
}

func extractEncodeVerEditionAttrsAndRemoveFromTokens(tokens []model.Token, out *[]model.Attribute) {
	if out == nil || len(tokens) == 0 {
		return
	}
	for i := 0; i < len(tokens); i++ {
		if tokens[i].Removed {
			continue
		}
		ver := normalizeEditionToken(tokens[i].Text, false)
		if ver != "ver" && ver != "version" {
			continue
		}
		if !isInBracket(tokens, i) && !isInParen(tokens, i) {
			continue
		}
		prev := prevAliveToken(tokens, i-1)
		if prev < 0 {
			continue
		}
		prevNorm := normalizeEditionToken(tokens[prev].Text, true)
		if !isLikelyEncodeEditionAnchor(prevNorm) {
			continue
		}
		end := i
		if n := nextAliveToken(tokens, i+1); n >= 0 && strings.TrimSpace(tokens[n].Text) == "." {
			end = n
		}
		*out = append(*out, model.Attribute{
			Type:       model.AttrNote,
			Normalized: "ENCODE_VER",
			TokenStart: i,
			TokenEnd:   end + 1,
			Confidence: 0.7,
			Source:     model.AttrSourceEditionToken,
		})
		for idx := i; idx <= end; idx++ {
			tokens[idx].Removed = true
		}
	}
}

func extractUncensoredEditionAttrsAndRemoveFromTokens(tokens []model.Token, out *[]model.Attribute) {
	if out == nil || len(tokens) == 0 {
		return
	}
	for i := 0; i < len(tokens); i++ {
		if tokens[i].Removed {
			continue
		}
		if compactEditionWord(tokens[i].Text) != "uncensored" {
			continue
		}
		if !isInBracket(tokens, i) && !isInParen(tokens, i) {
			continue
		}
		j := nextAliveToken(tokens, i+1)
		if j < 0 {
			continue
		}
		ver := normalizeEditionToken(tokens[j].Text, false)
		if ver != "ver" && ver != "version" {
			continue
		}
		end := j
		if n := nextAliveToken(tokens, j+1); n >= 0 && strings.TrimSpace(tokens[n].Text) == "." {
			end = n
		}
		*out = append(*out, model.Attribute{
			Type:       model.AttrNote,
			Normalized: "UNCENSORED_VER",
			TokenStart: i,
			TokenEnd:   end + 1,
			Confidence: 0.72,
			Source:     model.AttrSourceEditionToken,
		})
		for k := i; k <= end; k++ {
			tokens[k].Removed = true
		}
	}
}

func extractUncutEditionAttrsAndRemoveFromTokens(tokens []model.Token, out *[]model.Attribute) {
	if out == nil || len(tokens) == 0 {
		return
	}
	for i := 0; i < len(tokens); i++ {
		if tokens[i].Removed {
			continue
		}
		if compactEditionWord(tokens[i].Text) != "uncut" {
			continue
		}
		if !isInBracket(tokens, i) && !isInParen(tokens, i) {
			continue
		}
		end := i
		norm := "UNCUT"
		if j := nextAliveToken(tokens, i+1); j >= 0 {
			ver := normalizeEditionToken(tokens[j].Text, false)
			if ver == "ver" || ver == "version" {
				end = j
				norm = "UNCUT_VER"
				if n := nextAliveToken(tokens, j+1); n >= 0 && strings.TrimSpace(tokens[n].Text) == "." {
					end = n
				}
			}
		}
		*out = append(*out, model.Attribute{
			Type:       model.AttrNote,
			Normalized: norm,
			TokenStart: i,
			TokenEnd:   end + 1,
			Confidence: 0.72,
			Source:     model.AttrSourceEditionToken,
		})
		for k := i; k <= end; k++ {
			tokens[k].Removed = true
		}
	}
}

func extractDirectorsCutEditionAttrsAndRemoveFromTokens(tokens []model.Token, out *[]model.Attribute) {
	if out == nil || len(tokens) == 0 {
		return
	}
	for i := 0; i < len(tokens); i++ {
		if tokens[i].Removed {
			continue
		}
		left := compactEditionWord(tokens[i].Text)
		if left != "directors" && left != "director" {
			continue
		}
		if !isInBracket(tokens, i) && !isInParen(tokens, i) {
			continue
		}
		j := nextAliveToken(tokens, i+1)
		if j < 0 {
			continue
		}
		if compactEditionWord(tokens[j].Text) != "cut" {
			continue
		}
		*out = append(*out, model.Attribute{
			Type:       model.AttrNote,
			Normalized: "DIRECTORS_CUT",
			TokenStart: i,
			TokenEnd:   j + 1,
			Confidence: 0.72,
			Source:     model.AttrSourceEditionToken,
		})
		tokens[i].Removed = true
		tokens[j].Removed = true
	}
}

func extractExtendedCutEditionAttrsAndRemoveFromTokens(tokens []model.Token, out *[]model.Attribute) {
	if out == nil || len(tokens) == 0 {
		return
	}
	for i := 0; i < len(tokens); i++ {
		if tokens[i].Removed {
			continue
		}
		if compactEditionWord(tokens[i].Text) != "extended" {
			continue
		}
		if !isInBracket(tokens, i) && !isInParen(tokens, i) {
			continue
		}
		j := nextAliveToken(tokens, i+1)
		if j < 0 || compactEditionWord(tokens[j].Text) != "cut" {
			continue
		}
		*out = append(*out, model.Attribute{
			Type:       model.AttrNote,
			Normalized: "EXTENDED_CUT",
			TokenStart: i,
			TokenEnd:   j + 1,
			Confidence: 0.72,
			Source:     model.AttrSourceEditionToken,
		})
		tokens[i].Removed = true
		tokens[j].Removed = true
	}
}

func extractRemasteredEditionAttrsAndRemoveFromTokens(tokens []model.Token, out *[]model.Attribute) {
	if out == nil || len(tokens) == 0 {
		return
	}
	for i := 0; i < len(tokens); i++ {
		if tokens[i].Removed {
			continue
		}
		cur := compactEditionWord(tokens[i].Text)
		if cur != "remaster" && cur != "remastered" {
			continue
		}
		if !isInBracket(tokens, i) && !isInParen(tokens, i) {
			continue
		}
		end := i
		norm := "REMASTERED"
		if j := nextAliveToken(tokens, i+1); j >= 0 {
			ver := normalizeEditionToken(tokens[j].Text, false)
			if ver == "ver" || ver == "version" {
				end = j
				norm = "REMASTERED_VER"
				if n := nextAliveToken(tokens, j+1); n >= 0 && strings.TrimSpace(tokens[n].Text) == "." {
					end = n
				}
			}
		}
		*out = append(*out, model.Attribute{
			Type:       model.AttrNote,
			Normalized: norm,
			TokenStart: i,
			TokenEnd:   end + 1,
			Confidence: 0.72,
			Source:     model.AttrSourceEditionToken,
		})
		for k := i; k <= end; k++ {
			tokens[k].Removed = true
		}
	}
}

func isLikelyEncodeEditionAnchor(norm string) bool {
	if norm == "" {
		return false
	}
	switch norm {
	case "x264", "x265", "h264", "h265", "hevc", "avc", "mpeg2", "av1":
		return true
	}
	if strings.HasSuffix(norm, "bit") {
		n := strings.TrimSuffix(norm, "bit")
		return n == "8" || n == "10" || n == "12"
	}
	return false
}

func normalizeEditionToken(raw string, foldConnectors bool) string {
	s := strings.TrimSpace(raw)
	s = strings.TrimFunc(s, func(r rune) bool {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			return false
		}
		return true
	})
	s = strings.ToLower(s)
	if foldConnectors {
		s = strings.ReplaceAll(s, "_", "")
		s = strings.ReplaceAll(s, "-", "")
	}
	return s
}

func compactEditionWord(raw string) string {
	s := strings.TrimSpace(strings.ToLower(raw))
	if s == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func nextAliveToken(tokens []model.Token, from int) int {
	for i := from; i < len(tokens); i++ {
		if tokens[i].Removed {
			continue
		}
		if strings.TrimSpace(tokens[i].Text) == "" {
			continue
		}
		return i
	}
	return -1
}

func prevAliveToken(tokens []model.Token, from int) int {
	for i := from; i >= 0; i-- {
		if tokens[i].Removed {
			continue
		}
		txt := strings.TrimSpace(tokens[i].Text)
		if txt == "" {
			continue
		}
		if txt == "-" || txt == "_" || txt == "/" || txt == "." || txt == "|" || txt == "｜" {
			continue
		}
		return i
	}
	return -1
}

func shouldUseUnderscoreEditionRoute(rawTitle string) bool {
	toks := tokenize.Tokenize(rawTitle)
	if len(toks) < 3 || toks[0] != "[" || toks[2] != "]" {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(toks[1]), "shincaps")
}

var tailGroupPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]{2,20}$`)
var tailGroupHyphenPrefixPattern = regexp.MustCompile(`(?i)^(?:x?264|x?265|h?264|h?265|\d\.\d|pcm|flac|aac|ac3|eac3|ddp|dts|opus|truehd|atmos)(?:\d\.\d)?$`)
var tailGroupTechTokenPattern = regexp.MustCompile(`(?i)^(?:dts(?:hd|hdma)?|hdma|truehd|eac3|ac3|flac|opus|pcm|ma\d{1,2}p|ddp\d{0,2})$`)

// Regular expressions hoisted out of function bodies so they are compiled once at init.
var (
	reExtractCompositeEditionNotes1                  = regexp.MustCompile(`[&＆+/／|｜,，]+`)
	reExtractRestoreEditionAttrsAndRemoveFromTokens1 = regexp.MustCompile(`(?i)(?:数字修复|數字修復|修复版|修復版|修复|修復|重制|重製|restore(?:d)?|remaster(?:ed)?)`)
)
