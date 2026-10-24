package parser

import (
	"github.com/wangkayu/reltitle/pkg/model"
	"strings"
)

func bracketBlockInnerText(tokens []model.Token, idx int) string {
	if idx < 0 || idx >= len(tokens) {
		return ""
	}
	start := -1
	depth := 0
	for i := 0; i <= idx; i++ {
		if tokens[i].Text == "[" || tokens[i].Text == "【" {
			if depth == 0 {
				start = i
			}
			depth++
		} else if tokens[i].Text == "]" || tokens[i].Text == "】" {
			if depth > 0 {
				depth--
				if depth == 0 {
					start = -1
				}
			}
		}
	}
	if start < 0 {
		return ""
	}
	end := -1
	depth = 0
	for j := start; j < len(tokens); j++ {
		if tokens[j].Text == "[" || tokens[j].Text == "【" {
			depth++
		} else if tokens[j].Text == "]" || tokens[j].Text == "】" {
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
		return ""
	}
	parts := make([]string, 0, end-start)
	for k := start + 1; k < end; k++ {
		if tokens[k].Removed {
			continue
		}
		t := strings.TrimSpace(tokens[k].Text)
		if t == "" || t == "[" || t == "]" || t == "【" || t == "】" {
			continue
		}
		parts = append(parts, t)
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}

func bracketBoundsAt(tokens []model.Token, idx int) (int, int, bool) {
	if idx < 0 || idx >= len(tokens) || !isInBracket(tokens, idx) {
		return -1, -1, false
	}
	openIdx := -1
	openTok := ""
	depth := 0
	for j := idx; j >= 0; j-- {
		t := strings.TrimSpace(tokens[j].Text)
		if t == "]" || t == "】" {
			depth++
			continue
		}
		if t == "[" || t == "【" {
			if depth == 0 {
				openIdx = j
				openTok = t
				break
			}
			depth--
		}
	}
	if openIdx < 0 {
		return -1, -1, false
	}
	closeTok := "]"
	if openTok == "【" {
		closeTok = "】"
	}
	d := 0
	for j := openIdx; j < len(tokens); j++ {
		t := strings.TrimSpace(tokens[j].Text)
		if t == openTok {
			d++
			continue
		}
		if t == closeTok {
			d--
			if d == 0 {
				return openIdx, j, true
			}
		}
	}
	return -1, -1, false
}

func isInParen(tokens []model.Token, idx int) bool {
	depth := 0
	for i := 0; i < idx; i++ {
		if tokens[i].Text == "(" || tokens[i].Text == "（" {
			depth++
		} else if tokens[i].Text == ")" || tokens[i].Text == "）" {
			if depth > 0 {
				depth--
			}
		}
	}
	return depth > 0
}

func parenBlockInnerText(tokens []model.Token, idx int) string {
	if idx < 0 || idx >= len(tokens) {
		return ""
	}
	for j := idx; j >= 0; j-- {
		if tokens[j].Text != "(" && tokens[j].Text != "（" {
			continue
		}
		openIdx, closeIdx, inner, ok := parseParenBlock(tokens, j, len(tokens))
		if !ok {
			continue
		}
		if openIdx <= idx && idx <= closeIdx {
			return strings.TrimSpace(inner)
		}
	}
	return ""
}

func parseParenBlockAll(tokens []model.Token, openIdx int, limit int) (int, int, string, bool) {
	if openIdx < 0 || openIdx >= len(tokens) {
		return openIdx, openIdx, "", false
	}
	op := tokens[openIdx].Text
	if op != "(" && op != "（" {
		return openIdx, openIdx, "", false
	}
	close := ")"
	if op == "（" {
		close = "）"
	}
	end := -1
	depth := 0
	maxJ := limit
	if maxJ > len(tokens) {
		maxJ = len(tokens)
	}
	for j := openIdx; j < maxJ; j++ {
		if tokens[j].Text == op {
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
		return openIdx, openIdx, "", false
	}
	parts := make([]string, 0, end-openIdx)
	for k := openIdx + 1; k < end; k++ {
		t := strings.TrimSpace(tokens[k].Text)
		if t == "" {
			continue
		}
		parts = append(parts, t)
	}
	return openIdx, end, strings.TrimSpace(strings.Join(parts, " ")), true
}

func parseParenBlock(tokens []model.Token, openIdx int, limit int) (int, int, string, bool) {
	if openIdx < 0 || openIdx >= len(tokens) {
		return openIdx, openIdx, "", false
	}
	op := tokens[openIdx].Text
	if op != "(" && op != "（" {
		return openIdx, openIdx, "", false
	}
	close := ")"
	if op == "（" {
		close = "）"
	}
	end := -1
	depth := 0
	maxJ := limit
	if maxJ > len(tokens) {
		maxJ = len(tokens)
	}
	for j := openIdx; j < maxJ; j++ {
		if tokens[j].Text == op {
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
		return openIdx, openIdx, "", false
	}
	parts := make([]string, 0, end-openIdx)
	for k := openIdx + 1; k < end; k++ {
		if tokens[k].Removed {
			continue
		}
		t := strings.TrimSpace(tokens[k].Text)
		if t == "" {
			continue
		}
		parts = append(parts, t)
	}
	return openIdx, end, strings.TrimSpace(strings.Join(parts, " ")), true
}

func isInBracket(tokens []model.Token, idx int) bool {
	depth := 0
	for i := 0; i < idx; i++ {
		if tokens[i].Text == "[" || tokens[i].Text == "【" {
			depth++
		} else if tokens[i].Text == "]" || tokens[i].Text == "】" {
			if depth > 0 {
				depth--
			}
		}
	}
	return depth > 0
}
