package downloadcompletion

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"bansoko/internal/batchplan"
	"bansoko/internal/release/facts"
)

func TestFactsOfFreezesIdentityAndRoots(t *testing.T) {
	correlation := `{"external_id":"hash:ABCDEF","info_hash":"` + "ABCDEF0123456789ABCDEF0123456789ABCDEF01" + `","path_snapshot":{"remote_root":"/echo","application_root":"/echo-app"}}`
	frozen := `{"path_snapshot":{"remote_root":"/downloads","application_root":"/data/downloads"}}`

	got := factsOf(candidate{Correlation: []byte(correlation), FrozenInput: []byte(frozen)})
	if got.InfoHash != "abcdef0123456789abcdef0123456789abcdef01" || got.ProviderRef != "hash:ABCDEF" {
		t.Fatalf("identity = %+v", got)
	}
	// The admission-time snapshot is the authority; the correlation only echoes it.
	if got.RemoteRoot != "/downloads" || got.ApplicationRoot != "/data/downloads" {
		t.Fatalf("roots = %+v", got)
	}

	fallback := factsOf(candidate{Correlation: []byte(correlation), FrozenInput: []byte(`{}`)})
	if fallback.RemoteRoot != "/echo" || fallback.ApplicationRoot != "/echo-app" {
		t.Fatalf("fallback roots = %+v", fallback)
	}

	for name, c := range map[string]candidate{
		"no correlation":     {Correlation: nil, FrozenInput: []byte(frozen)},
		"null correlation":   {Correlation: []byte("null"), FrozenInput: []byte(frozen)},
		"short hash":         {Correlation: []byte(`{"info_hash":"abc"}`), FrozenInput: []byte(frozen)},
		"non hex hash":       {Correlation: []byte(`{"info_hash":"zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz"}`), FrozenInput: []byte(frozen)},
		"invalid json":       {Correlation: []byte(`{`), FrozenInput: []byte(`{`)},
		"hash with newlines": {Correlation: []byte("{\"info_hash\":\"\\n1111111111111111111111111111111111111\"}"), FrozenInput: []byte(frozen)},
	} {
		if got := factsOf(c); got.InfoHash != "" {
			t.Fatalf("%s: a bad torrent identity was kept: %+v", name, got)
		}
	}
	empty := factsOf(candidate{Correlation: []byte(correlation), FrozenInput: []byte(`{"path_snapshot":{"remote_root":" ","application_root":""}}`)})
	if empty.RemoteRoot != "/echo" {
		t.Fatalf("a blank frozen root must fall back to the echo: %+v", empty)
	}
}

type snapshotStub struct {
	snapshot *facts.Snapshot
	err      error
}

func (s snapshotStub) ReadSnapshot(context.Context, string) (*facts.Snapshot, error) {
	return s.snapshot, s.err
}

func TestFrozenActionReadsTheBatchPlan(t *testing.T) {
	service := &Service{}
	for name, tc := range map[string]struct {
		snapshot *facts.Snapshot
		want     string
	}{
		"no snapshot":    {nil, ActionSeed},
		"no plan":        {&facts.Snapshot{}, ActionSeed},
		"plan without":   {&facts.Snapshot{ReleaseFacts: facts.ReleaseFacts{BatchPlan: &batchplan.Plan{}}}, ActionSeed},
		"pause":          {&facts.Snapshot{ReleaseFacts: facts.ReleaseFacts{BatchPlan: &batchplan.Plan{DownloadCompletion: ActionPause}}}, ActionPause},
		"remove":         {&facts.Snapshot{ReleaseFacts: facts.ReleaseFacts{BatchPlan: &batchplan.Plan{DownloadCompletion: ActionRemove}}}, ActionRemove},
		"remove data":    {&facts.Snapshot{ReleaseFacts: facts.ReleaseFacts{BatchPlan: &batchplan.Plan{DownloadCompletion: ActionRemoveWithData}}}, ActionRemoveWithData},
		"unknown action": {&facts.Snapshot{ReleaseFacts: facts.ReleaseFacts{BatchPlan: &batchplan.Plan{DownloadCompletion: "wipe"}}}, ActionSeed},
	} {
		service.snapshot = snapshotStub{snapshot: tc.snapshot}
		got, limits, err := service.frozenAction(context.Background(), "release")
		if err != nil || got != tc.want || limits != nil {
			t.Fatalf("%s: action %q err %v, want %q", name, got, err, tc.want)
		}
	}
	// A snapshot that cannot be read is an error, never a silent seed: the pass
	// leaves the acquisition unmarked and tries again.
	service.snapshot = snapshotStub{err: errors.New("database unavailable")}
	if _, _, err := service.frozenAction(context.Background(), "release"); err == nil {
		t.Fatal("an unreadable snapshot was read as seed")
	}
}

func TestFrozenActionReadsTheSeedLimits(t *testing.T) {
	minutes := int64(1440)
	limits := &batchplan.SeedLimits{SeedingTimeMinutes: &minutes, AtLimit: batchplan.SeedLimitRemove}
	service := &Service{snapshot: snapshotStub{snapshot: &facts.Snapshot{ReleaseFacts: facts.ReleaseFacts{BatchPlan: &batchplan.Plan{DownloadCompletion: ActionSeed, DownloadCompletionSeedLimits: limits}}}}}
	action, got, err := service.frozenAction(context.Background(), "release")
	if err != nil || action != ActionSeed || got == nil || !got.Equal(limits) {
		t.Fatalf("seed limits = %q %+v %v", action, got, err)
	}
	// Limits frozen next to another action are not seed limits.
	service.snapshot = snapshotStub{snapshot: &facts.Snapshot{ReleaseFacts: facts.ReleaseFacts{BatchPlan: &batchplan.Plan{DownloadCompletion: ActionPause, DownloadCompletionSeedLimits: limits}}}}
	if action, got, _ := service.frozenAction(context.Background(), "release"); action != ActionPause || got != nil {
		t.Fatalf("pause with limits = %q %+v", action, got)
	}
	// An invalid frozen value reads as seed without limits.
	service.snapshot = snapshotStub{snapshot: &facts.Snapshot{ReleaseFacts: facts.ReleaseFacts{BatchPlan: &batchplan.Plan{DownloadCompletion: ActionSeed, DownloadCompletionSeedLimits: &batchplan.SeedLimits{AtLimit: "wipe"}}}}}
	if action, got, _ := service.frozenAction(context.Background(), "release"); action != ActionSeed || got != nil {
		t.Fatalf("invalid limits = %q %+v", action, got)
	}
}

// A plan frozen before seed limits existed decodes without them.
func TestOldPlanJSONReadsWithoutSeedLimits(t *testing.T) {
	var plan batchplan.Plan
	if err := json.Unmarshal([]byte(`{"version":"v","file_kinds_version":"k","unclassified_route":"skip","download_completion":"seed","media_kind":"tv","files":[],"coverage_keys":[]}`), &plan); err != nil {
		t.Fatal(err)
	}
	if plan.CompletionAction() != ActionSeed || plan.CompletionSeedLimits() != nil {
		t.Fatalf("old plan = %+v", plan)
	}
}

// The action names the executor acts on are the names the section stores and
// the batch plan freezes.
func TestActionNamesMatchTheFrozenPlan(t *testing.T) {
	for _, pair := range [][2]string{
		{ActionSeed, batchplan.CompletionSeed}, {ActionPause, batchplan.CompletionPause},
		{ActionRemove, batchplan.CompletionRemove}, {ActionRemoveWithData, batchplan.CompletionRemoveWithData},
	} {
		if pair[0] != pair[1] {
			t.Fatalf("executor action %q != batch plan action %q", pair[0], pair[1])
		}
	}
}

func TestCleanupSeedLimitHandoffsUsesSavedRefsAndRetriesFailures(t *testing.T) {
	qb := &fakeQB{}
	rows := []seedLimitProviderRef{
		{ID: "completion-1", InfoHash: testHash, ProviderRef: "tag:bansoko-seed-1"},
		{ID: "completion-2", InfoHash: testHash, ProviderRef: "tag:bansoko-seed-2"},
	}
	cleaned, err := cleanupSeedLimitHandoffs(context.Background(), qb, rows)
	if err != nil {
		t.Fatal(err)
	}
	if len(cleaned) != 2 || cleaned[0] != rows[0].ID || cleaned[1] != rows[1].ID {
		t.Fatalf("converged post-handoff rows = %v", cleaned)
	}
	if len(qb.cleanupRefs) != 2 || qb.cleanupRefs[0] != rows[0].ProviderRef || qb.cleanupRefs[1] != rows[1].ProviderRef {
		t.Fatalf("post-handoff cleanup refs = %v", qb.cleanupRefs)
	}

	qb.cleanupErr = errors.New("connection reset")
	if _, err := cleanupSeedLimitHandoffs(context.Background(), qb, rows[:1]); err == nil {
		t.Fatal("a failed post-handoff cleanup must surface for the next RunPass")
	}
	if _, err := cleanupSeedLimitHandoffs(context.Background(), nil, rows); err == nil {
		t.Fatal("an unwired post-handoff cleanup must not be reported as converged")
	}
	if _, err := cleanupSeedLimitHandoffs(context.Background(), nil, nil); err != nil {
		t.Fatalf("an empty handoff batch needs no qB client: %v", err)
	}
}

func TestCleanupSeedLimitHandoffsWaitsForQBSeedLimitDelete(t *testing.T) {
	qb := &fakeQB{listed: []Torrent{{Hash: testHash, State: "uploading"}}}
	row := seedLimitProviderRef{ID: "completion-1", InfoHash: testHash, ProviderRef: "tag:bansoko-seed-1"}
	cleaned, err := cleanupSeedLimitHandoffs(context.Background(), qb, []seedLimitProviderRef{row})
	if err != nil {
		t.Fatal(err)
	}
	if len(cleaned) != 0 || len(qb.cleanupRefs) != 0 {
		t.Fatalf("an actively seeding task was marked converged: cleaned=%v refs=%v", cleaned, qb.cleanupRefs)
	}

	// qB can apply its at-limit delete long after the sync worker has exited.
	// The same saved completion record must then clean the now-unreferenced tag.
	qb.listed = nil
	cleaned, err = cleanupSeedLimitHandoffs(context.Background(), qb, []seedLimitProviderRef{row})
	if err != nil {
		t.Fatal(err)
	}
	if len(cleaned) != 1 || cleaned[0] != row.ID || len(qb.cleanupRefs) != 1 || qb.cleanupRefs[0] != row.ProviderRef {
		t.Fatalf("post-delete convergence: cleaned=%v refs=%v", cleaned, qb.cleanupRefs)
	}
}

func TestBoundedReasonRedactsPathsAndURLs(t *testing.T) {
	if boundedReason("").Valid {
		t.Fatal("an empty detail must stay NULL")
	}
	got := boundedReason("request qB delete: Post \"http://10.0.0.5:8080/api/v2/torrents/delete\": connection reset")
	if !got.Valid || len(got.String) > 600 {
		t.Fatalf("reason = %+v", got)
	}
	if contains(got.String, "10.0.0.5") {
		t.Fatalf("a URL leaked into the stored reason: %q", got.String)
	}
}

func contains(text, part string) bool {
	for i := 0; i+len(part) <= len(text); i++ {
		if text[i:i+len(part)] == part {
			return true
		}
	}
	return false
}
