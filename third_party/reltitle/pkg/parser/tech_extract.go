package parser

import (
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/wangkayu/reltitle/pkg/model"
)

func extractWxHResolutionAttrsAndRemoveFromTokens(tokens []model.Token, attrs *[]model.Attribute) {
	if attrs == nil || len(tokens) == 0 {
		return
	}
	techStart, hasTechStart := firstTechTokenIndexFromAttrs(*attrs)
	for i := 0; i < len(tokens); i++ {
		if tokens[i].Removed {
			continue
		}
		if hasTechStart && i+2 < techStart {
			continue
		}
		t := strings.TrimSpace(tokens[i].Text)
		if t == "" {
			continue
		}
		m := wxhResolutionPattern.FindStringSubmatch(t)
		if len(m) != 4 {
			continue
		}
		w, _ := strconv.Atoi(m[1])
		h, _ := strconv.Atoi(m[2])
		if w <= 0 || h <= 0 {
			continue
		}
		// Keep orientation-insensitive handling.
		if w < h {
			w, h = h, w
		}
		// Map common sizes to p-based resolution.
		norm := ""
		switch {
		case w == 3840 && h == 2160:
			norm = "2160p"
		case w == 1920 && h == 1080:
			norm = "1080p"
		case w == 1280 && h == 720:
			norm = "720p"
		case w == 720 && h == 480:
			norm = "480p"
		}
		if norm == "" {
			norm = strconv.Itoa(w) + "x" + strconv.Itoa(h)
		}
		*attrs = append(*attrs, model.Attribute{Type: model.AttrResolution, Normalized: norm, TokenStart: i, TokenEnd: i + 1, Confidence: 0.75, Source: model.AttrSourceResolutionWxH})
		tokens[i].Removed = true
	}
}

var stableTechResolutionPattern = regexp.MustCompile(`(?i)^(\d{3,4})p$`)
var stableTechBitDepthPattern = regexp.MustCompile(`(?i)^(8|10|12)\s*bit$`)
var stableTechCodecBitDepthPattern = regexp.MustCompile(`(?i)^(hevc|x265|h265|x264|h264)\s*[-_ ]?\s*0*(8|10|12)(?:bit|b)?$`)
var maProfileBitDepthPattern = regexp.MustCompile(`(?i)^ma0*(8|10|12)p$`)
var looseBitDepthPattern = regexp.MustCompile(`(?i)^0*(8|10|12)\s*(?:-|_)?\s*bit[\.,，:：;；!?]?$`)
var stableTechSplitPattern = regexp.MustCompile(`[+_/\.]`)

func (p *Parser) extractStableTechSpanComboAttrsAndRemoveFromTokens(tokens []model.Token, attrs *[]model.Attribute) {
	if attrs == nil || len(tokens) == 0 {
		return
	}
	for i := 0; i < len(tokens); i++ {
		if tokens[i].Text != "[" && tokens[i].Text != "【" && tokens[i].Text != "(" && tokens[i].Text != "（" {
			continue
		}

		open := i
		close := -1
		if tokens[i].Text == "[" || tokens[i].Text == "【" {
			openTok := tokens[i].Text
			closeTok := "]"
			if openTok == "【" {
				closeTok = "】"
			}
			depth := 0
			for j := i; j < len(tokens); j++ {
				t := strings.TrimSpace(tokens[j].Text)
				if t == openTok {
					depth++
					continue
				}
				if t == closeTok {
					depth--
					if depth == 0 {
						close = j
						break
					}
				}
			}
			if close <= i {
				continue
			}
		} else {
			_, c, _, ok := parseParenBlock(tokens, i, len(tokens))
			if !ok || c <= i {
				continue
			}
			close = c
		}
		if close <= open+1 {
			i = close
			continue
		}

		blockAttrs := make([]model.Attribute, 0, 6)
		recognized := make(map[int]bool)
		nonSepCount := 0
		coreCount := 0

		for j := open + 1; j < close; j++ {
			if tokens[j].Removed {
				continue
			}
			txt := strings.TrimSpace(tokens[j].Text)
			if txt == "" || isTechDelimiterToken(txt) {
				continue
			}
			nonSepCount++
			local, ok := p.inferStableTechTokenAttrs(txt, j)
			if !ok {
				continue
			}
			for _, a := range local {
				if isStableTechCoreType(a.Type) {
					coreCount++
				}
				blockAttrs = append(blockAttrs, a)
			}
			recognized[j] = true
		}

		if nonSepCount == 0 || len(blockAttrs) < 2 || coreCount == 0 {
			i = close
			continue
		}
		if float64(len(recognized))/float64(nonSepCount) < 0.6 {
			i = close
			continue
		}

		*attrs = append(*attrs, blockAttrs...)
		for idx := range recognized {
			tokens[idx].Removed = true
		}
		for j := open + 1; j < close; j++ {
			if tokens[j].Removed {
				continue
			}
			txt := strings.TrimSpace(tokens[j].Text)
			if !isTechDelimiterToken(txt) {
				continue
			}
			left := j - 1
			right := j + 1
			if left > open && right < close && recognized[left] && recognized[right] {
				tokens[j].Removed = true
			}
		}
		i = close
	}
}

func isTechDelimiterToken(s string) bool {
	switch strings.TrimSpace(s) {
	case "-", "_", "+", ",", "，", ".", "/", "／", "&", "＆":
		return true
	default:
		return false
	}
}

func isStableTechCoreType(t model.AttrType) bool {
	switch t {
	case model.AttrResolution, model.AttrSourceType, model.AttrVideoCodec, model.AttrAudioCodec, model.AttrPlatform:
		return true
	default:
		return false
	}
}

func (p *Parser) inferStableTechTokenAttrs(raw string, idx int) ([]model.Attribute, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, false
	}
	clean := strings.Trim(raw, "[](){}<>【】（）.,，:：;；'\"")
	clean = strings.TrimSpace(clean)
	if clean == "" {
		return nil, false
	}

	parts := []string{clean}
	if stableTechSplitPattern.MatchString(clean) {
		tmp := stableTechSplitPattern.Split(clean, -1)
		parts = make([]string, 0, len(tmp))
		for _, part := range tmp {
			pp := strings.TrimSpace(part)
			if pp == "" {
				continue
			}
			parts = append(parts, pp)
		}
		if len(parts) == 0 {
			parts = []string{clean}
		}
	}

	out := make([]model.Attribute, 0, 3)
	for _, part := range parts {
		pp := strings.TrimSpace(part)
		if pp == "" {
			continue
		}
		low := strings.ToLower(pp)

		if m := stableTechResolutionPattern.FindStringSubmatch(low); len(m) == 2 {
			out = append(out, model.Attribute{Type: model.AttrResolution, Normalized: m[1] + "p", TokenStart: idx, TokenEnd: idx + 1, Confidence: 0.76, Source: model.AttrSourceTechComboSpan})
			continue
		}

		if mm := stableTechBitDepthPattern.FindStringSubmatch(low); len(mm) == 2 {
			out = append(out, model.Attribute{Type: model.AttrBitDepth, Normalized: mm[1] + "bit", TokenStart: idx, TokenEnd: idx + 1, Confidence: 0.74, Source: model.AttrSourceTechComboSpan})
			continue
		}
		if mm := stableTechCodecBitDepthPattern.FindStringSubmatch(low); len(mm) == 3 {
			if norm, ok := p.normalizeVideoCodecToken(mm[1]); ok {
				out = append(out,
					model.Attribute{Type: model.AttrVideoCodec, Normalized: norm, TokenStart: idx, TokenEnd: idx + 1, Confidence: 0.76, Source: model.AttrSourceTechComboSpan},
					model.Attribute{Type: model.AttrBitDepth, Normalized: mm[2] + "bit", TokenStart: idx, TokenEnd: idx + 1, Confidence: 0.74, Source: model.AttrSourceTechComboSpan},
				)
				continue
			}
		}
		if low == "remux" {
			out = append(out, model.Attribute{Type: model.AttrType("remux"), Normalized: "REMUX", TokenStart: idx, TokenEnd: idx + 1, Confidence: 0.74, Source: model.AttrSourceTechComboSpan})
			continue
		}
		if norm, ok := p.sourceTypeCanonical[low]; ok && strings.TrimSpace(norm) != "" {
			out = append(out, model.Attribute{Type: model.AttrSourceType, Normalized: norm, TokenStart: idx, TokenEnd: idx + 1, Confidence: 0.76, Source: model.AttrSourceTechComboSpan})
			continue
		}
		if norm, ok := p.platformCanonical[low]; ok && strings.TrimSpace(norm) != "" {
			out = append(out, model.Attribute{Type: model.AttrPlatform, Normalized: norm, TokenStart: idx, TokenEnd: idx + 1, Confidence: 0.74, Source: model.AttrSourceTechComboSpan})
			continue
		}
		if norm, ok := p.normalizeVideoCodecToken(low); ok {
			out = append(out, model.Attribute{Type: model.AttrVideoCodec, Normalized: norm, TokenStart: idx, TokenEnd: idx + 1, Confidence: 0.76, Source: model.AttrSourceTechComboSpan})
			continue
		}
		if norm, ok := p.normalizeAudioCodecToken(low); ok {
			out = append(out, model.Attribute{Type: model.AttrAudioCodec, Normalized: norm, TokenStart: idx, TokenEnd: idx + 1, Confidence: 0.76, Source: model.AttrSourceTechComboSpan})
			continue
		}
		if norm, ok := p.normalizeContainerToken(low); ok {
			out = append(out, model.Attribute{Type: model.AttrContainer, Normalized: norm, TokenStart: idx, TokenEnd: idx + 1, Confidence: 0.74, Source: model.AttrSourceTechComboSpan})
			continue
		}
		if norm, ok := p.normalizeAudioFlagToken(low); ok {
			out = append(out, model.Attribute{Type: model.AttrAudioFlag, Normalized: norm, TokenStart: idx, TokenEnd: idx + 1, Confidence: 0.7, Source: model.AttrSourceTechComboSpan})
			continue
		}
	}
	if len(out) == 0 {
		return nil, false
	}
	return out, true
}

func extractLooseBitDepthAttrsAndRemoveFromTokens(tokens []model.Token, attrs *[]model.Attribute) {
	if attrs == nil || len(tokens) == 0 {
		return
	}
	seen := make(map[string]struct{}, 3)
	for _, a := range *attrs {
		if a.Type != model.AttrBitDepth {
			continue
		}
		n := strings.TrimSpace(strings.ToLower(a.Normalized))
		if n == "" {
			continue
		}
		seen[n] = struct{}{}
	}
	for i := 0; i < len(tokens); i++ {
		if tokens[i].Removed {
			continue
		}
		txt := strings.TrimSpace(tokens[i].Text)
		if txt == "" {
			continue
		}
		mm := looseBitDepthPattern.FindStringSubmatch(txt)
		if len(mm) != 2 {
			continue
		}
		norm := mm[1] + "bit"
		key := strings.ToLower(norm)
		if _, ok := seen[key]; ok {
			tokens[i].Removed = true
			continue
		}
		*attrs = append(*attrs, model.Attribute{
			Type:       model.AttrBitDepth,
			Normalized: norm,
			TokenStart: i,
			TokenEnd:   i + 1,
			Confidence: 0.72,
			Source:     model.AttrSourceBitDepthLoose,
		})
		seen[key] = struct{}{}
		tokens[i].Removed = true
	}
}

func (p *Parser) extractLooseCodecBitDepthAttrsAndRemoveFromTokens(tokens []model.Token, attrs *[]model.Attribute) {
	if attrs == nil || len(tokens) == 0 || p == nil {
		return
	}
	seenVideo := make(map[string]struct{}, 2)
	seenBit := make(map[string]struct{}, 2)
	for _, a := range *attrs {
		if a.Type == model.AttrVideoCodec {
			n := strings.ToLower(strings.TrimSpace(a.Normalized))
			if n != "" {
				seenVideo[n] = struct{}{}
			}
			continue
		}
		if a.Type == model.AttrBitDepth {
			n := strings.ToLower(strings.TrimSpace(a.Normalized))
			if n != "" {
				seenBit[n] = struct{}{}
			}
		}
	}
	for i := 0; i < len(tokens); i++ {
		if tokens[i].Removed {
			continue
		}
		txt := strings.ToLower(strings.TrimSpace(tokens[i].Text))
		if txt == "" {
			continue
		}
		if !isInBracket(tokens, i) && !isInParen(tokens, i) {
			continue
		}
		mm := stableTechCodecBitDepthPattern.FindStringSubmatch(txt)
		if len(mm) != 3 {
			continue
		}
		vcodec, ok := p.normalizeVideoCodecToken(mm[1])
		if !ok || strings.TrimSpace(vcodec) == "" {
			continue
		}
		bit := mm[2] + "bit"
		if _, ok := seenVideo[strings.ToLower(vcodec)]; !ok {
			*attrs = append(*attrs, model.Attribute{
				Type:       model.AttrVideoCodec,
				Normalized: vcodec,
				TokenStart: i,
				TokenEnd:   i + 1,
				Confidence: 0.74,
				Source:     model.AttrSourceTechComboToken,
			})
			seenVideo[strings.ToLower(vcodec)] = struct{}{}
		}
		if _, ok := seenBit[strings.ToLower(bit)]; !ok {
			*attrs = append(*attrs, model.Attribute{
				Type:       model.AttrBitDepth,
				Normalized: bit,
				TokenStart: i,
				TokenEnd:   i + 1,
				Confidence: 0.72,
				Source:     model.AttrSourceTechComboToken,
			})
			seenBit[strings.ToLower(bit)] = struct{}{}
		}
		tokens[i].Removed = true
	}
}

func isLikelyTechContextToken(s string) bool {
	s = strings.TrimSpace(strings.ToUpper(s))
	if s == "" {
		return false
	}
	if reIsLikelyTechContextToken1.MatchString(s) {
		return true
	}
	switch s {
	case "WEB", "WEBRIP", "WEB-DL", "WEBDL",
		"BD", "BDRIP", "BLURAY", "UHD",
		"X264", "X265", "H264", "H265", "AVC", "HEVC",
		"AAC", "AC3", "EAC3", "DDP", "FLAC",
		"MKV", "MP4", "AVI", "TS", "M2TS",
		"MULTI", "CUSTOM", "VOSTFR", "VOSTEN", "VF", "DUAL",
		"REMUX", "OVA", "OAD", "ONA",
		"SET", "ISO", "ISOS", "NTSC", "PAL", "BOOTLEG",
		"AUDIO", "LATINO", "R2J", "TOKUTEN":
		return true
	default:
		return false
	}
}

func hasNearbyTechContext(tokens []model.Token, center int, radius int) bool {
	if len(tokens) == 0 {
		return false
	}
	if radius < 1 {
		radius = 1
	}
	start := center - radius
	if start < 0 {
		start = 0
	}
	end := center + radius + 1
	if end > len(tokens) {
		end = len(tokens)
	}
	for i := start; i < end; i++ {
		if i == center {
			continue
		}
		if isLikelyTechContextToken(tokens[i].Text) {
			return true
		}
	}
	return false
}

func hasNearbySpecialReleaseToken(tokens []model.Token, center int, radius int) bool {
	if len(tokens) == 0 {
		return false
	}
	if radius < 1 {
		radius = 1
	}
	start := center - radius
	if start < 0 {
		start = 0
	}
	end := center + radius + 1
	if end > len(tokens) {
		end = len(tokens)
	}
	for i := start; i < end; i++ {
		if i == center || tokens[i].Removed {
			continue
		}
		switch strings.ToUpper(strings.TrimSpace(tokens[i].Text)) {
		case "NCOP", "NCED", "PV", "CM", "OP", "ED":
			return true
		}
	}
	return false
}

func (p *Parser) hasNearbyAudioCodecToken(tokens []model.Token, center int, radius int) bool {
	if p == nil || len(tokens) == 0 {
		return false
	}
	if radius < 1 {
		radius = 1
	}
	start := center - radius
	if start < 0 {
		start = 0
	}
	end := center + radius + 1
	if end > len(tokens) {
		end = len(tokens)
	}
	for i := start; i < end; i++ {
		if i == center || tokens[i].Removed {
			continue
		}
		base := strings.TrimSpace(tokens[i].Text)
		if base == "" {
			continue
		}
		if _, ok := p.normalizeAudioCodecToken(base); ok {
			return true
		}
		if i+2 < len(tokens) && !tokens[i+1].Removed && !tokens[i+2].Removed && strings.TrimSpace(tokens[i+1].Text) == "-" {
			combined := strings.TrimSpace(tokens[i].Text) + "-" + strings.TrimSpace(tokens[i+2].Text)
			if _, ok := p.normalizeAudioCodecToken(combined); ok {
				return true
			}
		}
	}
	return false
}

func hasAudioCodecAttrNorm(attrs []model.Attribute, norm string) bool {
	norm = strings.ToUpper(strings.TrimSpace(norm))
	if norm == "" {
		return false
	}
	for _, a := range attrs {
		if a.Type != model.AttrAudioCodec {
			continue
		}
		if strings.ToUpper(strings.TrimSpace(a.Normalized)) == norm {
			return true
		}
	}
	return false
}

func hasSourceTypeAttr(attrs []model.Attribute) bool {
	for _, a := range attrs {
		if a.Type == model.AttrSourceType && strings.TrimSpace(a.Normalized) != "" {
			return true
		}
	}
	return false
}

func extractDashedAudioCodecAttrsAndRemoveFromTokens(tokens []model.Token, attrs *[]model.Attribute) {
	if attrs == nil || len(tokens) < 3 {
		return
	}
	for i := 0; i < len(tokens); i++ {
		if tokens[i].Removed {
			continue
		}
		// E-AC-3 => EAC3
		if i+4 < len(tokens) &&
			strings.EqualFold(strings.TrimSpace(tokens[i].Text), "E") &&
			strings.TrimSpace(tokens[i+1].Text) == "-" &&
			strings.EqualFold(strings.TrimSpace(tokens[i+2].Text), "AC") &&
			strings.TrimSpace(tokens[i+3].Text) == "-" &&
			strings.EqualFold(strings.TrimSpace(tokens[i+4].Text), "3") {
			if !hasAudioCodecAttrNorm(*attrs, "EAC3") {
				*attrs = append(*attrs, model.Attribute{
					Type:       model.AttrAudioCodec,
					Normalized: "EAC3",
					TokenStart: i,
					TokenEnd:   i + 5,
					Confidence: 0.74,
					Source:     model.AttrSourceCodecDashed,
				})
			}
			for k := i; k <= i+4; k++ {
				tokens[k].Removed = true
			}
			i += 4
			continue
		}
		// AC-3 => AC3
		if i+2 < len(tokens) &&
			strings.EqualFold(strings.TrimSpace(tokens[i].Text), "AC") &&
			strings.TrimSpace(tokens[i+1].Text) == "-" &&
			strings.EqualFold(strings.TrimSpace(tokens[i+2].Text), "3") {
			if !hasAudioCodecAttrNorm(*attrs, "AC3") {
				*attrs = append(*attrs, model.Attribute{
					Type:       model.AttrAudioCodec,
					Normalized: "AC3",
					TokenStart: i,
					TokenEnd:   i + 3,
					Confidence: 0.74,
					Source:     model.AttrSourceCodecDashed,
				})
			}
			for k := i; k <= i+2; k++ {
				tokens[k].Removed = true
			}
			i += 2
		}
	}
}

func (p *Parser) extractMaProfileAudioCodecAttrsAndRemoveFromTokens(tokens []model.Token, attrs *[]model.Attribute) {
	if p == nil || attrs == nil || len(tokens) < 3 {
		return
	}
	for i := 0; i+2 < len(tokens); i++ {
		if tokens[i].Removed {
			continue
		}
		base := strings.TrimSpace(tokens[i].Text)
		m := maProfileBitDepthPattern.FindStringSubmatch(base)
		if len(m) != 2 {
			continue
		}
		if tokens[i+1].Removed || tokens[i+2].Removed {
			continue
		}
		sep := strings.TrimSpace(tokens[i+1].Text)
		if sep != "_" && sep != "-" {
			continue
		}
		codecNorm, ok := p.normalizeAudioCodecToken(strings.TrimSpace(tokens[i+2].Text))
		if !ok || strings.TrimSpace(codecNorm) == "" {
			continue
		}
		if !isInBracket(tokens, i) && !isInParen(tokens, i) && !hasNearbyTechContext(tokens, i, 4) && i < len(tokens)-4 {
			continue
		}
		bit := strings.TrimLeft(m[1], "0")
		if bit == "" {
			bit = m[1]
		}
		*attrs = append(*attrs, model.Attribute{
			Type:       model.AttrBitDepth,
			Normalized: bit + "bit",
			TokenStart: i,
			TokenEnd:   i + 1,
			Confidence: 0.72,
			Source:     model.AttrSourceTechComboToken,
		})
		if !hasAudioCodecAttrNorm(*attrs, codecNorm) {
			*attrs = append(*attrs, model.Attribute{
				Type:       model.AttrAudioCodec,
				Normalized: codecNorm,
				TokenStart: i + 2,
				TokenEnd:   i + 3,
				Confidence: 0.72,
				Source:     model.AttrSourceTechComboToken,
			})
		}
		tokens[i].Removed = true
		tokens[i+1].Removed = true
		tokens[i+2].Removed = true
		i += 2
	}
}

func (p *Parser) extractStandaloneMaProfileBitDepthAttrsAndRemoveFromTokens(tokens []model.Token, attrs *[]model.Attribute) {
	if attrs == nil || len(tokens) == 0 {
		return
	}
	seenBit := make(map[string]struct{}, 2)
	for _, a := range *attrs {
		if a.Type != model.AttrBitDepth {
			continue
		}
		n := strings.ToLower(strings.TrimSpace(a.Normalized))
		if n != "" {
			seenBit[n] = struct{}{}
		}
	}
	for i := 0; i < len(tokens); i++ {
		if tokens[i].Removed {
			continue
		}
		base := strings.TrimSpace(tokens[i].Text)
		m := maProfileBitDepthPattern.FindStringSubmatch(base)
		if len(m) != 2 {
			continue
		}
		if !isInBracket(tokens, i) && !isInParen(tokens, i) && !hasNearbyTechContext(tokens, i, 4) && !(p != nil && p.hasNearbyAudioCodecToken(tokens, i, 4)) {
			continue
		}
		bit := strings.TrimLeft(m[1], "0")
		if bit == "" {
			bit = m[1]
		}
		norm := bit + "bit"
		if _, ok := seenBit[strings.ToLower(norm)]; !ok {
			*attrs = append(*attrs, model.Attribute{
				Type:       model.AttrBitDepth,
				Normalized: norm,
				TokenStart: i,
				TokenEnd:   i + 1,
				Confidence: 0.72,
				Source:     model.AttrSourceTechComboToken,
			})
			seenBit[strings.ToLower(norm)] = struct{}{}
		}
		tokens[i].Removed = true
	}
}

var crc8HexTokenPattern = regexp.MustCompile(`(?i)^[0-9a-f]{8}$`)

func extractCRCAttrsAndRemoveFromTokens(tokens []model.Token, attrs *[]model.Attribute) {
	if attrs == nil || len(tokens) == 0 {
		return
	}
	seen := make(map[string]struct{}, 4)
	for _, a := range *attrs {
		if a.Type != model.AttrCRC {
			continue
		}
		n := strings.ToUpper(strings.TrimSpace(a.Normalized))
		if n != "" {
			seen[n] = struct{}{}
		}
	}
	for i := 0; i < len(tokens); i++ {
		if tokens[i].Removed {
			continue
		}
		raw := strings.TrimSpace(tokens[i].Text)
		if !crc8HexTokenPattern.MatchString(raw) {
			continue
		}
		low := strings.ToLower(raw)
		if !strings.ContainsAny(low, "abcdef") {
			isolatedTailBlock := i > 0 && i+1 < len(tokens) && tokens[i-1].Text == "[" && tokens[i+1].Text == "]" && hasTechAttr(*attrs)
			if isolatedTailBlock {
				for j := i + 2; j < len(tokens); j++ {
					if tokens[j].Text != "." && !reFileExtSuffix.MatchString("."+tokens[j].Text) {
						isolatedTailBlock = false
						break
					}
				}
			}
			_, dateErr := time.Parse("20060102", raw)
			if !isolatedTailBlock || dateErr == nil || looksLikeDateOrTimeText(raw) {
				continue
			}
		}
		if !isInBracket(tokens, i) && !isInParen(tokens, i) {
			continue
		}
		if !hasNearbyTechContext(tokens, i, 10) && i < len(tokens)-6 {
			continue
		}
		norm := strings.ToUpper(raw)
		if _, ok := seen[norm]; !ok {
			*attrs = append(*attrs, model.Attribute{
				Type:       model.AttrCRC,
				Normalized: norm,
				TokenStart: i,
				TokenEnd:   i + 1,
				Confidence: 0.82,
				Source:     model.AttrSourceCRCToken,
			})
			seen[norm] = struct{}{}
		}
		tokens[i].Removed = true
	}
}

var volumeWordPattern = regexp.MustCompile(`(?i)^vol(?:ume)?\.?$`)
var shortNumericPattern = regexp.MustCompile(`^\d{1,3}$`)
var numericRangeTokenPattern = regexp.MustCompile(`^\d{1,4}\s*[-~～−–]\s*\d{1,4}$`)

func hasDiscLikeSourceTypeAttr(attrs []model.Attribute) bool {
	for _, a := range attrs {
		if a.Type != model.AttrSourceType {
			continue
		}
		n := strings.ToUpper(strings.TrimSpace(a.Normalized))
		n = strings.ReplaceAll(n, "-", "")
		n = strings.ReplaceAll(n, "_", "")
		switch {
		case strings.Contains(n, "BD"),
			strings.Contains(n, "BLURAY"),
			strings.Contains(n, "DVD"),
			strings.Contains(n, "VHS"),
			strings.Contains(n, "LASERDISC"),
			strings.Contains(n, "LD"),
			strings.Contains(n, "REMUX"):
			return true
		}
	}
	return false
}

func hasEpisodeRangeMarker(tokens []model.Token, idx int) bool {
	next := nextAliveToken(tokens, idx+1)
	if next < 0 {
		return false
	}
	marker := strings.ToLower(strings.TrimSpace(tokens[next].Text))
	switch marker {
	case "-", "~", "～", "−", "–", "to", "&", "＆", ",", "，", "/", "／":
		return true
	default:
		return false
	}
}

func (p *Parser) extractVolumeEpisodeMetaMarkersAndRemoveFromTokens(tokens []model.Token, attrs *[]model.Attribute) {
	if len(tokens) == 0 {
		return
	}
	hasDiscSource := false
	if attrs != nil {
		hasDiscSource = hasDiscLikeSourceTypeAttr(*attrs)
	}
	for i := 0; i < len(tokens); i++ {
		if tokens[i].Removed {
			continue
		}
		cur := strings.TrimSpace(tokens[i].Text)
		curLower := strings.ToLower(cur)

		// Consume metadata prefixes like "Episodes 01-10" in package/tech context.
		if curLower == "episode" || curLower == "episodes" {
			j := nextAliveToken(tokens, i+1)
			if j < 0 {
				continue
			}
			next := strings.TrimSpace(tokens[j].Text)
			if (shortNumericPattern.MatchString(next) || numericRangeTokenPattern.MatchString(next)) &&
				(isInBracket(tokens, i) || isInParen(tokens, i) || hasEpisodeRangeMarker(tokens, j) || hasDiscSource) {
				tokens[i].Removed = true
			}
			continue
		}

		// Consume volume marker word in clear disc/batch packaging context.
		if !volumeWordPattern.MatchString(cur) {
			continue
		}
		j := nextAliveToken(tokens, i+1)
		dotIdx := -1
		if j >= 0 && strings.TrimSpace(tokens[j].Text) == "." {
			dotIdx = j
			j = nextAliveToken(tokens, j+1)
		}
		if j < 0 {
			continue
		}
		next := strings.TrimSpace(tokens[j].Text)
		if !shortNumericPattern.MatchString(next) {
			continue
		}
		if !(isInBracket(tokens, i) || isInParen(tokens, i) || hasDiscSource) {
			continue
		}
		tokens[i].Removed = true
		if dotIdx >= 0 {
			tokens[dotIdx].Removed = true
		}
	}
}

func (p *Parser) extractStandaloneSourceTypeAttrsAndRemoveFromTokens(tokens []model.Token, attrs *[]model.Attribute) {
	if p == nil || attrs == nil || len(tokens) == 0 || len(p.sourceTypeCanonical) == 0 {
		return
	}
	hasSource := hasSourceTypeAttr(*attrs)
	for i := 0; i < len(tokens); i++ {
		if tokens[i].Removed {
			continue
		}
		txt := strings.ToUpper(strings.TrimSpace(tokens[i].Text))
		if txt == "" {
			continue
		}
		matched := false
		spanEnd := i + 1
		norm := ""
		if v, ok := p.sourceTypeCanonical[strings.ToLower(txt)]; ok && strings.TrimSpace(v) != "" {
			norm = strings.TrimSpace(v)
			matched = true
		}
		if i+2 < len(tokens) && !tokens[i+1].Removed && !tokens[i+2].Removed && strings.TrimSpace(tokens[i+1].Text) == "-" {
			right := strings.TrimSpace(tokens[i+2].Text)
			combinedKey := strings.ToLower(txt + "-" + right)
			if v, ok := p.sourceTypeCanonical[combinedKey]; ok && strings.TrimSpace(v) != "" {
				spanEnd = i + 3
				norm = strings.TrimSpace(v)
				matched = true
			} else if strings.EqualFold(txt, "DVD") && (strings.EqualFold(right, "ISO") || strings.EqualFold(right, "ISOS") || strings.EqualFold(right, "REMUX")) {
				if v, ok := p.sourceTypeCanonical["dvd"]; ok && strings.TrimSpace(v) != "" {
					spanEnd = i + 3
					norm = strings.TrimSpace(v)
					matched = true
				}
			}
		}
		if !matched || norm == "" {
			continue
		}
		techCtx := isInBracket(tokens, i) || isInParen(tokens, i) || hasNearbyTechContext(tokens, i, 4)
		specialCtx := hasNearbySpecialReleaseToken(tokens, i, 3)
		if !techCtx && !specialCtx {
			continue
		}
		if !hasSource && !specialCtx {
			*attrs = append(*attrs, model.Attribute{
				Type:       model.AttrSourceType,
				Normalized: norm,
				TokenStart: i,
				TokenEnd:   spanEnd,
				Confidence: 0.72,
				Source:     model.AttrSourceSourceStandalone,
			})
			hasSource = true
		}
		for k := i; k < spanEnd && k < len(tokens); k++ {
			tokens[k].Removed = true
		}
	}
}

func (p *Parser) extractStandaloneAudioCodecAttrsAndRemoveFromTokens(tokens []model.Token, attrs *[]model.Attribute) {
	if p == nil || attrs == nil || len(tokens) == 0 || len(p.audioCodecCanonical) == 0 {
		return
	}
	for i := 0; i < len(tokens); i++ {
		if tokens[i].Removed {
			continue
		}
		base := strings.TrimSpace(tokens[i].Text)
		if base == "" {
			continue
		}
		spanEnd := i + 1
		norm := ""
		if i+2 < len(tokens) && !tokens[i+1].Removed && !tokens[i+2].Removed && strings.TrimSpace(tokens[i+1].Text) == "-" {
			combined := strings.TrimSpace(tokens[i].Text) + "-" + strings.TrimSpace(tokens[i+2].Text)
			if v, ok := p.normalizeAudioCodecToken(combined); ok && strings.TrimSpace(v) != "" {
				norm = strings.TrimSpace(v)
				spanEnd = i + 3
			}
		}
		if norm == "" {
			if v, ok := p.normalizeAudioCodecToken(base); ok && strings.TrimSpace(v) != "" {
				norm = strings.TrimSpace(v)
			}
		}
		if norm == "" {
			continue
		}
		if !isInBracket(tokens, i) && !isInParen(tokens, i) && !hasNearbyTechContext(tokens, i, 4) && !p.hasNearbyAudioCodecToken(tokens, i, 4) {
			continue
		}
		if hasAudioCodecAttrNorm(*attrs, norm) {
			continue
		}
		*attrs = append(*attrs, model.Attribute{
			Type:       model.AttrAudioCodec,
			Normalized: norm,
			TokenStart: i,
			TokenEnd:   spanEnd,
			Confidence: 0.72,
			Source:     model.AttrSourceAudioCodecStandalone,
		})
		for k := i; k < spanEnd && k < len(tokens); k++ {
			tokens[k].Removed = true
		}
	}
}

func isTechTokenConsumeAttrType(t model.AttrType) bool {
	switch t {
	case model.AttrResolution,
		model.AttrSourceType,
		model.AttrVideoCodec,
		model.AttrAudioCodec,
		model.AttrBitDepth,
		model.AttrContainer,
		model.AttrPlatform:
		return true
	default:
		return false
	}
}

func consumeTokensByTechAttrSpans(tokens []model.Token, attrs []model.Attribute) {
	if len(tokens) == 0 || len(attrs) == 0 {
		return
	}
	technical := make([]bool, len(tokens))
	for _, a := range attrs {
		if !isTechTokenConsumeAttrType(a.Type) {
			continue
		}
		if a.TokenStart < 0 || a.TokenEnd <= a.TokenStart || a.TokenStart >= len(tokens) {
			continue
		}
		end := a.TokenEnd
		if end > len(tokens) {
			end = len(tokens)
		}
		for i := a.TokenStart; i < end; i++ {
			tokens[i].Removed = true
			technical[i] = true
		}
	}
	for i := 1; i+1 < len(tokens); i++ {
		if tokens[i].Text == "-" && !tokens[i].Protected && isInBracket(tokens, i) && technical[i-1] && technical[i+1] {
			tokens[i].Removed = true
		}
	}
}

func consumeTokensByEPSlotPrimarySpan(tokens []model.Token, epslot *model.EPSlot) {
	if len(tokens) == 0 || epslot == nil || epslot.Primary == nil {
		return
	}
	start := epslot.Primary.TokenStart
	end := epslot.Primary.TokenEnd
	if start < 0 || end <= start || start >= len(tokens) {
		return
	}
	if end > len(tokens) {
		end = len(tokens)
	}
	if epslot.Primary.Kind == model.EPKindRange && end == start+1 {
		if start+2 < len(tokens) && tokens[start+1].Text == "-" && !tokens[start+2].Protected {
			left := sxxExxPattern.FindStringSubmatch(tokens[start].Text)
			right := strings.TrimPrefix(strings.ToUpper(tokens[start+2].Text), "E")
			if len(left) == 3 && episodeOfTotalPattern.MatchString(right) &&
				normalizeEpisodeRangeText(left[2]+"-"+right) == normalizeEpisodeRangeText(epslot.Primary.EpisodeRange) {
				end = start + 3
				epslot.Primary.TokenEnd = end
			}
		}
		for candidateEnd := start + 2; candidateEnd <= len(tokens) && candidateEnd <= start+5; candidateEnd++ {
			if tokens[candidateEnd-1].Protected {
				break
			}
			var text strings.Builder
			for j := start; j < candidateEnd; j++ {
				text.WriteString(tokens[j].Text)
			}
			if normalizeEpisodeRangeText(text.String()) == normalizeEpisodeRangeText(epslot.Primary.EpisodeRange) {
				end = candidateEnd
				epslot.Primary.TokenEnd = end
				break
			}
		}
	}
	for i := start; i < end; i++ {
		tokens[i].Removed = true
	}
	if start > 0 && tokens[start-1].Text == "-" && !tokens[start-1].Protected &&
		(epslot.Primary.Kind == model.EPKindEP || epslot.Primary.Kind == model.EPKindRange) {
		tokens[start-1].Removed = true
	}
}

func extractYearAttrsAndRemoveFromTokens(tokens []model.Token, attrs *[]model.Attribute, hasTech bool) {
	if attrs == nil || len(tokens) == 0 {
		return
	}
	for i := 0; i < len(tokens); i++ {
		if tokens[i].Removed {
			continue
		}
		y := strings.TrimSpace(tokens[i].Text)
		// An explicit remake/version year is metadata even without a tech tail.
		if strings.HasSuffix(y, "年版") {
			year := strings.TrimSuffix(y, "年版")
			if yearPattern.MatchString(year) {
				*attrs = append(*attrs, model.Attribute{Type: model.AttrYear, Normalized: year, TokenStart: i, TokenEnd: i + 1, Confidence: 0.9, Source: model.AttrSourceYearToken})
				tokens[i].Removed = true
				continue
			}
		}
		if !yearPattern.MatchString(y) {
			continue
		}
		*attrs = append(*attrs, model.Attribute{Type: model.AttrYear, Normalized: y, TokenStart: i, TokenEnd: i + 1, Confidence: 0.7, Source: model.AttrSourceYearToken})

		remove := hasTech || isInBracket(tokens, i)
		if !remove {
			if i-1 >= 0 {
				p := strings.TrimSpace(tokens[i-1].Text)
				if p == "." || p == "-" || p == "_" || p == "/" {
					remove = true
				}
			}
		}
		if !remove {
			if i+1 < len(tokens) {
				n := strings.TrimSpace(tokens[i+1].Text)
				if n == "." || n == "-" || n == "_" || n == "/" {
					remove = true
				}
			}
		}
		if remove {
			tokens[i].Removed = true
		}
	}
}

func looksLikeDashSeparatedTechSuffix(tokens []model.Token, idx int) bool {
	// e.g. E-AC-3 should not yield episode=3
	if idx < 2 || idx >= len(tokens) {
		return false
	}
	cur := strings.TrimSpace(tokens[idx].Text)
	if cur == "" {
		return false
	}
	if cur != "3" {
		return false
	}
	if strings.TrimSpace(tokens[idx-1].Text) != "-" {
		return false
	}
	prev := strings.ToUpper(strings.TrimSpace(tokens[idx-2].Text))
	if prev == "AC" {
		return true
	}
	return false
}

func removePreYearNoiseNumberTokens(tokens []model.Token) {
	if len(tokens) == 0 {
		return
	}
	countPrevTitleWords := func(idx int) int {
		cnt := 0
		start := idx - 8
		if start < 0 {
			start = 0
		}
		for i := start; i < idx; i++ {
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
			if t == "-" || t == "/" || t == "|" || t == "\\" || t == "." {
				cnt = 0
				continue
			}
			allLetters := true
			for _, r := range t {
				if !(('A' <= r && r <= 'Z') || ('a' <= r && r <= 'z')) {
					allLetters = false
					break
				}
			}
			if allLetters {
				cnt++
			}
		}
		return cnt
	}

	for i := 0; i < len(tokens); i++ {
		y := strings.TrimSpace(tokens[i].Text)
		if !yearPattern.MatchString(y) {
			continue
		}
		j := i - 1
		for j >= 0 {
			pj := strings.TrimSpace(tokens[j].Text)
			if pj == "" || pj == "." || pj == "-" || pj == "_" {
				j--
				continue
			}
			break
		}
		if j < 0 {
			continue
		}
		cand := strings.TrimSpace(tokens[j].Text)
		if !reRemovePreYearNoiseNumberTokens1.MatchString(cand) {
			continue
		}
		// Only remove when we already have a multi-word latin title prefix to reduce false positives.
		if countPrevTitleWords(j) < 2 {
			continue
		}
		// Avoid removing obvious resolutions.
		if cand == "480" || cand == "720" || cand == "1080" || cand == "2160" {
			continue
		}
		tokens[j].Removed = true
	}
}

func isTitlePrefixedDigitRangeInParen(tokens []model.Token, idx int) bool {
	if idx < 0 || idx >= len(tokens) {
		return false
	}
	if !isInParen(tokens, idx) {
		return false
	}
	// Detect patterns like: ( 藤本树 17 - 26 ) where 17-26 is part of the title.
	if idx+2 >= len(tokens) {
		return false
	}
	if !episodeOfTotalPattern.MatchString(strings.TrimSpace(tokens[idx].Text)) {
		return false
	}
	sep := strings.TrimSpace(tokens[idx+1].Text)
	if sep != "-" && sep != "~" {
		return false
	}
	if !episodeOfTotalPattern.MatchString(strings.TrimSpace(tokens[idx+2].Text)) {
		return false
	}
	// If there is meaningful title text before the range within the same paren block,
	// treat this range as title suffix, not episode_range.
	openIdx := -1
	for j := idx; j >= 0; j-- {
		if tokens[j].Text == "(" || tokens[j].Text == "（" {
			openIdx = j
			break
		}
	}
	if openIdx < 0 {
		return false
	}
	for j := idx - 1; j > openIdx; j-- {
		if tokens[j].Removed {
			continue
		}
		t := strings.TrimSpace(tokens[j].Text)
		if t == "" {
			continue
		}
		if t == "-" || t == "~" {
			continue
		}
		if episodeOfTotalPattern.MatchString(t) {
			continue
		}
		return true
	}
	return false
}

func looksLikeLowercaseLatinWordToken(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	if len([]rune(s)) < 2 || len([]rune(s)) > 6 {
		return false
	}
	for _, r := range s {
		if r < 'a' || r > 'z' {
			return false
		}
	}
	return true
}

func looksLikeBitDepthWordToken(s string) bool {
	low := strings.ToLower(strings.TrimSpace(s))
	return low == "bit" || low == "bits"
}

func looksLikeAudioCodecTokenSimple(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	low := strings.ToLower(s)
	low = strings.ReplaceAll(low, "_", "-")
	codecSet := map[string]bool{"aac": true, "flac": true, "opus": true, "ac3": true, "eac3": true, "ddp": true, "dd": true, "truehd": true, "dts": true, "dts-hdma": true, "dtshdma": true, "pcm": true}
	return codecSet[low]
}

func looksLikeDottedNumericSequence(tokens []model.Token, idx int) bool {
	if idx < 0 || idx >= len(tokens) {
		return false
	}
	if tokens[idx].Removed {
		return false
	}
	if !isInBracket(tokens, idx) {
		return false
	}
	cur := strings.TrimSpace(tokens[idx].Text)
	if !episodeOfTotalPattern.MatchString(cur) {
		return false
	}
	if idx-1 >= 0 && !tokens[idx-1].Removed {
		if strings.TrimSpace(tokens[idx-1].Text) == "." {
			if idx-2 >= 0 && !tokens[idx-2].Removed {
				p := strings.TrimSpace(tokens[idx-2].Text)
				if episodeOfTotalPattern.MatchString(p) {
					return true
				}
				if looksLikeAudioCodecTokenSimple(p) {
					return true
				}
			}
		}
	}
	if idx+1 < len(tokens) && !tokens[idx+1].Removed {
		if strings.TrimSpace(tokens[idx+1].Text) == "." {
			if idx+2 < len(tokens) && !tokens[idx+2].Removed {
				n := strings.TrimSpace(tokens[idx+2].Text)
				if episodeOfTotalPattern.MatchString(n) {
					return true
				}
			}
		}
	}
	return false
}

func isRightSideOfPlainDigitRangeInTitle(tokens []model.Token, idx int) bool {
	// Guard for cases like:
	//   "藤本树 17 - 26 / Fujimoto Tatsuki 17 - 26 - 07 ..."
	// where "17-26" is part of title/alias, not an episode.
	if idx < 2 || idx >= len(tokens) {
		return false
	}
	if isInBracket(tokens, idx) || isInParen(tokens, idx) {
		return false
	}
	if strings.TrimSpace(tokens[idx].Text) == "" {
		return false
	}
	if tokens[idx-1].Removed || tokens[idx-2].Removed {
		return false
	}
	sep := strings.TrimSpace(tokens[idx-1].Text)
	if sep != "-" && sep != "~" {
		return false
	}
	left := strings.TrimSpace(tokens[idx-2].Text)
	right := strings.TrimSpace(tokens[idx].Text)
	if !episodeOfTotalPattern.MatchString(left) || !episodeOfTotalPattern.MatchString(right) {
		return false
	}
	// Strong signal: followed by '/' => title/alias split.
	if idx+1 < len(tokens) {
		if strings.TrimSpace(tokens[idx+1].Text) == "/" {
			return true
		}
	}
	// Another strong signal: followed by '-' + digits + tech bracket (e.g. "- 07 [")
	if idx+3 < len(tokens) {
		if strings.TrimSpace(tokens[idx+1].Text) == "-" {
			n := strings.TrimSpace(tokens[idx+2].Text)
			if episodeOfTotalPattern.MatchString(n) {
				b := strings.TrimSpace(tokens[idx+3].Text)
				if b == "[" || b == "【" || b == "(" || b == "（" {
					return true
				}
			}
		}
	}
	return false
}

func consumeStarWrappedLeadingMeta(tokens []model.Token, start int) (int, bool) {
	star := strings.TrimSpace(tokens[start].Text)
	if star != "★" && star != "☆" {
		return start, false
	}
	maxLookahead := start + 16
	if maxLookahead > len(tokens) {
		maxLookahead = len(tokens)
	}
	end := -1
	for j := start + 1; j < maxLookahead; j++ {
		if tokens[j].Removed {
			continue
		}
		if strings.TrimSpace(tokens[j].Text) == star {
			end = j
			break
		}
	}
	if end < 0 {
		return start, false
	}
	innerParts := make([]string, 0, end-start)
	for k := start + 1; k < end; k++ {
		if tokens[k].Removed {
			continue
		}
		t := strings.TrimSpace(tokens[k].Text)
		if t == "" {
			continue
		}
		if t == "-" || t == "·" {
			continue
		}
		innerParts = append(innerParts, t)
	}
	inner := strings.TrimSpace(strings.Join(innerParts, ""))
	if inner == "" {
		return start, false
	}
	if looksLikeLeadingTitleMeta(inner) {
		return end + 1, true
	}
	return start, false
}

// Regular expressions hoisted out of function bodies so they are compiled once at init.
var (
	reIsLikelyTechContextToken1       = regexp.MustCompile(`^\d{3,4}P$`)
	reRemovePreYearNoiseNumberTokens1 = regexp.MustCompile(`^\d{2,4}$`)
)
