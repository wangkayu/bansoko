package application

import (
	"context"
	"testing"

	"bansoko/internal/testutil/animetest"
	"bansoko/internal/testutil/disposabledb"
)

// A displaced file names the *other* instance that took it over. When this
// instance's own redownload took the file (the same managed anime), the record
// itself is the taker, so no successor is reported rather than a link to
// itself.
func TestAcquisitionRecordReportsOnlyAnotherInstancesSuccessor(t *testing.T) {
	for _, tc := range []struct {
		name       string
		successor  string
		wantTitle  string
		wantLinked bool
	}{
		{name: "another instance", successor: "anime-other", wantTitle: "Other Show", wantLinked: true},
		{name: "this instance", successor: "anime-failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := animetest.OpenTestDB(t)
			ctx := context.Background()
			root := "record-successor"
			seedFailedRecordRoot(t, db, root)
			if tc.wantLinked {
				if _, err := disposabledb.Owner(t).ExecContext(ctx, `INSERT INTO managed_anime(id, title) VALUES ('anime-other', 'Other Show')`); err != nil {
					t.Fatal(err)
				}
			}
			// A terminal state is entered from active only, so seed the holder and
			// take the overwrite displacement with the successor instance named.
			if _, err := disposabledb.Owner(t).ExecContext(ctx, `
INSERT INTO placements(id, physical_path, managed_anime_id, source_evidence_type, source_owner_ref,
 source_fingerprint, target_fingerprint, quality_facts_ref, facts_version, verification_state, state)
VALUES ('plc-displaced', '/library/Failed Show/file-a.mkv', 'anime-failed', 'file_operation', 'fo-failed',
 'sha256:a', 'sha256:a', 'rel-failed', 'parser-v1', 'verified', 'active')`); err != nil {
				t.Fatal(err)
			}
			if _, err := disposabledb.Owner(t).ExecContext(ctx, `
UPDATE placements SET state = 'displaced', displaced_at = CURRENT_TIMESTAMP, displaced_cause = 'overwrite',
 displaced_by_placement_id = 'plc-successor', displaced_by_anime_id = $1, displaced_by_ref = 'fixture'
WHERE id = 'plc-displaced'`, tc.successor); err != nil {
				t.Fatal(err)
			}

			service := readmodelNewTestService(t, db, nil)
			page, err := service.ListAcquisitionRecordFiles(ctx, root, AcquisitionFileQuery{})
			if err != nil {
				t.Fatal(err)
			}
			if len(page.Items) != 1 || page.Items[0].PlacementEnd == nil {
				t.Fatalf("files = %+v, want one displaced file", page.Items)
			}
			end := page.Items[0].PlacementEnd
			if end.State != "displaced" || end.Cause == nil || *end.Cause != "overwrite" {
				t.Fatalf("placement end = %+v", end)
			}
			if !tc.wantLinked {
				if end.Successor != nil {
					t.Fatalf("a same-instance takeover reports a successor: %+v", end.Successor)
				}
				return
			}
			if end.Successor == nil || end.Successor.ManagedAnimeID != tc.successor || end.Successor.Title != tc.wantTitle {
				t.Fatalf("successor = %+v, want %s", end.Successor, tc.successor)
			}
		})
	}
}
