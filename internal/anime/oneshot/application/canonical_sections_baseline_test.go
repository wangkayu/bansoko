package application

import (
	animedomain "bansoko/internal/anime/domain"
	"os"
	"strings"
	"testing"
)

func TestCandidateCanonicalSectionsBaselineMatchesDomain(t *testing.T) {
	data, err := os.ReadFile("../../../../db/migrations/001_baseline.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(data)
	start := strings.Index(sql, "canonical_sections TEXT[] := ARRAY[")
	if start < 0 {
		t.Fatal("missing canonical set")
	}
	end := strings.Index(sql[start:], "];")
	if end < 0 {
		t.Fatal("unterminated canonical set")
	}
	set := sql[start : start+end]
	if strings.Count(set, "'") != 2*len(animedomain.EffectiveSectionOrder) {
		t.Fatalf("canonical set differs from domain: %s", set)
	}
	for _, name := range animedomain.EffectiveSectionOrder {
		if !strings.Contains(set, "'"+name+"'") {
			t.Fatalf("missing %s", name)
		}
	}
	if strings.Count(sql, "<> cardinality(canonical_sections)") != 3 {
		t.Fatal("all three frozen section maps must have exact keys")
	}
}
