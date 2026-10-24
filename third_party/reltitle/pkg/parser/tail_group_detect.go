package parser

import (
	"strings"

	"github.com/wangkayu/reltitle/pkg/model"
)

func isTailGroupToken(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	// Guard: EP-like tokens such as E192 / EP07 are episode markers, not release groups.
	if episodePrefixedTokenPattern.MatchString(s) {
		return false
	}
	if !tailGroupPattern.MatchString(s) {
		return false
	}
	up := strings.ToUpper(s)
	if strings.Contains(up, "RAWS") || strings.Contains(up, "SUBS") {
		return false
	}
	if up == "TIME" || up == "RAWS" || up == "SUBS" || up == "KUN" || up == "SAN" || up == "CHAN" {
		return false
	}
	if up == "DL" || up == "WEB" || up == "WEBRIP" || up == "WEB-DL" || up == "BLURAY" || up == "BDRIP" || up == "DVDRIP" {
		return false
	}
	if up == "CUSTOM" {
		return false
	}
	if up == "H264" || up == "H265" || up == "X264" || up == "X265" || up == "HEVC" || up == "AVC" || up == "AAC" {
		return false
	}
	if up == "NCOP" || up == "NCED" || up == "PV" || up == "CM" {
		return false
	}
	if tailGroupTechTokenPattern.MatchString(up) {
		return false
	}
	return true
}

func detectTailGroup(tokens []model.Token) (string, int) {
	return detectTailGroupWithMinIdx(tokens, -1)
}

func detectTailGroupAfter(tokens []model.Token, minIdx int) (string, int) {
	return detectTailGroupWithMinIdx(tokens, minIdx)
}

func detectTailGroupWithMinIdx(tokens []model.Token, minIdx int) (string, int) {
	// Prefer explicit "... X - Raws/Subs" pattern, even if tokens were marked removed by tech passes.
	for i := len(tokens) - 1; i >= 0; i-- {
		if i <= minIdx {
			continue
		}
		if isInBracket(tokens, i) || isInParen(tokens, i) {
			continue
		}
		text := strings.TrimSpace(tokens[i].Text)
		if !strings.EqualFold(text, "Raws") && !strings.EqualFold(text, "Subs") {
			continue
		}
		if i-2 <= minIdx {
			continue
		}
		if strings.TrimSpace(tokens[i-1].Text) != "-" {
			continue
		}
		left := strings.TrimSpace(tokens[i-2].Text)
		if !isTailGroupToken(left) {
			continue
		}
		return left + "-" + text, i - 2
	}

	lastTail := -1
	for j := len(tokens) - 1; j >= 0; j-- {
		if j <= minIdx {
			continue
		}
		if tokens[j].Removed {
			continue
		}
		if isInBracket(tokens, j) || isInParen(tokens, j) {
			continue
		}
		t := strings.TrimSpace(tokens[j].Text)
		if t == "" {
			continue
		}
		low := strings.ToLower(t)
		if t == "(" || t == ")" || t == "-" || t == "." || low == "raws" || low == "subs" {
			continue
		}
		lastTail = j
		break
	}

	for i := len(tokens) - 1; i >= 0; i-- {
		if i <= minIdx {
			continue
		}
		if tokens[i].Removed {
			continue
		}
		if isInBracket(tokens, i) || isInParen(tokens, i) {
			continue
		}
		text := strings.TrimSpace(tokens[i].Text)
		if text == "" {
			continue
		}
		cand := ""
		if isTailGroupToken(text) {
			if i-1 >= 0 {
				sep := strings.TrimSpace(tokens[i-1].Text)
				if sep == "-" {
					// For single-token tail group, require at least one uppercase letter to avoid picking words like 'kun'.
					if !hasUpperASCII(text) {
						continue
					}
					// Must be near tail; otherwise '-' is more likely a title separator.
					if lastTail >= 0 && i < lastTail {
						continue
					}
					// Reconstruct patterns like "- Tsundere - Raws" -> "Tsundere-Raws"
					if i+2 < len(tokens) {
						sep2 := strings.TrimSpace(tokens[i+1].Text)
						next := strings.TrimSpace(tokens[i+2].Text)
						if sep2 == "-" && (strings.EqualFold(next, "Raws") || strings.EqualFold(next, "Subs")) {
							if isTailGroupToken(next) || strings.EqualFold(next, "Raws") || strings.EqualFold(next, "Subs") {
								return text + "-" + next, i
							}
						}
					}
					// Reconstruct patterns like Tsundere - Raws (tokenized split)
					if (strings.EqualFold(text, "Raws") || strings.EqualFold(text, "Subs")) && i-2 >= 0 {
						p := strings.TrimSpace(tokens[i-2].Text)
						pl := strings.ToLower(p)
						if isTailGroupToken(p) && pl != "part" {
							return p + "-" + text, i
						}
					}
					cand = text
				}
			}
		} else if strings.Contains(text, "-") {
			parts := strings.Split(text, "-")
			if len(parts) >= 2 {
				prefix := strings.TrimSpace(parts[len(parts)-2])
				last := strings.TrimSpace(parts[len(parts)-1])
				if tailGroupHyphenPrefixPattern.MatchString(prefix) && isTailGroupToken(last) {
					cand = last
				}
			}
		}
		if cand == "" {
			continue
		}
		return cand, i
	}
	return "", -1
}
