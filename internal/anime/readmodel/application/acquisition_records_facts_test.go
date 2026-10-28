package application

import (
	"database/sql"
	"reflect"
	"strconv"
	"testing"
	"time"
)

// TestFrozenPlanEpisodes pins the record's episode list to the frozen batch
// plan coverage keys: numbered TV keys project to ascending unique numbers,
// anything else (a movie, a fractional or unknown key, a missing or corrupt
// plan, an oversized list) reads as absent.
func TestFrozenPlanEpisodes(t *testing.T) {
	valid := func(text string) sql.NullString { return sql.NullString{String: text, Valid: true} }
	oversized := "["
	for i := 0; i <= acquisitionEpisodesMax; i++ {
		if i > 0 {
			oversized += ","
		}
		oversized += `"tv:e` + strconv.Itoa(i+1) + `"`
	}
	oversized += "]"
	for _, tc := range []struct {
		name string
		raw  sql.NullString
		want []int
	}{
		{"single episode", valid(`["tv:e7"]`), []int{7}},
		{"batch ascending", valid(`["tv:e1","tv:e3","tv:e2"]`), []int{1, 2, 3}},
		{"movie key", valid(`["movie:0"]`), nil},
		{"fractional key", valid(`["tv:e7.5"]`), nil},
		{"no snapshot", sql.NullString{}, nil},
		{"null plan", valid(`null`), nil},
		{"corrupt plan", valid(`{"not":"a list"}`), nil},
		{"empty plan", valid(`[]`), nil},
		{"over the bound", valid(oversized), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := frozenPlanEpisodes(tc.raw); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("episodes = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestDownloadTransfer pins the transfer to the persisted byte observation:
// no mark, no positive total or an incoherent pair shows nothing, and the
// speed rides along only when the poll recorded one.
func TestDownloadTransfer(t *testing.T) {
	at := sql.NullTime{Time: time.Date(2026, 10, 20, 1, 0, 0, 0, time.UTC), Valid: true}
	n := func(v int64) sql.NullInt64 { return sql.NullInt64{Int64: v, Valid: true} }
	s := func(v string) sql.NullString { return sql.NullString{String: v, Valid: true} }
	got := downloadTransfer(n(50), n(200), s("1024"), at)
	if got == nil || got.CompletedBytes != 50 || got.TotalBytes != 200 || got.DownloadSpeed == nil || *got.DownloadSpeed != 1024 || !got.ObservedAt.Equal(at.Time) {
		t.Fatalf("transfer = %+v", got)
	}
	if got := downloadTransfer(n(0), n(200), s("0"), at); got == nil || got.DownloadSpeed == nil || *got.DownloadSpeed != 0 {
		t.Fatalf("a reported zero must stay a zero: %+v", got)
	}
	if got := downloadTransfer(n(50), n(200), sql.NullString{}, at); got == nil || got.DownloadSpeed != nil {
		t.Fatalf("an observation without speed keeps speed absent: %+v", got)
	}
	if got := downloadTransfer(n(50), n(200), s("fast"), at); got == nil || got.DownloadSpeed != nil {
		t.Fatalf("an unreadable speed is absent: %+v", got)
	}
	for name, args := range map[string][4]any{
		"no mark":      {sql.NullInt64{}, n(200), s("1"), at},
		"no total":     {n(5), sql.NullInt64{}, s("1"), at},
		"zero total":   {n(0), n(0), s("1"), at},
		"over total":   {n(300), n(200), s("1"), at},
		"no timestamp": {n(5), n(200), s("1"), sql.NullTime{}},
	} {
		if got := downloadTransfer(args[0].(sql.NullInt64), args[1].(sql.NullInt64), args[2].(sql.NullString), args[3].(sql.NullTime)); got != nil {
			t.Fatalf("%s must show no transfer: %+v", name, got)
		}
	}
}
