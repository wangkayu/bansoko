package parser

import (
	"regexp"
	"strings"

	"github.com/wangkayu/reltitle/pkg/model"
)

// reVideoFileExtensionTail matches a raw input that ends in a video file
// extension glued to the name, e.g. "...[x264_AAC].mp4". A bracketed or
// space-separated "[MKV]" / " MKV" is a declared container tag, not an
// extension. Disc images (.iso) are excluded: they may hold a whole batch.
var reVideoFileExtensionTail = regexp.MustCompile(`(?i)[^\s.]\.(mkv|mp4|avi|m2ts|ts|mov|wmv|flv|mpg|mpeg|m4v|webm|rmvb|rm)$`)

// markFileExtensionContainer tags the container read from a trailing file
// extension with the file_extension source, adding it when the lexicon has no
// entry for the extension. The input is then a single video file.
func markFileExtensionContainer(rawTitle string, tokens []model.Token, attrs *[]model.Attribute) {
	m := reVideoFileExtensionTail.FindStringSubmatch(strings.TrimSpace(rawTitle))
	if len(m) != 2 {
		return
	}
	last := len(tokens) - 1
	for last >= 0 && strings.TrimSpace(tokens[last].Text) == "" {
		last--
	}
	if last < 1 || !strings.EqualFold(strings.TrimSpace(tokens[last].Text), m[1]) || strings.TrimSpace(tokens[last-1].Text) != "." {
		return
	}
	tokens[last-1].Removed = true
	tokens[last].Removed = true
	for i := range *attrs {
		a := &(*attrs)[i]
		if a.Type == model.AttrContainer && a.TokenStart == last {
			a.Source = model.AttrSourceFileExtension
			return
		}
	}
	*attrs = append(*attrs, model.Attribute{Type: model.AttrContainer, Normalized: strings.ToUpper(m[1]), TokenStart: last, TokenEnd: last + 1, Confidence: 0.7, Source: model.AttrSourceFileExtension})
}

func hasFileExtensionContainer(attrs []model.Attribute) bool {
	for _, a := range attrs {
		if a.Type == model.AttrContainer && a.Source.Canonical() == model.AttrSourceFileExtension {
			return true
		}
	}
	return false
}

// reconcileFileExtensionContainer keeps one container when the input is a
// file: the extension is what the file is now, so a declared tag with the same
// value is dropped and a conflicting one (a remuxed "[MKV].mp4") becomes a note.
func reconcileFileExtensionContainer(attrs []model.Attribute) []model.Attribute {
	ext := ""
	for _, a := range attrs {
		if a.Type == model.AttrContainer && a.Source.Canonical() == model.AttrSourceFileExtension {
			ext = a.Normalized
			break
		}
	}
	if ext == "" {
		return attrs
	}
	out := attrs[:0]
	for _, a := range attrs {
		if a.Type == model.AttrContainer && a.Source.Canonical() != model.AttrSourceFileExtension {
			if strings.EqualFold(a.Normalized, ext) {
				continue
			}
			a.Type = model.AttrNote
		}
		out = append(out, a)
	}
	return out
}
