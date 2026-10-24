package torrenttree

import (
	"fmt"
	"strings"
	"testing"
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

func TestRenderAlignsNamesAndFoldsTheRemainder(t *testing.T) {
	files := []File{
		{Path: "Show/Season 1/01.mkv", SizeBytes: 2 << 30},
		{Path: "Show/Season 1/01.ass", SizeBytes: 1 << 20},
		{Path: "Show/readme.txt", SizeBytes: 40},
	}
	got := Render(files)
	wantSuffix := []string{"2.0 GiB · 2 files", "40 B"}
	lines := strings.Split(got, "\n")
	if len(lines) != 3 || !strings.HasSuffix(lines[2], "合计: 2.0 GiB · 3 files") {
		t.Fatalf("render =\n%s", got)
	}
	first := strings.Index(lines[0], wantSuffix[0])
	second := strings.Index(lines[1], wantSuffix[1])
	if first < 0 || first != second {
		t.Fatalf("sizes are not aligned:\n%s", got)
	}

	var many []File
	for index := 0; index < MaxEntries+12; index++ {
		many = append(many, File{Path: fmt.Sprintf("Show/dir-%02d/01.mkv", index), SizeBytes: 1 << 20})
	}
	rendered := Render(many)
	folded := strings.Split(rendered, "\n")
	if len(folded) != MaxEntries+2 {
		t.Fatalf("folded render has %d lines:\n%s", len(folded), rendered)
	}
	if folded[MaxEntries] != "… 折叠 12 项" {
		t.Fatalf("fold line = %q", folded[MaxEntries])
	}
	if !strings.HasSuffix(rendered, "合计: 42.0 MiB · 42 files") {
		t.Fatalf("totals do not cover the folded entries:\n%s", rendered)
	}
}

func TestRenderAlignsWideRunes(t *testing.T) {
	got := Render([]File{
		{Path: "Show/字幕/01.ass", SizeBytes: 1 << 20},
		{Path: "Show/subs/01.ass", SizeBytes: 1 << 20},
	})
	lines := strings.Split(got, "\n")
	if len(lines) != 3 {
		t.Fatalf("render =\n%s", got)
	}
	if displayCells(lines[0][:strings.Index(lines[0], "1.0 MiB")]) != displayCells(lines[1][:strings.Index(lines[1], "1.0 MiB")]) {
		t.Fatalf("wide rune column is not aligned:\n%s", got)
	}
}
