package application

import (
	"context"
	"time"
)

// defaultPreviewFreshWindow is the shipped RSS refresh interval; it applies
// when the schedule cannot be read.
const defaultPreviewFreshWindow = 15 * time.Minute

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
