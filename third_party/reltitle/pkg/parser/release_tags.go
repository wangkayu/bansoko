package parser

import (
	"github.com/wangkayu/reltitle/pkg/model"
	"regexp"
	"strings"
)

func extractStrictCustomReleaseTagAttrsAndRemoveFromTokens(tokens []model.Token, out *[]model.Attribute) {
	if out == nil || len(tokens) == 0 {
		return
	}
	hasReleaseMetaForCustomUpper := func() bool {
		for i := 0; i < len(tokens); i++ {
			if tokens[i].Removed {
				continue
			}
			t := strings.ToUpper(strings.TrimSpace(tokens[i].Text))
			if t == "" {
				continue
			}
			if t == "VOSTFR" || t == "VF" || t == "MULTI" || t == "BR" || t == "BDRIP" || t == "DVDRIP" {
				return true
			}
		}
		return false
	}
	hasFrenchLangMeta := func() bool {
		for i := 0; i < len(tokens); i++ {
			if tokens[i].Removed {
				continue
			}
			t := strings.ToUpper(strings.TrimSpace(tokens[i].Text))
			if t == "" {
				continue
			}
			if t == "VOSTFR" || t == "VF" || t == "FRENCH" || t == "FRANCAIS" || t == "TRUEFRENCH" {
				return true
			}
		}
		return false
	}

	allowUpper := hasReleaseMetaForCustomUpper()
	allowTitleCase := hasFrenchLangMeta()
	for i := 0; i < len(tokens); i++ {
		if tokens[i].Removed {
			continue
		}
		txt := strings.TrimSpace(tokens[i].Text)
		if txt == "" {
			continue
		}
		// Only treat CUSTOM as release tag when the title also contains strong release meta.
		if txt == "CUSTOM" {
			if !allowUpper {
				continue
			}
		} else if txt == "Custom" {
			// TitleCase 'Custom' is ambiguous. Only treat it as a release tag when French language signals exist.
			if !allowTitleCase {
				continue
			}
		} else {
			continue
		}
		*out = append(*out, model.Attribute{Type: model.AttrEdition, Normalized: "CUSTOM", TokenStart: i, TokenEnd: i + 1, Confidence: 0.6, Source: model.AttrSourceCustomTag})
		tokens[i].Removed = true
		// If surrounded by parentheses, remove them too.
		if i-1 >= 0 {
			p := strings.TrimSpace(tokens[i-1].Text)
			if p == "(" || p == "（" {
				tokens[i-1].Removed = true
			}
		}
		if i+1 < len(tokens) {
			n := strings.TrimSpace(tokens[i+1].Text)
			if n == ")" || n == "）" {
				tokens[i+1].Removed = true
			}
		}
	}
}

func extractGuardedDVDRIPReleaseTagAttrsAndRemoveFromTokens(tokens []model.Token, out *[]model.Attribute) {
	if out == nil || len(tokens) == 0 {
		return
	}
	// Guard: only treat DVDRIP as release meta if other strong meta/tech signals exist.
	// This avoids invading the title block in rare cases where DVDRIP is part of a real title.
	hasStrongMeta := func() bool {
		for i := 0; i < len(tokens); i++ {
			if tokens[i].Removed {
				continue
			}
			t := strings.ToUpper(strings.TrimSpace(tokens[i].Text))
			if t == "" {
				continue
			}
			// Language / release ecosystem signals
			if t == "VOSTFR" || t == "VF" || t == "MULTI" || t == "BR" || t == "BDRIP" {
				return true
			}
			// Common codec / container / audio signals
			if t == "X264" || t == "X265" || t == "H264" || t == "H265" || t == "AVC" || t == "HEVC" || t == "AAC" || t == "AC3" || t == "EAC3" || t == "DDP" || t == "FLAC" || t == "MKV" || t == "MP4" {
				return true
			}
			// Common resolution signals
			if strings.HasSuffix(t, "P") {
				if reExtractGuardedDVDRIPReleaseTagAttrsAndRemoveFromTokens1.MatchString(t) {
					return true
				}
			}
		}
		return false
	}

	if !hasStrongMeta() {
		return
	}

	for i := 0; i < len(tokens); i++ {
		if tokens[i].Removed {
			continue
		}
		txt := strings.TrimSpace(tokens[i].Text)
		if txt == "" {
			continue
		}
		if !strings.EqualFold(txt, "DVDRIP") {
			continue
		}
		*out = append(*out, model.Attribute{Type: model.AttrSourceType, Normalized: "DVDRip", TokenStart: i, TokenEnd: i + 1, Confidence: 0.7, Source: model.AttrSourceDVDRipTag})
		tokens[i].Removed = true
	}
}

var reExtractGuardedDVDRIPReleaseTagAttrsAndRemoveFromTokens1 = regexp.MustCompile(`^\d{3,4}P$`)
