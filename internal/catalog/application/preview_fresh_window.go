package application

import (
	"context"
	"time"
)

// defaultPreviewFreshWindow is the shipped RSS refresh interval; it applies
// when the schedule cannot be read.
const defaultPreviewFreshWindow = 15 * time.Minute

// defaultSearchFreshWindow is the fresh window of a keyword search snapshot. A
// search is not a scheduled feed: nothing refreshes it, so borrowing the RSS
// refresh interval would make a result a user is still reading stale within
// minutes. It gets its own hour, well inside the 24-hour retention the
// publication function applies.
const defaultSearchFreshWindow = time.Hour

// searchFreshWindow is the single fresh window of search snapshots.
func (s *Service) searchFreshWindow() time.Duration {
	return defaultSearchFreshWindow
}

// previewFreshWindow is the single fresh window of provider and custom feed
// preview snapshots: one RSS refresh interval, so a preview is reused exactly
// as long as the scheduled refresh would not have looked again.
func (s *Service) previewFreshWindow(ctx context.Context) time.Duration {
	if s == nil || s.db == nil {
		return defaultPreviewFreshWindow
	}
	var value int
	var unit string
	if err := s.db.QueryRowContext(ctx, `SELECT interval_value, interval_unit FROM automation_schedules WHERE kind = 'rss_refresh'`).Scan(&value, &unit); err != nil {
		return defaultPreviewFreshWindow
	}
	var step time.Duration
	switch unit {
	case "minute":
		step = time.Minute
	case "hour":
		step = time.Hour
	case "day":
		step = 24 * time.Hour
	}
	if window := time.Duration(value) * step; window > 0 {
		return window
	}
	return defaultPreviewFreshWindow
}
