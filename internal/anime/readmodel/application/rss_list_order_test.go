package application

import (
	"testing"
	"time"
)

func TestNaturalPathCompare(t *testing.T) {
	ordered := []string{
		"Show/Extras/NCOP.mkv",
		"Show/ep1.mkv",
		"Show/ep2.mkv",
		"Show/EP10.mkv",
		"Show/ep010b.mkv",
		"Show/ep11.mkv",
		"top.mkv",
	}
	for index := 0; index+1 < len(ordered); index++ {
		if naturalPathCompare(ordered[index], ordered[index+1]) >= 0 {
			t.Fatalf("%q must sort before %q", ordered[index], ordered[index+1])
		}
		if naturalPathCompare(ordered[index+1], ordered[index]) <= 0 {
			t.Fatalf("%q must sort after %q", ordered[index+1], ordered[index])
		}
	}
	if naturalPathCompare("A/a.mkv", "a/a.mkv") == 0 {
		t.Fatal("case-only differences need a stable tie-break")
	}
	if naturalPathCompare("x/ep2.mkv", "x/ep2.mkv") != 0 {
		t.Fatal("equal paths must compare equal")
	}
}

func TestSortRSSListFilesNaturalIsStable(t *testing.T) {
	files := []RSSListFile{
		{SourcePath: "S/ep10.mkv", Ordinal: 0},
		{SourcePath: "S/Ep2.mkv", Ordinal: 1},
		{SourcePath: "S/ep2.mkv", Ordinal: 2},
		{SourcePath: "S/sub/ep1.ass", Ordinal: 3},
	}
	sortRSSListFilesNatural(files)
	got := []int{files[0].Ordinal, files[1].Ordinal, files[2].Ordinal, files[3].Ordinal}
	// A directory lists before the files beside it; the case-only tie falls back to bytes.
	want := []int{3, 1, 2, 0}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}

func TestSortRSSListItemsNewestFirst(t *testing.T) {
	at := func(hour int) *time.Time {
		value := time.Date(2026, 10, 1, hour, 0, 0, 0, time.UTC)
		return &value
	}
	items := []RSSListItem{
		{StableSeedKey: "undated-b"},
		{StableSeedKey: "old", Seed: RSSListSeedFacts{PublishedAt: at(1)}},
		{StableSeedKey: "tie-b", Seed: RSSListSeedFacts{PublishedAt: at(5)}},
		{StableSeedKey: "undated-a"},
		{StableSeedKey: "new", Seed: RSSListSeedFacts{PublishedAt: at(9)}},
		{StableSeedKey: "tie-a", Seed: RSSListSeedFacts{PublishedAt: at(5)}},
	}
	sortRSSListItemsNewestFirst(items)
	want := []string{"new", "tie-a", "tie-b", "old", "undated-a", "undated-b"}
	for index, key := range want {
		if items[index].StableSeedKey != key {
			t.Fatalf("item %d = %s, want %s", index, items[index].StableSeedKey, key)
		}
	}
}
