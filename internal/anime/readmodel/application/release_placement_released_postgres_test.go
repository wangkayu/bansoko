package application

import (
	"context"
	"testing"

	rssapplication "bansoko/internal/rss/application"
	"bansoko/internal/testutil/animetest"
	"bansoko/internal/testutil/disposabledb"
)

// The run-again gate tells a release whose files were released from management
// apart from one whose files are still in the library. Only a release that has a
// released placement and no active verified one left reads placement_released,
// so the owner row explains the greyed retry instead of claiming the files are
// in the library.
func TestReleasePlacementReleasedFact(t *testing.T) {
	db := animetest.OpenTestDB(t)
	ctx := context.Background()
	seedFailedRecordRoot(t, db, "record-released-fact")
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := disposabledb.Owner(t).ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("seed released fact: %v", err)
		}
	}
	released := func() bool {
		t.Helper()
		var value bool
		if err := db.QueryRowContext(ctx, `
SELECT `+rssapplication.ReleasePlacementReleasedSQL("sub.managed_anime_id")+`
FROM releases r
JOIN rss_sources src ON src.id = r.rss_source_id
JOIN subscriptions sub ON sub.id = src.subscription_id
WHERE r.id = 'rel-failed'`).Scan(&value); err != nil {
			t.Fatalf("read released fact: %v", err)
		}
		return value
	}
	place := func(id, ownerRef, verification, state string) {
		t.Helper()
		exec(`
INSERT INTO placements(id, physical_path, managed_anime_id, source_evidence_type, source_owner_ref,
 source_fingerprint, target_fingerprint, quality_facts_ref, facts_version, verification_state, state)
VALUES ($1, '/library/Failed Show/' || $1 || '.mkv', 'anime-failed', 'file_operation', $2,
 'sha256:a', 'sha256:a', 'rel-failed', 'parser-v1', $3, $4)`, id, ownerRef, verification, state)
	}

	if released() {
		t.Fatal("a release with no placement reads released")
	}
	place("plc-released", "fo-released", "verified", "active")
	if released() {
		t.Fatal("a release holding an active placement reads released")
	}
	exec(`UPDATE placements SET state = 'released', released_at = CURRENT_TIMESTAMP,
 released_by = 'user:placement_issue', release_request_key = 'fixture-release-1'
WHERE id = 'plc-released'`)
	if !released() {
		t.Fatal("a released placement with no active one left does not read released")
	}
	// One active placement of the same release still holds files: the release is
	// not the released one, and the library reason stays the accurate one.
	place("plc-still-active", "fo-still-active", "verified", "active")
	if released() {
		t.Fatal("a release with an active placement left reads released")
	}
}
