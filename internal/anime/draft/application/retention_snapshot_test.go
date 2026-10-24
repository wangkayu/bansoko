package application

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"bansoko/internal/apperrors"
	"bansoko/internal/filemaintenance"
	"bansoko/internal/parser"
	"bansoko/internal/parserpolicy"
	"bansoko/internal/testutil/animetest"
	"bansoko/internal/testutil/presetdraft"
	"bansoko/internal/transport/httpapi/generated"
)

// instanceAxes is the per-instance axis state Subscribe provisions.
type instanceAxes struct {
	parserKind           string
	parserSource         int64
	unclassifiedRoute    string
	fileEffectSource     int64
	admissionMode        string
	retentionSource      int64
	retentionVersion     int64
	fileEffectVersion    int64
	parserInstanceExists bool
}

func readInstanceAxes(t *testing.T, db *sql.DB, animeID string) instanceAxes {
	t.Helper()
	var axes instanceAxes
	var parserKind sql.NullString
	var parserSource sql.NullInt64
	if err := db.QueryRowContext(context.Background(), `
SELECT parser_kind, parser_source_default_version, unclassified_route, file_effect_source_default_version,
       admission_mode, retention_source_default_version, retention_version, file_effect_version
FROM anime_business_configs WHERE managed_anime_id = $1`, animeID).Scan(
		&parserKind, &parserSource, &axes.unclassifiedRoute, &axes.fileEffectSource,
		&axes.admissionMode, &axes.retentionSource, &axes.retentionVersion, &axes.fileEffectVersion); err != nil {
		t.Fatalf("read instance axes: %v", err)
	}
	axes.parserKind, axes.parserSource, axes.parserInstanceExists = parserKind.String, parserSource.Int64, parserKind.Valid
	return axes
}

// globalAxes reads the current Global parser kind, unclassified route and
// retention admission mode with their versions.
type globalAxes struct {
	parserKind    parser.ParserKind
	parserVersion int64 // the version a draft freezes (1 for an absent row)
	parserSaveCAS int64 // the expected version for SaveDefault (0 for an absent row)
	route         filemaintenance.UnclassifiedRoutePolicyValue
	routeVersion  int64
	retention     filemaintenance.RetentionPolicy
	retentionVer  int64
}

func readGlobalAxes(t *testing.T, db *sql.DB) globalAxes {
	t.Helper()
	ctx := context.Background()
	var out globalAxes
	parserDefault, err := parserpolicy.NewService(db).GetDefault(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if parserDefault == nil {
		out.parserKind, out.parserVersion = parserpolicy.DefaultPolicy().Kind, 1
	} else {
		out.parserKind, out.parserVersion, out.parserSaveCAS = parserDefault.Policy.Kind, parserDefault.Version, parserDefault.Version
	}
	routeService := filemaintenance.NewFileEffectPolicyService(db)
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
	out.route, out.routeVersion = route.UnclassifiedRoute, route.Version
	retention, err := filemaintenance.NewRetentionPolicyService(db).GetDefault(ctx)
	if err != nil || retention == nil {
		t.Fatalf("Global retention policy must exist: %v err=%v", retention, err)
	}
	out.retention, out.retentionVer = retention.Policy, retention.Version
	return out
}

// TestSubscribeAxesComeFromDraftBusinessNotLiveGlobal is the one-time-input
// rule for the per-instance axes: a draft freezes Global's parser kind,
// unclassified route and retention when it is created, and a later Global
// change never reaches the instance that draft subscribes. Only a draft created
// after the change carries the new values.
func TestSubscribeAxesComeFromDraftBusinessNotLiveGlobal(t *testing.T) {
	db := animetest.OpenTestDB(t)
	defer db.Close()
	ctx := context.Background()
	service := draftNewTestService(t, db)

	before := readGlobalAxes(t, db)
	draft := presetdraft.Load(t, db, draftTestDraft(t))

	// Change all three Global axes after the draft froze them.
	nextKind := parser.ParserKind(parser.ParserRegex)
	if before.parserKind == nextKind {
		nextKind = parser.ParserReltitle
	}
	if _, err := parserpolicy.NewService(db).SaveDefault(ctx, before.parserSaveCAS, parserpolicy.Policy{Kind: nextKind}); err != nil {
		t.Fatalf("change Global parser kind: %v", err)
	}
	nextRoute := filemaintenance.UnclassifiedRoutePolicyValueRoute
	if before.route == nextRoute {
		nextRoute = filemaintenance.UnclassifiedRoutePolicyValueSkip
	}
	if _, err := filemaintenance.NewFileEffectPolicyService(db).SaveDefault(ctx, before.routeVersion, nextRoute); err != nil {
		t.Fatalf("change Global unclassified route: %v", err)
	}
	nextRetention := before.retention
	nextRetention.AdmissionMode = filemaintenance.ModeSingleCopy
	if before.retention.AdmissionMode == filemaintenance.ModeSingleCopy {
		nextRetention.AdmissionMode = filemaintenance.ModeAll
	}
	if _, err := filemaintenance.NewRetentionPolicyService(db).SaveDefault(ctx, before.retentionVer, nextRetention, "owner", "web"); err != nil {
		t.Fatalf("change Global retention: %v", err)
	}
	after := readGlobalAxes(t, db)

	subscribed, err := service.SubscribeAnime(ctx, generated.SubscribeAnimeInput{Draft: draft, Start: new(true)}, "axes-from-draft-1")
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	got := readInstanceAxes(t, db, subscribed.ManagedAnime.Id)
	if !got.parserInstanceExists || got.parserKind != string(before.parserKind) || got.parserSource != before.parserVersion {
		t.Fatalf("instance parser = %q@%d, want the draft's %q@%d (Global is now %q)", got.parserKind, got.parserSource, before.parserKind, before.parserVersion, nextKind)
	}
	if got.unclassifiedRoute != string(before.route) || got.fileEffectSource != before.routeVersion || got.fileEffectVersion != 1 {
		t.Fatalf("instance unclassified route = %q@%d v%d, want the draft's %q@%d v1 (Global is now %q)", got.unclassifiedRoute, got.fileEffectSource, got.fileEffectVersion, before.route, before.routeVersion, nextRoute)
	}
	if got.admissionMode != string(before.retention.AdmissionMode) || got.retentionSource != before.retentionVer || got.retentionVersion != 1 {
		t.Fatalf("instance retention = %q@%d v%d, want the draft's %q@%d v1 (Global is now %q)", got.admissionMode, got.retentionSource, got.retentionVersion, before.retention.AdmissionMode, before.retentionVer, nextRetention.AdmissionMode)
	}

	// A draft created after the change copies the new Global values.
	fresh := presetdraft.Load(t, db, draftTestDraft(t))
	second, err := service.SubscribeAnime(ctx, generated.SubscribeAnimeInput{Draft: fresh, Start: new(true)}, "axes-from-draft-2")
	if err != nil {
		t.Fatalf("subscribe fresh draft: %v", err)
	}
	next := readInstanceAxes(t, db, second.ManagedAnime.Id)
	if next.parserKind != string(nextKind) || next.parserSource != after.parserVersion ||
		next.unclassifiedRoute != string(nextRoute) || next.fileEffectSource != after.routeVersion ||
		next.admissionMode != string(nextRetention.AdmissionMode) || next.retentionSource != after.retentionVer {
		t.Fatalf("a draft created after the Global change must carry the new values: %+v, Global %+v", next, after)
	}
	// The first instance is untouched by the second subscribe.
	if again := readInstanceAxes(t, db, subscribed.ManagedAnime.Id); again != got {
		t.Fatalf("existing instance changed: before=%+v after=%+v", got, again)
	}
}

// TestSubscribeWithoutBusinessSnapshotFailsClosed proves direct Subscribe
// never falls back to Global: a draft carrying no business snapshot is
// rejected and writes nothing.
func TestSubscribeWithoutBusinessSnapshotFailsClosed(t *testing.T) {
	db := animetest.OpenTestDB(t)
	defer db.Close()
	service := draftNewTestService(t, db)
	_, err := service.SubscribeAnime(context.Background(), generated.SubscribeAnimeInput{Draft: draftTestDraft(t), Start: new(true)}, "axes-no-business")
	var business *apperrors.Business
	if !errors.As(err, &business) || business.Code != "BUSINESS_SNAPSHOT_INVALID" {
		t.Fatalf("subscribe without a business snapshot = %v, want BUSINESS_SNAPSHOT_INVALID", err)
	}
	var rows int
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM managed_anime`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("a rejected subscribe must write no anime, got %d", rows)
	}
}

// TestCandidateActivationWritesInstanceRetentionPolicy proves candidate
// activation provisions retention from the revision's frozen business.
func TestCandidateActivationWritesInstanceRetentionPolicy(t *testing.T) {
	db := animetest.OpenTestDB(t)
	defer db.Close()
	ctx := context.Background()
	service := draftNewTestService(t, db)
	retention := filemaintenance.NewRetentionPolicyService(db)

	candidate := saveCandidate(t, service, draftTestDraft(t))
	result, err := service.SubscribeAnimeConfigCandidate(ctx, candidate.Id, candidate.Version, "retention-candidate-activate-1", generated.CandidateSubscribeInput{Start: new(true)})
	if err != nil {
		t.Fatal(err)
	}
	row, err := retention.GetInstance(ctx, result.ManagedAnime.Id)
	if err != nil || row == nil {
		t.Fatalf("candidate activation must write an instance policy: row=%v err=%v", row, err)
	}
	singleton, err := retention.GetDefault(ctx)
	if err != nil || singleton == nil {
		t.Fatalf("singleton default must exist: %v err=%v", singleton, err)
	}
	if row.Version != 1 || row.SourceDefaultVersion != singleton.Version || row.Policy.Digest() != singleton.Policy.Digest() {
		t.Fatalf("activated instance must carry the frozen Global retention at version %d: %+v", singleton.Version, row)
	}
}
