package torrenttree

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf16"
)

func TestRelativeStripsOnlyASharedRoot(t *testing.T) {
	shared := Relative([]File{{Path: "Show/Season 1/01.mkv"}, {Path: "Show/readme.txt"}})
	if shared[0].Path != "Season 1/01.mkv" || shared[1].Path != "readme.txt" {
		t.Fatalf("shared root was not stripped: %+v", shared)
	}
	loose := Relative([]File{{Path: "a/01.mkv"}, {Path: "b/02.mkv"}})
	if loose[0].Path != "a/01.mkv" || loose[1].Path != "b/02.mkv" {
		t.Fatalf("unrelated roots were stripped: %+v", loose)
	}
	single := Relative([]File{{Path: "Show - 01.mkv"}})
	if single[0].Path != "Show - 01.mkv" {
		t.Fatalf("a single root file was stripped: %+v", single)
	}
}

func TestBuildFoldsDirectoriesWithRecursiveTotals(t *testing.T) {
	entries := Build([]File{
		{Path: "Show/Season 2/02.mkv", SizeBytes: 300},
		{Path: "Show/Season 1/01.mkv", SizeBytes: 200},
		{Path: "Show/Season 1/01.ass", SizeBytes: 100},
		{Path: "Show/readme.txt", SizeBytes: 7},
	})
	if len(entries) != 3 {
		t.Fatalf("entries = %+v", entries)
	}
	if entries[0].Name != "Season 1" || !entries[0].Dir || entries[0].Size != 300 || entries[0].Count != 2 {
		t.Fatalf("Season 1 = %+v", entries[0])
	}
	if entries[1].Name != "Season 2" || entries[1].Size != 300 || entries[1].Count != 1 {
		t.Fatalf("Season 2 = %+v", entries[1])
	}
	if entries[2].Name != "readme.txt" || entries[2].Dir || entries[2].Size != 7 || entries[2].Count != 1 {
		t.Fatalf("root file = %+v", entries[2])
	}
}

func TestRenderEmptyWhenThereIsNothingToShow(t *testing.T) {
	if got := Render(nil); got != "" {
		t.Fatalf("empty render = %q", got)
	}
}

// Each entry is a name line and an indented size line; the complete totals
// follow one blank line, and no column alignment is needed.
func TestRenderUsesTwoLineEntriesAndKeepsTotals(t *testing.T) {
	got := Render([]File{
		{Path: "Show/Season 1/01.mkv", SizeBytes: 2 << 30},
		{Path: "Show/Season 1/01.ass", SizeBytes: 1 << 20},
		{Path: "Show/readme.txt", SizeBytes: 40},
	})
	want := "Season 1/\n  └ 2.0 GiB · 2 files\nreadme.txt\n  └ 40 B\n\n合计: 2.0 GiB · 3 files"
	if got != want {
		t.Fatalf("render =\n%s\nwant\n%s", got, want)
	}
}

func TestRenderFoldsRemainderAndKeepsCompleteTotals(t *testing.T) {
	var many []File
	for index := 0; index < MaxEntries+12; index++ {
		many = append(many, File{Path: fmt.Sprintf("Show/dir-%02d/01.mkv", index), SizeBytes: 1 << 20})
	}
	rendered := Render(many)
	folded := strings.Split(rendered, "\n")
	// Two lines per shown entry, the fold line, the blank line, the totals.
	if len(folded) != MaxEntries*2+3 {
		t.Fatalf("folded render has %d lines:\n%s", len(folded), rendered)
	}
	if folded[MaxEntries*2] != "… 折叠 12 项" {
		t.Fatalf("fold line = %q", folded[MaxEntries*2])
	}
	if folded[MaxEntries*2+1] != "" {
		t.Fatalf("blank line before totals = %q", folded[MaxEntries*2+1])
	}
	if folded[MaxEntries*2+2] != "合计: 42.0 MiB · 42 files" {
		t.Fatalf("totals do not cover the folded entries:\n%s", rendered)
	}
}

func TestRenderSanitizesControlCharacters(t *testing.T) {
	got := Render([]File{
		{Path: "Show/a\nb/01.mkv", SizeBytes: 1},
		{Path: "Show/c\u2028d/02.mkv", SizeBytes: 2},
		{Path: "Show/\x01\x02/03.mkv", SizeBytes: 3},
	})
	if strings.ContainsAny(got, "\x01\x02") || strings.ContainsRune(got, '\u2028') {
		t.Fatalf("control characters survived:\n%q", got)
	}
	for _, want := range []string{"a b/\n  └ 1 B · 1 files", "c d/\n  └ 2 B · 1 files", "\uFFFD/\n  └ 3 B · 1 files"} {
		if !strings.Contains(got, want) {
			t.Fatalf("render does not contain %q:\n%s", want, got)
		}
	}
	lines := strings.Split(got, "\n")
	if len(lines) != 8 || lines[6] != "" || lines[7] != "合计: 6 B · 3 files" {
		t.Fatalf("a control character changed the line structure:\n%q", got)
	}
}

func TestRenderBoundsLongNamesAndKeepsTheStatsLine(t *testing.T) {
	got := Render([]File{
		{Path: "Show/" + strings.Repeat("字", 60) + "/01.mkv", SizeBytes: 1 << 20},
	})
	lines := strings.Split(got, "\n")
	if len(lines) != 4 {
		t.Fatalf("render =\n%s", got)
	}
	if cells := displayCells(lines[0]); cells > maxNameCells || !strings.HasSuffix(lines[0], "…/") {
		t.Fatalf("name line has %d cells and is %q", cells, lines[0])
	}
	if lines[1] != "  └ 1.0 MiB · 1 files" || lines[2] != "" || lines[3] != "合计: 1.0 MiB · 1 files" {
		t.Fatalf("long name displaced the stats or totals:\n%q", got)
	}
}

// A full listing is bounded well under a channel's text limit, so the two-line
// format itself never pushes the complete totals out of a tree-only message.
func TestRenderBoundedUnderChannelTextLimit(t *testing.T) {
	files := make([]File, 0, MaxEntries)
	for index := 0; index < MaxEntries; index++ {
		name := fmt.Sprintf("%02d %s", index, strings.Repeat("字", 22))
		files = append(files, File{Path: "Show/" + name + "/01.mkv", SizeBytes: 1 << 30})
	}
	rendered := Render(files)
	if units := len(utf16.Encode([]rune(rendered))); units > 4096 {
		t.Fatalf("full listing is %d UTF-16 units:\n%s", units, rendered)
	}
	if !strings.HasSuffix(rendered, "\n\n合计: 30.0 GiB · 30 files") {
		t.Fatalf("totals are not complete:\n%s", rendered)
	}
}
