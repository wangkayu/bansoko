package application

import (
	"strings"
	"testing"
)

func TestReleaseSummaryNeverShowsRawVerdict(t *testing.T) {
	for verdict, label := range releaseVerdictSummaries {
		if got := releaseSummary(verdict); got != "评估结果："+label || strings.Contains(got, verdict) {
			t.Fatalf("releaseSummary(%q) = %q", verdict, got)
		}
	}
	if got := releaseSummary("unknown_verdict"); got != "评估完成" {
		t.Fatalf("unknown verdict summary = %q", got)
	}
}
