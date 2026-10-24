package domain

import (
	"testing"
	"time"
)

func TestRefreshBackoff(t *testing.T) {
	want := map[int]time.Duration{0: 0, 1: 5 * time.Minute, 2: 10 * time.Minute, 3: 20 * time.Minute, 4: 40 * time.Minute, 5: 80 * time.Minute, 6: 2 * time.Hour, 50: 2 * time.Hour}
	for failures, delay := range want {
		if got := RefreshBackoff(failures); got != delay {
			t.Fatalf("RefreshBackoff(%d) = %s, want %s", failures, got, delay)
		}
	}
}
