package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"reflect"
	"testing"

	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/testutil/animetest"
	"bansoko/internal/transport/httpapi/generated"
)

// TestSingleItemRulesPatchKeepsFrozenBusinessConsistent proves ruling 3: the
// merged anime_business_configs row is the one configuration source, so after a
// single-item rules PATCH the frozen business snapshot describes the patched
// configuration and its digest is the canonical hash of that snapshot.
func TestSingleItemRulesPatchKeepsFrozenBusinessConsistent(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := ownershipNewTestService(t, db, &ownershipFakeFetcher{})
	subscribe := ownershipSubscribeTestDraft(t, service, "subscribe-key-business-sync")
	animeID := subscribe.ManagedAnime.Id

	before := readBusinessConfigRow(t, db, animeID)
	if before.Snapshot == nil {
		t.Fatal("fixture must leave a frozen business snapshot")
	}

	if _, err := service.PatchAnimeRules(context.Background(), animeID, ownershipMustRulesPatch(t, generated.RulesPatch{
		ExpectedVersion: before.RulesVersion,
		Sections:        ownershipReplaceParserRules(t, []string{`\[(\d{1,4})\]`}).Sections,
	})); err != nil {
		t.Fatalf("rules patch: %v", err)
	}

	after := readBusinessConfigRow(t, db, animeID)
	if after.RulesVersion != before.RulesVersion+1 {
		t.Fatalf("rules version did not advance: %d -> %d", before.RulesVersion, after.RulesVersion)
	}
	if after.BusinessVersion != before.BusinessVersion+1 {
		t.Fatalf("business version did not advance with the patch: %d -> %d", before.BusinessVersion, after.BusinessVersion)
	}
	var section struct {
		EpisodePatterns []string `json:"episode_patterns"`
	}
	if err := json.Unmarshal(after.Snapshot.Sections[animedomain.SectionParser], &section); err != nil {
		t.Fatal(err)
	}
	if len(section.EpisodePatterns) != 1 || section.EpisodePatterns[0] != `\[(\d{1,4})\]` {
		t.Fatalf("snapshot parser section did not follow the patch: %+v", section)
	}
	if after.Snapshot.SectionVersions[animedomain.SectionParser] != 0 {
		t.Fatalf("patched section provenance = %d, want 0 (instance override won)", after.Snapshot.SectionVersions[animedomain.SectionParser])
	}
	// Only the patched section may change: every other section keeps its value.
	for name, beforeValue := range before.Snapshot.Sections {
		if name == animedomain.SectionParser {
			continue
		}
		if !reflect.DeepEqual(beforeValue, after.Snapshot.Sections[name]) {
			t.Fatalf("section %s changed with the rules patch: %s -> %s", name, beforeValue, after.Snapshot.Sections[name])
		}
	}
	if after.Snapshot.Parser != before.Snapshot.Parser {
		t.Fatalf("parser envelope changed with the rules patch: %+v -> %+v", before.Snapshot.Parser, after.Snapshot.Parser)
	}
	digest, err := after.Snapshot.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if digest != after.BusinessDigest {
		t.Fatalf("stored digest %s is not the snapshot digest %s", after.BusinessDigest, digest)
	}
}

type businessConfigRow struct {
	RulesVersion    int64
	BusinessVersion int64
	BusinessDigest  string
	Snapshot        *animedomain.BusinessSnapshot
}

func readBusinessConfigRow(t *testing.T, db *sql.DB, animeID string) businessConfigRow {
	t.Helper()
	var (
		row    businessConfigRow
		raw    []byte
		digest sql.NullString
	)
	if err := db.QueryRowContext(context.Background(), `
SELECT rules_version, business_version, business_snapshot, business_digest
FROM anime_business_configs WHERE managed_anime_id=$1`, animeID).
		Scan(&row.RulesVersion, &row.BusinessVersion, &raw, &digest); err != nil {
		t.Fatal(err)
	}
	if len(raw) > 0 {
		var snapshot animedomain.BusinessSnapshot
		if err := json.Unmarshal(raw, &snapshot); err != nil {
			t.Fatal(err)
		}
		row.Snapshot = &snapshot
		row.BusinessDigest = digest.String
	}
	return row
}
