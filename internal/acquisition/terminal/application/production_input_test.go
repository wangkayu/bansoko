package application

import (
	anime "bansoko/internal/anime/domain"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
	"testing"
)

func TestProductionLimitsMatchRuntimePolicy(t *testing.T) {
	v, err := productionLimits(nil, anime.DefaultDownloadTimeouts())
	if err != nil {
		t.Fatal(err)
	}
	stages := map[string]string{workitems.KindQBSubmit: "submit", workitems.KindArtifactInspect: "inspect", workitems.KindPostProcessPlan: "plan", workitems.KindFileExecute: "file"}
	for _, p := range worker.DefaultPolicies() {
		if name, ok := stages[p.Kind]; ok {
			got := v.Object[name].Object
			want := int64(p.BusinessAttemptTimeout().Seconds())
			if got["max_attempts"].Int != int64(p.MaxAttempts) || got["attempt_timeout_seconds"].Int != want {
				t.Fatalf("%s differs from KindPolicy", name)
			}
			// Sync obligation: the migration's DDL DEFAULTs for
			// the inspect/plan wall mirror this value (86400s). Changing it
			// requires a forward migration that moves those DEFAULTs in the
			// same slice (see 183_inspect_plan_attempt_budget_test.go).
			if (name == "inspect" || name == "plan") && got["wall_seconds"].Int != 86400 {
				t.Fatalf("%s wall_seconds=%d drifted from the 183 DDL DEFAULT (86400)", name, got["wall_seconds"].Int)
			}
		}
	}
}

// Invariant: the inspect/plan business budget frozen into the
// limits snapshot never exceeds the claim budget the SQL work-creation paths
// use; recovery (150/152) judges exhaustion by the claim budget.
func TestProductionLimitsBusinessBudgetWithinClaimBudget(t *testing.T) {
	v, err := productionLimits(nil, anime.DefaultDownloadTimeouts())
	if err != nil {
		t.Fatal(err)
	}
	for name, claimMax := range map[string]int64{"inspect": workitems.ArtifactInspectClaimMaxAttempts, "plan": workitems.PostProcessPlanClaimMaxAttempts} {
		if got := v.Object[name].Object["max_attempts"].Int; got < 1 || got > claimMax {
			t.Fatalf("%s business max_attempts=%d exceeds claim budget %d", name, got, claimMax)
		}
	}
}

// A progressing slow copy is not cut off by a fixed wall clock: the file
// stage budget grows with the largest accepted file at the storage floor and
// stays within the cap and the stage wall.
func TestProductionLimitsScaleFileStageWithLargestFile(t *testing.T) {
	base := int64(0)
	for _, p := range worker.DefaultPolicies() {
		if p.Kind == workitems.KindFileExecute {
			base = int64(p.BusinessAttemptTimeout().Seconds())
		}
	}
	const gib = int64(1) << 30
	v, err := productionLimits([]int64{gib / 4, 3 * gib, gib}, anime.DefaultDownloadTimeouts())
	if err != nil {
		t.Fatal(err)
	}
	got := v.Object["file"].Object["attempt_timeout_seconds"].Int
	// 3 GiB × 2 passes (copy read and write) at 10 MiB/s is 614.4s of
	// transfer on top of the base.
	if want := base + 615; got != want {
		t.Fatalf("file attempt timeout=%d want %d", got, want)
	}
	v, err = productionLimits([]int64{1 << 50}, anime.DefaultDownloadTimeouts())
	if err != nil {
		t.Fatal(err)
	}
	if got := v.Object["file"].Object["attempt_timeout_seconds"].Int; got != int64(worker.MaxStorageAttemptTimeout.Seconds()) || got > v.Object["file"].Object["wall_seconds"].Int {
		t.Fatalf("huge file attempt timeout=%d want cap %v", got, worker.MaxStorageAttemptTimeout)
	}
}

// One inspect attempt covers hashing the whole batch plus the primary
// re-read, so a large batch on slow storage fits a single attempt.
func TestProductionLimitsScaleInspectStageWithBatchBytes(t *testing.T) {
	base := int64(0)
	for _, p := range worker.DefaultPolicies() {
		if p.Kind == workitems.KindArtifactInspect {
			base = int64(p.BusinessAttemptTimeout().Seconds())
		}
	}
	const gib = int64(1) << 30
	// 12 x 5 GiB: 60 GiB hashed plus a 5 GiB re-read at 10 MiB/s = 6656s.
	sizes := make([]int64, 12)
	for i := range sizes {
		sizes[i] = 5 * gib
	}
	v, err := productionLimits(sizes, anime.DefaultDownloadTimeouts())
	if err != nil {
		t.Fatal(err)
	}
	if got, want := v.Object["inspect"].Object["attempt_timeout_seconds"].Int, base+6656; got != want {
		t.Fatalf("inspect attempt timeout=%d want %d", got, want)
	}
}

// A plan attempt grows with the file count only: satisfaction stats targets
// and never reads content.
func TestProductionLimitsScalePlanStageWithFiles(t *testing.T) {
	base := int64(0)
	for _, p := range worker.DefaultPolicies() {
		if p.Kind == workitems.KindPostProcessPlan {
			base = int64(p.BusinessAttemptTimeout().Seconds())
		}
	}
	const gib = int64(1) << 30
	sizes := make([]int64, 24)
	for i := range sizes {
		sizes[i] = gib / 2
	}
	v, err := productionLimits(sizes, anime.DefaultDownloadTimeouts())
	if err != nil {
		t.Fatal(err)
	}
	// 24 files x 2s; no byte-scaled transfer.
	if got, want := v.Object["plan"].Object["attempt_timeout_seconds"].Int, base+48; got != want {
		t.Fatalf("plan attempt timeout=%d want %d", got, want)
	}
}

// The configured Global download timeouts are what acceptance freezes: both
// values land in the limits unchanged and max_polls follows the wait.
func TestProductionLimitsFreezeConfiguredDownloadTimeouts(t *testing.T) {
	for _, timeouts := range []anime.DownloadTimeouts{
		anime.DefaultDownloadTimeouts(),
		{StallSeconds: 2 * 3600, DownloadWaitSeconds: 3 * 86400},
		{StallSeconds: anime.MinDownloadStallSeconds, DownloadWaitSeconds: anime.MinDownloadWaitSeconds},
		{StallSeconds: anime.MaxDownloadStallSeconds, DownloadWaitSeconds: anime.MaxDownloadWaitSeconds},
		{StallSeconds: 3601, DownloadWaitSeconds: 3601},
	} {
		v, err := productionLimits(nil, timeouts)
		if err != nil {
			t.Fatalf("%+v: %v", timeouts, err)
		}
		if got := v.Object["stall_seconds"].Int; got != timeouts.StallSeconds {
			t.Fatalf("%+v: stall_seconds=%d", timeouts, got)
		}
		if got := v.Object["download_wait_seconds"].Int; got != timeouts.DownloadWaitSeconds {
			t.Fatalf("%+v: download_wait_seconds=%d", timeouts, got)
		}
		sync := v.Object["sync"].Object
		interval := sync["poll_interval_seconds"].Int
		if want := (timeouts.DownloadWaitSeconds+interval-1)/interval + 1; sync["max_polls"].Int != want {
			t.Fatalf("%+v: max_polls=%d want %d", timeouts, sync["max_polls"].Int, want)
		}
	}
	// The default keeps the former fixed values (7 days, 10081 polls, 6 h).
	v, err := productionLimits(nil, anime.DefaultDownloadTimeouts())
	if err != nil {
		t.Fatal(err)
	}
	if v.Object["download_wait_seconds"].Int != 604800 || v.Object["sync"].Object["max_polls"].Int != 10081 || v.Object["stall_seconds"].Int != 21600 {
		t.Fatalf("default limits drifted: %+v", v.Object)
	}
}

func TestProductionLimitsRefuseInvalidDownloadTimeouts(t *testing.T) {
	for _, timeouts := range []anime.DownloadTimeouts{
		{},
		{StallSeconds: anime.MinDownloadStallSeconds - 1, DownloadWaitSeconds: anime.DefaultDownloadWaitSeconds},
		{StallSeconds: anime.DefaultDownloadStallSeconds, DownloadWaitSeconds: anime.MaxDownloadWaitSeconds + 1},
		{StallSeconds: 2 * 86400, DownloadWaitSeconds: 86400},
	} {
		if _, err := productionLimits(nil, timeouts); err == nil {
			t.Fatalf("%+v accepted", timeouts)
		}
	}
}
