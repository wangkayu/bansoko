package filemaintenance

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	observabilityport "bansoko/internal/observability/port"
)

// A key decision writes its line at admission only when its verdict is final
// there; a replace verdict writes nothing until it ends (replaced or
// not_replaced); a verdict outside the decision CHECK is an error.
func TestAdmissionReplacementMessage(t *testing.T) {
	for verdict, want := range map[string]observabilityport.MessageCode{
		"keep":    observabilityport.MessageReplacementKeptExisting,
		"retain":  observabilityport.MessageReplacementKeptExisting,
		"queue":   observabilityport.MessageReplacementKeptAll,
		"blocked": observabilityport.MessageReplacementBlocked,
	} {
		got, final, err := admissionReplacementMessage(verdict)
		if err != nil || !final || got != want {
			t.Fatalf("admissionReplacementMessage(%q) = %q, %v, %v; want %q, true", verdict, got, final, err, want)
		}
		spec, ok := observabilityport.LookupMessage(got)
		if !ok || spec.Kind != observabilityport.ActivityKindReplacementDecided {
			t.Fatalf("message %q is not a replacement_decided message", got)
		}
	}
	if got, final, err := admissionReplacementMessage("replace"); err != nil || final || got != "" {
		t.Fatalf("admissionReplacementMessage(replace) = %q, %v, %v; want no admission line", got, final, err)
	}
	for _, verdict := range []string{"other", ""} {
		if got, final, err := admissionReplacementMessage(verdict); err == nil {
			t.Fatalf("admissionReplacementMessage(%q) = %q, %v; want an error", verdict, got, final)
		}
	}
}

// The mapping covers exactly the verdicts the decision CHECK admits.
func TestAdmissionReplacementMessageCoversTheVerdictCheck(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("..", "..", "db", "migrations", "*.sql"))
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(paths)
	pattern := regexp.MustCompile(`replacement_decisions_verdict_check CHECK \(verdict IN \(([^)]*)\)\)`)
	var latest []byte
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, match := range pattern.FindAllSubmatch(raw, -1) {
			latest = match[1]
		}
	}
	if latest == nil {
		t.Fatal("no migration defines replacement_decisions_verdict_check")
	}
	for _, value := range strings.Split(string(latest), ",") {
		verdict := strings.Trim(strings.TrimSpace(value), "'")
		if _, _, err := admissionReplacementMessage(verdict); err != nil {
			t.Errorf("verdict %q: %v", verdict, err)
		}
	}
}
