package application

import (
	"testing"
	"time"
)

func TestPendingBenchActionMarksStartBaseline(t *testing.T) {
	baseline := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	before, after := baseline.Add(-time.Hour), baseline.Add(time.Hour)
	on := Subscription{NewReleasesOnly: true, StartBaselineAt: &baseline}
	cases := []struct {
		name         string
		subscription Subscription
		published    *time.Time
		want         string
	}{
		{"published before the creation", on, &before, rssListReasonStartBaseline},
		{"no publish time", on, nil, rssListReasonStartBaseline},
		{"published after the creation", on, &after, rssListReasonNotStarted},
		{"option off", Subscription{}, &before, rssListReasonNotStarted},
	}
	for _, tc := range cases {
		action := pendingBenchAction(tc.subscription, tc.published)
		if action.Allowed || action.Kind != RSSListDownloadNone || action.ReasonCode != tc.want {
			t.Errorf("%s: action = %+v, want blocked with %s", tc.name, action, tc.want)
		}
	}
}
