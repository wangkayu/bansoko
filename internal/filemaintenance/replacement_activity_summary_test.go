package filemaintenance

import (
	"strings"
	"testing"
)

// The business-history line names the outcome in words; the typed verdict
// stays in the reason code and never shows raw.
func TestReplacementActivitySummaryNeverShowsTheVerdict(t *testing.T) {
	for verdict, want := range map[string]string{
		// A replace decision is planned at acceptance; only its settle
		// writes the replacement as done.
		"replace":  "替换版本：将替换旧文件",
		"replaced": "替换版本：已替换旧文件",
		"keep":     "替换版本：保留现有文件",
		"retain":   "替换版本：保留现有文件",
		"queue":    "替换版本：保留所有版本",
		"blocked":  "替换版本：无法比较，未替换",
		"other":    "替换版本已评估",
	} {
		got := replacementActivitySummary(verdict)
		if got != want {
			t.Fatalf("replacementActivitySummary(%q) = %q, want %q", verdict, got, want)
		}
		if strings.Contains(got, verdict) || strings.Contains(got, "洗版") {
			t.Fatalf("replacementActivitySummary(%q) = %q shows an internal word", verdict, got)
		}
	}
}
