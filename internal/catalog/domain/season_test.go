package domain

import (
	"testing"
	"time"
)

func TestSeasonAtAndAfter(t *testing.T) {
	cases := []struct {
		at        time.Time
		now, next string
	}{
		{time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC), "2026-winter", "2026-spring"},
		{time.Date(2026, time.June, 30, 23, 0, 0, 0, time.UTC), "2026-spring", "2026-summer"},
		{time.Date(2026, time.July, 1, 0, 0, 0, 0, time.UTC), "2026-summer", "2026-fall"},
		{time.Date(2026, time.December, 31, 0, 0, 0, 0, time.UTC), "2026-fall", "2027-winter"},
	}
	for _, c := range cases {
		if got := SeasonAt(c.at); got != c.now {
			t.Fatalf("SeasonAt(%s) = %s, want %s", c.at, got, c.now)
		}
		if got := SeasonAfter(c.at); got != c.next {
			t.Fatalf("SeasonAfter(%s) = %s, want %s", c.at, got, c.next)
		}
	}
}

func TestSeasonBefore(t *testing.T) {
	if got := SeasonBefore(time.Date(2026, time.February, 1, 0, 0, 0, 0, time.UTC)); got != "2025-fall" {
		t.Fatalf("SeasonBefore(2026-02) = %s, want 2025-fall", got)
	}
	if got := SeasonBefore(time.Date(2026, time.August, 1, 0, 0, 0, 0, time.UTC)); got != "2026-spring" {
		t.Fatalf("SeasonBefore(2026-08) = %s, want 2026-spring", got)
	}
}

func TestCatalogSnapshotStale(t *testing.T) {
	now := time.Date(2026, time.October, 11, 12, 0, 0, 0, time.UTC)
	at := func(age time.Duration) *time.Time { value := now.Add(-age); return &value }
	if !CatalogSnapshotStale(nil, now) {
		t.Fatal("a season that never refreshed successfully must be stale")
	}
	if CatalogSnapshotStale(at(CatalogFreshFor), now) {
		t.Fatal("an observation exactly 24 hours old is still fresh")
	}
	if !CatalogSnapshotStale(at(CatalogFreshFor+time.Second), now) {
		t.Fatal("an observation older than 24 hours must be stale")
	}
	if CatalogSnapshotStale(at(time.Hour), now) {
		t.Fatal("a recent observation must be fresh")
	}
}
