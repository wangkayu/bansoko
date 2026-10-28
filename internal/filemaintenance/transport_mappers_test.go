package filemaintenance

import (
	"database/sql"
	"testing"
	"time"
)

func TestReplacementDecisionViewToGeneratedPreservesIsolation(t *testing.T) {
	inputDigest := "digest"
	candidatePlacementID := "candidate-placement"
	snapshotGeneration := 7
	subscriptionVersion := 11
	reason := "loser"
	pools := []ReplacementPoolEvidence{{
		PoolKey: "group",
		Reason:  &reason,
		Verdict: "replace",
		Winner: &ReplacementPlacementRef{
			Fingerprint: "winner-fingerprint",
			PlacementID: "winner-placement",
			Version:     3,
		},
		Losers: &[]ReplacementPlacementRef{{
			Fingerprint: "loser-fingerprint",
			PlacementID: "loser-placement",
			Version:     2,
		}},
	}}
	view := ReplacementDecisionView{
		AdmissionMode:        "ranked",
		CandidatePlacementID: &candidatePlacementID,
		CandidateReleaseID:   "release",
		CreatedAt:            time.Unix(10, 0),
		ID:                   "decision",
		InputDigest:          &inputDigest,
		ManagedAnimeID:       "anime",
		PolicyVersion:        4,
		Pools:                &pools,
		ProfileVersion:       "profile",
		SnapshotGeneration:   &snapshotGeneration,
		StaleReason:          &reason,
		State:                "admitted",
		SubscriptionVersion:  &subscriptionVersion,
		Verdict:              "replace",
		Version:              5,
	}

	mapped := replacementDecisionViewToGenerated(view)
	*mapped.CandidatePlacementId = "changed"
	*mapped.InputDigest = "changed"
	(*mapped.Pools)[0].PoolKey = "changed"
	(*mapped.Pools)[0].Winner.PlacementId = "changed"
	(*(*mapped.Pools)[0].Losers)[0].Fingerprint = "changed"

	if *view.CandidatePlacementID != "candidate-placement" || *view.InputDigest != "digest" {
		t.Fatal("scalar pointers alias application view")
	}
	if (*view.Pools)[0].PoolKey != "group" || (*view.Pools)[0].Winner.PlacementID != "winner-placement" {
		t.Fatal("nested pool values alias application view")
	}
	if (*(*view.Pools)[0].Losers)[0].Fingerprint != "loser-fingerprint" {
		t.Fatal("loser slice aliases application view")
	}
}

func TestReplacementDecisionViewToGeneratedPreservesPoolShape(t *testing.T) {
	if mapped := replacementDecisionViewToGenerated(ReplacementDecisionView{}); mapped.Pools != nil {
		t.Fatal("nil pools became non-nil")
	}

	mapped := replacementDecisionViewToGenerated(ReplacementDecisionView{Pools: &[]ReplacementPoolEvidence{}})
	if mapped.Pools == nil || len(*mapped.Pools) != 0 {
		t.Fatal("empty pools shape was not preserved")
	}
}

// Frozen comparison steps reach the decision view as stored: the scanner keeps
// them and the transport maps them field by field (empty values omitted).
func TestReplacementDecisionStepsReachTheView(t *testing.T) {
	raw := []byte(`[{"pool_key":"tv:e1","verdict":"replace","steps":[{"key":"subgroup.score","candidate":"90","current":"10","result":"equal"},{"key":"anibt.resolution","candidate":"1080p","current":"720p","candidate_listed":true,"current_listed":true,"result":"better"}]}]`)
	view, err := scanDecisionViewApplication(decisionScan(raw))
	if err != nil {
		t.Fatal(err)
	}
	mapped := replacementDecisionViewToGenerated(view)
	steps := (*mapped.Pools)[0].Steps
	if steps == nil || len(*steps) != 2 {
		t.Fatalf("steps = %+v", steps)
	}
	first, second := (*steps)[0], (*steps)[1]
	if first.Key != "subgroup.score" || string(first.Result) != "equal" || first.CandidateListed != nil || *first.Candidate != "90" {
		t.Fatalf("first step = %+v", first)
	}
	if second.Key != "anibt.resolution" || string(second.Result) != "better" || second.CandidateListed == nil || !*second.CurrentListed || *second.Current != "720p" {
		t.Fatalf("second step = %+v", second)
	}
	if empty := replacementDecisionViewToGenerated(ReplacementDecisionView{Pools: &[]ReplacementPoolEvidence{{PoolKey: "k"}}}); (*empty.Pools)[0].Steps != nil {
		t.Fatal("a pool without steps must omit them")
	}
}

func TestReplacementDecisionScannerPreservesJSONPoolShape(t *testing.T) {
	for _, test := range []struct {
		name           string
		raw            []byte
		wantPools      bool
		wantEmptyPool  bool
		wantEmptyLoser bool
	}{
		{name: "null", raw: []byte("null")},
		{name: "empty", raw: []byte("[]"), wantPools: true, wantEmptyPool: true},
		{name: "empty losers", raw: []byte(`[{"pool_key":"group","verdict":"retain","losers":[]}]`), wantPools: true, wantEmptyLoser: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			view, err := scanDecisionViewApplication(decisionScan(test.raw))
			if err != nil {
				t.Fatal(err)
			}
			if (view.Pools != nil) != test.wantPools {
				t.Fatalf("pools nil=%t, want nil=%t", view.Pools == nil, !test.wantPools)
			}
			if test.wantEmptyPool && len(*view.Pools) != 0 {
				t.Fatalf("got %d pools, want empty", len(*view.Pools))
			}
			if test.wantEmptyLoser && ((*view.Pools)[0].Losers == nil || len(*(*view.Pools)[0].Losers) != 0) {
				t.Fatal("empty loser array shape was not preserved")
			}
		})
	}
}

func decisionScan(pools []byte) func(...any) error {
	return func(dest ...any) error {
		*dest[0].(*string) = "decision"
		*dest[1].(*string) = "anime"
		*dest[2].(*string) = "release"
		*dest[3].(*string) = "tv:e1"
		*dest[4].(*sql.NullString) = sql.NullString{}
		*dest[5].(*int) = 1
		*dest[6].(*string) = "profile"
		*dest[7].(*sql.NullInt64) = sql.NullInt64{}
		*dest[8].(*sql.NullString) = sql.NullString{}
		*dest[9].(*sql.NullInt64) = sql.NullInt64{}
		*dest[10].(*string) = "ranked"
		*dest[11].(*string) = "retain"
		*dest[12].(*string) = "admitted"
		*dest[13].(*sql.NullString) = sql.NullString{}
		*dest[14].(*[]byte) = pools
		*dest[15].(*int) = 1
		*dest[16].(*time.Time) = time.Unix(10, 0)
		return nil
	}
}

func TestPlacementMappersPreserveEmptySliceShape(t *testing.T) {
	if mapped := placementViewsToGenerated([]PlacementView{}); mapped == nil || len(mapped) != 0 {
		t.Fatal("empty placement list shape was not preserved")
	}
	if mapped := placementCoverageViewsToGenerated([]PlacementCoverageView{}); mapped == nil || len(mapped) != 0 {
		t.Fatal("empty coverage list shape was not preserved")
	}
}
