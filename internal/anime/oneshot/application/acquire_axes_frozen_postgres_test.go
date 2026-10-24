package application

import (
	"context"
	"database/sql"
	"testing"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	"bansoko/internal/filemaintenance"
	"bansoko/internal/parser"
	"bansoko/internal/parserpolicy"
)

type oneShotAxes struct {
	parserKind        string
	parserSource      int64
	unclassifiedRoute string
	fileEffectSource  int64
	admissionMode     string
	retentionSource   int64
}

func readOneShotAxes(t *testing.T, db *sql.DB, animeID string) oneShotAxes {
	t.Helper()
	var axes oneShotAxes
	if err := db.QueryRowContext(context.Background(), `
SELECT parser_kind, parser_source_default_version, unclassified_route, file_effect_source_default_version,
       admission_mode, retention_source_default_version
FROM anime_business_configs WHERE managed_anime_id = $1`, animeID).Scan(
		&axes.parserKind, &axes.parserSource, &axes.unclassifiedRoute, &axes.fileEffectSource,
		&axes.admissionMode, &axes.retentionSource); err != nil {
		t.Fatalf("read one-shot instance axes: %v", err)
	}
	return axes
}

// TestAcquireSeedOnceCurrentInputAxesComeFromDraftNotLiveGlobal is the
// one-time-input rule for a one-shot admitted from a current_input draft: the
// draft froze Global's parser kind, unclassified route and retention when it
// was created, and a Global change before admission never reaches the
// one-shot instance.
func TestAcquireSeedOnceCurrentInputAxesComeFromDraftNotLiveGlobal(t *testing.T) {
	service, _, db, seed, refresh := humanOneShotFixture(t)
	ctx := context.Background()

	parserService := parserpolicy.NewService(db)
	routeService := filemaintenance.NewFileEffectPolicyService(db)
	retentionService := filemaintenance.NewRetentionPolicyService(db)

	parserDefault, err := parserService.GetDefault(ctx)
	if err != nil {
		t.Fatal(err)
	}
	wantKind, wantParserVersion, parserCAS := parserpolicy.DefaultPolicy().Kind, int64(1), int64(0)
	if parserDefault != nil {
		wantKind, wantParserVersion, parserCAS = parserDefault.Policy.Kind, parserDefault.Version, parserDefault.Version
	}
	route, err := routeService.GetDefault(ctx)
	if err == nil && route == nil {
		// The baseline seeds no file_effect_policy row; save one so the test
		// has a Global value to change.
		if _, err = routeService.SaveDefault(ctx, 0, filemaintenance.UnclassifiedRoutePolicyValueSkip); err == nil {
			route, err = routeService.GetDefault(ctx)
		}
	}
	if err != nil || route == nil {
		t.Fatalf("Global file effect policy must exist: %v err=%v", route, err)
	}
	retention, err := retentionService.GetDefault(ctx)
	if err != nil || retention == nil {
		t.Fatalf("Global retention policy must exist: %v err=%v", retention, err)
	}

	// The draft is created (loaded from Global) before Global changes.
	snapshot := refresh([]acquisitiondomain.FeedItem{seedFeedItem(seed)})
	_, input := currentInputForSeed(t, service, seed, nil)
	input.Seed = bindSeedIdentity(seed, snapshot)

	nextKind := parser.ParserKind(parser.ParserRegex)
	if wantKind == nextKind {
		nextKind = parser.ParserReltitle
	}
	if _, err := parserService.SaveDefault(ctx, parserCAS, parserpolicy.Policy{Kind: nextKind}); err != nil {
		t.Fatalf("change Global parser kind: %v", err)
	}
	nextRoute := filemaintenance.UnclassifiedRoutePolicyValueRoute
	if route.UnclassifiedRoute == nextRoute {
		nextRoute = filemaintenance.UnclassifiedRoutePolicyValueSkip
	}
	if _, err := routeService.SaveDefault(ctx, route.Version, nextRoute); err != nil {
		t.Fatalf("change Global unclassified route: %v", err)
	}
	nextRetention := retention.Policy
	nextRetention.AdmissionMode = filemaintenance.ModeSingleCopy
	if retention.Policy.AdmissionMode == filemaintenance.ModeSingleCopy {
		nextRetention.AdmissionMode = filemaintenance.ModeAll
	}
	if _, err := retentionService.SaveDefault(ctx, retention.Version, nextRetention, "owner", "web"); err != nil {
		t.Fatalf("change Global retention: %v", err)
	}

	result, err := service.AcquireSeedOnce(ctx, input, "one-shot-axes-from-draft")
	if err != nil {
		t.Fatalf("AcquireSeedOnce current_input: %v", err)
	}
	got := readOneShotAxes(t, db, result.ManagedAnimeID)
	want := oneShotAxes{
		parserKind: string(wantKind), parserSource: wantParserVersion,
		unclassifiedRoute: string(route.UnclassifiedRoute), fileEffectSource: route.Version,
		admissionMode: string(retention.Policy.AdmissionMode), retentionSource: retention.Version,
	}
	if got != want {
		t.Fatalf("one-shot instance axes = %+v, want the draft's %+v (Global is now %s/%s/%s)", got, want, nextKind, nextRoute, nextRetention.AdmissionMode)
	}
}
