// Package torrenttree groups a torrent's file list into the one-level listing
// the UI preview and the torrent.tree fact both render. It is pure: the file
// list comes from the retained bencode payload (internal/acquisition/torrent)
// or from the release's stored file projection, never from a fresh fetch.
package torrenttree

import (
	"fmt"
	"sort"
	"strings"
)

const (
	// MaxEntries bounds the top-level entries one rendered listing shows. The
	// folded remainder is reported on one line and the totals stay complete.
	MaxEntries = 30
	// minNameCells keeps short listings aligned to a readable column;
	// maxNameCells stops one very long name from pushing the sizes away.
	minNameCells = 16
	maxNameCells = 48
)

// File is one file of a torrent: its stored path and its size in bytes.
type File struct {
	Path      string
	SizeBytes int64
}

// Entry is one top-level entry: a directory directly under the torrent root,
// or a file stored at the root itself.
type Entry struct {
	Name  string
	Dir   bool
	Size  int64
	Count int
}

// Relative strips the torrent root directory from every path when all files
// share one leading component. A torrent stores its files under its own name,
// so the name is not an entry of the listing; the listing is about what is
// inside. Paths outside a shared root are returned unchanged.
func Relative(files []File) []File {
	root := sharedRoot(files)
	out := make([]File, 0, len(files))
	for _, file := range files {
		path := cleanPath(file.Path)
		if root != "" {
			path = strings.TrimPrefix(path, root+"/")
		}
		if path == "" {
			continue
		}
		out = append(out, File{Path: path, SizeBytes: file.SizeBytes})
	}
	return out
}

// Build groups the files into their top-level entries, sorted by name.
func Build(files []File) []Entry {
	byName := make(map[string]*Entry, len(files))
	order := make([]string, 0, len(files))
	for _, file := range Relative(files) {
		name, dir := file.Path, false
		if index := strings.IndexByte(name, '/'); index >= 0 {
			name, dir = name[:index], true
		}
		if name == "" {
			continue
		}
		entry, ok := byName[name]
		if !ok {
			entry = &Entry{Name: name, Dir: dir}
			byName[name] = entry
			order = append(order, name)
		}
		entry.Size += file.SizeBytes
		entry.Count++
	}
	sort.Strings(order)
	entries := make([]Entry, 0, len(order))
	for _, name := range order {
		entries = append(entries, *byName[name])
	}
	return entries
}

// Totals returns the size and file count of every file in the listing,
// regardless of how many top-level entries are shown.
func Totals(files []File) (int64, int) {
	relative := Relative(files)
	var total int64
	for _, file := range relative {
		total += file.SizeBytes
	}
	return total, len(relative)
}

// Render is the plain-text listing: one line per top-level entry, aligned on
// a name column, folders carrying their recursive size and file count, then
// one folded remainder line when the entries overflow MaxEntries, then the
// complete totals. It returns "" when there is nothing to show, so a missing
// payload renders nothing instead of a placeholder.
func Render(files []File) string {
	entries := Build(files)
	if len(entries) == 0 {
		return ""
	}
	totalSize, totalCount := Totals(files)
	shown, hidden := entries, 0
	if len(entries) > MaxEntries {
		shown, hidden = entries[:MaxEntries], len(entries)-MaxEntries
	}
	width := minNameCells
	for _, entry := range shown {
		if cells := displayCells(label(entry)); cells > width {
			width = cells
		}
	}
	if width > maxNameCells {
		width = maxNameCells
	}
	var builder strings.Builder
	for _, entry := range shown {
		text := truncateCells(label(entry), width)
		builder.WriteString(text)
		builder.WriteString(strings.Repeat(" ", width-displayCells(text)))
		builder.WriteString("  ")
		builder.WriteString(HumanSize(entry.Size))
		if entry.Dir {
			fmt.Fprintf(&builder, " · %d files", entry.Count)
		}
		builder.WriteString("\n")
	}
	if hidden > 0 {
		fmt.Fprintf(&builder, "… 折叠 %d 项\n", hidden)
	}
	fmt.Fprintf(&builder, "合计: %s · %d files", HumanSize(totalSize), totalCount)
	return builder.String()
}

// HumanSize formats a byte count with binary units and one decimal, so the
// sizes line up in a monospace block.
func HumanSize(size int64) string {
	switch {
	case size >= 1<<30:
		return fmt.Sprintf("%.1f GiB", float64(size)/float64(1<<30))
	case size >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(size)/float64(1<<20))
	case size >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(size)/float64(1<<10))
	default:
		return fmt.Sprintf("%d B", size)
	}
}

func label(entry Entry) string {
	if entry.Dir {
		return entry.Name + "/"
	}
	return entry.Name
}

func sharedRoot(files []File) string {
	root := ""
	for index, file := range files {
		path := cleanPath(file.Path)
		cut := strings.IndexByte(path, '/')
		if cut <= 0 {
			return ""
		}
		if index == 0 {
			root = path[:cut]
			continue
		}
		if path[:cut] != root {
			return ""
		}
	}
	return root
}

func cleanPath(path string) string {
	return strings.Trim(strings.ReplaceAll(path, "\\", "/"), "/")
}

// truncateCells cuts text to at most width display cells, marking the cut
// with an ellipsis.
func truncateCells(text string, width int) string {
	if displayCells(text) <= width {
		return text
	}
	var builder strings.Builder
	used := 0
	for _, r := range text {
		cells := runeCells(r)
		if used+cells > width-1 {
			break
		}
		builder.WriteRune(r)
		used += cells
	}
	builder.WriteString("…")
	return builder.String()
}

// displayCells is the monospace width of text: East Asian wide runes occupy
// two cells, everything else one.
func displayCells(text string) int {
	total := 0
	for _, r := range text {
		total += runeCells(r)
	}
	return total
}

func runeCells(r rune) int {
	switch {
	case r >= 0x1100 && r <= 0x115F, // Hangul Jamo initials
		r >= 0x2E80 && r <= 0xA4CF, // CJK radicals through Yi
		r >= 0xA960 && r <= 0xA97F,
		r >= 0xAC00 && r <= 0xD7A3, // Hangul syllables
		r >= 0xF900 && r <= 0xFAFF, // CJK compatibility ideographs
		r >= 0xFE10 && r <= 0xFE19,
		r >= 0xFE30 && r <= 0xFE6F,
		r >= 0xFF00 && r <= 0xFF60, // fullwidth forms
		r >= 0xFFE0 && r <= 0xFFE6,
		r >= 0x1F300 && r <= 0x1F64F,
		r >= 0x1F900 && r <= 0x1F9FF,
		r >= 0x20000 && r <= 0x3FFFD:
		return 2
	}
	return 1
}
