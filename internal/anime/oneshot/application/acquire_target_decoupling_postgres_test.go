package application

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	acquisitiontarget "bansoko/internal/acquisition/target"
	"bansoko/internal/postprocessing/naming"
	"bansoko/internal/testutil/frozenpolicy"
	"bansoko/internal/transport/httpapi/generated"
)

// The one-shot download admission is decoupled from
// media-library reachability and naming completeness. A target plan that fails
// for a naming or library condition no longer refuses the command: the
// admission proceeds to qB, the frozen target facts record the typed failure
// and the accepted chain carries the fault in its post-processing stages.
// Only a real target-state refusal (collision policy, or an entirely
// file-filter-excluded set) stays a synchronous typed rejection. No bypass exists.

// A relative directory template is not a runtime naming condition: targets are
// absolute by construction and the admission persists the submitted draft's
// resolved configuration, so it is refused with the same typed validation the
// candidate save applies. No owner row is written and no download is admitted.
// The same decoupling still applies to a real render failure — see
// TestAcquireSeedOnceProceedsWithUnresolvedNaming.
func TestAcquireSeedOnceRefusesRelativeNamingTarget(t *testing.T) {
	service, _, db, seed, refresh := humanOneShotFixture(t)
	ctx := context.Background()

	snapshot := refresh([]acquisitiondomain.FeedItem{seedFeedItem(seed)})
	_, input := currentInputForSeed(t, service, seed, func(d generated.AnimeDraft) generated.AnimeDraft {
		rules := generated.NamingRules{
			Language: generated.NamingRulesLanguage(naming.Language),
			Tv: generated.NamingVariant{
				DirectoryTemplate:     "Show",
				VideoFilenameTemplate: "${sanitize(file.name)}",
			},
			Movie: generated.NamingVariant{
				DirectoryTemplate:     "Show",
				VideoFilenameTemplate: "${sanitize(file.name)}",
			},
			Unknown: generated.NamingVariant{
				DirectoryTemplate:     "Show/extras",
				VideoFilenameTemplate: "${sanitize(file.name)}",
			},
		}
		var override generated.NamingRulesOverride
		if err := override.FromNamingRulesReplace(generated.NamingRulesReplace{
			Mode:  generated.NamingRulesReplaceModeReplace,
			Value: rules,
		}); err != nil {
			t.Fatal(err)
		}
		d.Rules.Sections.Naming = &override
		return d
	})
	input.Seed = bindSeedIdentity(seed, snapshot)
	business, err := frozenpolicy.SeedBusiness(ctx, db, `vars.actual = {title: input.release_title};`)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := business.Digest()
	if err != nil {
		t.Fatal(err)
	}
	input.Draft.Business = &business
	input.Draft.BusinessDigest = &digest

	_, err = service.AcquireSeedOnce(ctx, input, "one-shot-relative-target-0001")
	if code := typedErrorCode(err); code != naming.ErrCodeNamingTargetNotAbsolute {
		t.Fatalf("AcquireSeedOnce with a relative naming target err = %v, want %s", err, naming.ErrCodeNamingTargetNotAbsolute)
	}
	assertNoOneShotOwnerRows(t, service)
}

// A naming template that genuinely fails to render is a naming condition, not
// a refusal: the download is admitted and the typed diagnostic freezes with
// the input. The template reads anibt.groupslug, which passes the draft's
// validation but is undefined for this Mikan release, so the render fails
// with NAMING_FACT_UNDEFINED and the target plan fails.
func TestAcquireSeedOnceProceedsWithUnresolvedNaming(t *testing.T) {
	service, _, db, seed, refresh := humanOneShotFixture(t)
	ctx := context.Background()

	snapshot := refresh([]acquisitiondomain.FeedItem{seedFeedItem(seed)})
	_, input := currentInputForSeed(t, service, seed, func(d generated.AnimeDraft) generated.AnimeDraft {
		variant := generated.NamingVariant{
			DirectoryTemplate:     "/library/Show",
			VideoFilenameTemplate: "${anime.title} ${anibt.groupslug}.${file.ext}",
		}
		var override generated.NamingRulesOverride
		if err := override.FromNamingRulesReplace(generated.NamingRulesReplace{
			Mode: generated.NamingRulesReplaceModeReplace,
			Value: generated.NamingRules{
				Language: generated.NamingRulesLanguage(naming.Language),
				Tv:       variant,
				Movie:    variant,
				Unknown:  variant,
			},
		}); err != nil {
			t.Fatal(err)
		}
		d.Rules.Sections.Naming = &override
		return d
	})
	input.Seed = bindSeedIdentity(seed, snapshot)

	result, err := service.AcquireSeedOnce(ctx, input, "one-shot-decoupled-naming-0001")
	if err != nil {
		t.Fatalf("AcquireSeedOnce with a naming render failure: %v", err)
	}
	if result.State != string(OneShotAcquisitionStateRunning) || result.DownloadTaskID == nil {
		t.Fatalf("admission state = %q task = %v, want a running chain with a download task", result.State, result.DownloadTaskID)
	}
	view, err := service.ReadOneShotAcquisition(ctx, result.OneShotID)
	if err != nil {
		t.Fatalf("read one-shot view: %v", err)
	}
	facts := decodeOneShotTargetFactsForTest(t, view.InputSnapshot)
	if facts["admission"] != string(acquisitiontarget.AdmissionFailed) {
		t.Fatalf("frozen target admission = %v (reason %v), want %q", facts["admission"], facts["reason"], acquisitiontarget.AdmissionFailed)
	}
	// The failed render freezes the typed naming reason.
	if code, _ := facts["reason_code"].(string); code != "target_render_failed" {
		t.Fatalf("frozen target reason_code = %q, want target_render_failed", code)
	}
	if reason, _ := facts["reason"].(string); !strings.Contains(reason, "naming_fact_undefined") {
		t.Fatalf("frozen target reason = %q, want the undefined naming fact", reason)
	}
	var setCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM acquisition_targets WHERE release_id = $1`, view.ReleaseID).Scan(&setCount); err != nil {
		t.Fatalf("count target sets: %v", err)
	}
	if setCount != 0 {
		t.Fatal("a failed plan persisted a target set")
	}
}

func decodeOneShotTargetFactsForTest(t *testing.T, snapshot *OneShotInputSnapshot) map[string]any {
	t.Helper()
	if snapshot == nil || len(snapshot.TargetFacts) == 0 {
		t.Fatal("input snapshot carries no target facts")
	}
	var facts map[string]any
	if err := json.Unmarshal(snapshot.TargetFacts, &facts); err != nil {
		t.Fatalf("decode target facts: %v", err)
	}
	return facts
}
