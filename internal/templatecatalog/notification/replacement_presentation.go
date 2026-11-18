package notification

import (
	"strings"
)

// The replacement notice presents its two sides as fixed-order blocks. The
// winning version is a single block the default template writes from the
// separate frozen original source facts (replaced.new.*). The replaced
// versions are a list, and the template language has no block iteration, so
// the list is exposed as one derived presentation fact per shipped language:
// the same frozen replaced.old_versions entries, rendered as "旧版本" /
// "Old version" blocks with the field labels of that language. The derived
// value is computed from the frozen facts at render time; it never reads a
// library path, a planned target or a live Release row, and a version without
// any recorded source fact is left out instead of producing an empty shell.
const (
	replacedOldVersionsZH = "replaced.old_versions_zh"
	replacedOldVersionsEN = "replaced.old_versions_en"
)

// replacementPresentation renders a derived replacement presentation fact and
// reports whether the name is one. An absent or empty target list renders
// empty without a warning: the default template's line is then dropped.
func replacementPresentation(name string, facts map[string]any) (string, bool) {
	switch name {
	case replacedOldVersionsZH:
		return replacementOldVersions(facts, true), true
	case replacedOldVersionsEN:
		return replacementOldVersions(facts, false), true
	default:
		return "", false
	}
}

func replacementOldVersions(facts map[string]any, zh bool) string {
	value, _ := LookupFact(facts, "replaced.old_versions")
	type version struct{ title, group, file string }
	versions := make([]version, 0)
	for _, entry := range replacementTargetEntries(value) {
		current := version{
			title: strings.TrimSpace(FormatFact(entry["title"])),
			group: strings.TrimSpace(FormatFact(entry["group"])),
			file:  strings.TrimSpace(FormatFact(entry["path"])),
		}
		if current.title == "" && current.group == "" && current.file == "" {
			continue
		}
		versions = append(versions, current)
	}
	blocks := make([]string, 0, len(versions))
	for index, current := range versions {
		blocks = append(blocks, replacementVersionBlock(current.title, current.group, current.file, index, len(versions), zh))
	}
	if len(blocks) == 0 {
		return ""
	}
	// Keep one blank line between the two sections when the new-version block
	// is present; an old-only notice starts at the first old block.
	separator := ""
	if replacementNewVersionPresent(facts) {
		separator = "\n"
	}
	return separator + strings.Join(blocks, "\n\n")
}

func replacementNewVersionPresent(facts map[string]any) bool {
	for _, name := range []string{"replaced.new.title", "replaced.new.group", "replaced.new.file"} {
		value, _ := LookupFact(facts, name)
		if strings.TrimSpace(FormatFact(value)) != "" {
			return true
		}
	}
	return false
}

func replacementVersionBlock(title, group, file string, index, total int, zh bool) string {
	var lines []string
	if zh {
		lines = append(lines, replacementVersionHeader("旧版本", index, total))
		if title != "" {
			lines = append(lines, "标题："+title)
		}
		if group != "" {
			lines = append(lines, "字幕组："+group)
		}
		if file != "" {
			lines = append(lines, "文件："+file)
		}
	} else {
		lines = append(lines, replacementVersionHeader("Old version", index, total))
		if title != "" {
			lines = append(lines, "Title: "+title)
		}
		if group != "" {
			lines = append(lines, "Group: "+group)
		}
		if file != "" {
			lines = append(lines, "File: "+file)
		}
	}
	return strings.Join(lines, "\n")
}

// replacementVersionHeader numbers the blocks only when there is more than
// one replaced version, so a single old version keeps the plain header.
func replacementVersionHeader(base string, index, total int) string {
	if total <= 1 {
		return base
	}
	return base + " " + FormatFact(index+1)
}

// replacementTargetEntries accepts the list shapes a payload and a sample
// fact can carry: decoded JSON ([]any of map[string]any) and a Go-built list
// of maps. Empty or foreign entries are ignored.
func replacementTargetEntries(value any) []map[string]any {
	switch typed := value.(type) {
	case []map[string]any:
		return typed
	case []any:
		entries := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			if entry, ok := item.(map[string]any); ok {
				entries = append(entries, entry)
			}
		}
		return entries
	default:
		return nil
	}
}
