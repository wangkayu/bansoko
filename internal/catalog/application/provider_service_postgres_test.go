package application

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strconv"
	"testing"
	"time"

	"bansoko/internal/catalog/adapter/persistence"
	"bansoko/internal/catalog/domain"
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/testutil/disposabledb"
)

type catalogProviderReader struct {
	result domain.ReadResult
	err    error
}

func (r catalogProviderReader) ReadSeason(context.Context, string, domain.ReadHints) (domain.ReadResult, error) {
	return r.result, r.err
}

type countingCatalogProviderReader struct {
	Calls int
}

func (r *countingCatalogProviderReader) ReadSeason(context.Context, string, domain.ReadHints) (domain.ReadResult, error) {
	r.Calls++
	return domain.ReadResult{ContractVersion: mikanSeasonContractVersion}, nil
}

// openCatalogTestDB opens the isolated PostgreSQL schema through the shared
// disposable guard. Each test starts from a clean current migration chain.
func openCatalogTestDB(t *testing.T) *sql.DB {
	t.Helper()
	return disposabledb.ResetDisposableSchema(t)
}

func TestProviderCatalogKeepsMikanIdentityAndLastGoodEntries(t *testing.T) {
	db := openCatalogTestDB(t)
	season := "provider-test-" + time.Now().UTC().Format("150405.000000")
	ctx := context.Background()
	reader := catalogProviderReader{result: domain.ReadResult{
		SourceVersion: "mikan-source-1", ContractVersion: mikanSeasonContractVersion,
		Entries: []domain.Entry{{ProviderEntryRef: "12345", Title: "Mikan title", SourceHint: map[string]any{"bangumi_id": "12345"}}},
	}}
	service := NewService(db, nil)
	service.SetProviderReader(ProviderMikan, reader)
	first, err := service.RefreshProviderSeason(ctx, ProviderMikan, season)
	if err != nil {
		t.Fatal(err)
	}
	if first.Provider != ProviderMikan || len(first.Entries) != 1 || first.Entries[0].ProviderEntryRef != "12345" || first.Entries[0].BGMID != "" {
		t.Fatalf("unexpected Mikan snapshot: %+v", first)
	}
	var storedRef string
	var storedBGM sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT provider_entry_ref, bgm_id FROM provider_catalog_entries WHERE snapshot_id = $1`, first.ID).Scan(&storedRef, &storedBGM); err != nil {
		t.Fatal(err)
	}
	if storedRef != "12345" || storedBGM.Valid {
		t.Fatalf("Mikan identity was stored as BGM: ref=%q bgm=%+v", storedRef, storedBGM)
	}
	workspace, err := service.WorkspaceProvider(ctx, ProviderMikan, season)
	if err != nil || len(workspace.Items) != 1 || workspace.Items[0].Kind != domain.ItemMikanEntry || workspace.Items[0].ProviderEntryRef != "12345" {
		t.Fatalf("unexpected Mikan workspace: %+v err=%v", workspace, err)
	}

	service.SetProviderReader(ProviderMikan, catalogProviderReader{err: errors.New("temporary outage")})
	outage, err := service.RefreshProviderSeason(ctx, ProviderMikan, season)
	if err != nil {
		t.Fatal(err)
	}
	if outage.Status != domain.StatusOutage {
		t.Fatalf("failed refresh did not publish outage health: %+v", outage)
	}
	retained, err := service.WorkspaceProvider(ctx, ProviderMikan, season)
	if err != nil || len(retained.Items) != 1 || retained.Items[0].ProviderEntryRef != "12345" {
		t.Fatalf("outage discarded last-good Mikan entries: %+v err=%v", retained, err)
	}

	service.SetProviderReader(ProviderMikan, catalogProviderReader{result: domain.ReadResult{ContractVersion: mikanSeasonContractVersion}})
	empty, err := service.RefreshProviderSeason(ctx, ProviderMikan, season)
	if err != nil {
		t.Fatal(err)
	}
	if empty.Status != domain.StatusWithdrawn {
		t.Fatalf("valid empty Mikan response was not authoritative: %+v", empty)
	}
}

func TestProviderCatalogRetentionUsesDatabaseTimeAndKeepsLatestGood(t *testing.T) {
	db := openCatalogTestDB(t)
	ctx := context.Background()
	season := "retention-catalog"
	service := NewService(db, nil)
	service.SetProviderReader(ProviderMikan, catalogProviderReader{result: domain.ReadResult{
		ContractVersion: mikanSeasonContractVersion,
		Entries:         []domain.Entry{{ProviderEntryRef: "12345", Title: "Future observed title"}},
	}})
	future := time.Now().UTC().Add(365 * 24 * time.Hour)
	service.now = func() time.Time { return future }
	first, err := service.RefreshProviderSeason(ctx, ProviderMikan, season)
	if err != nil {
		t.Fatalf("future-observed provider refresh: %v", err)
	}
	var deadlineWithinDatabaseWindow, observedInFuture bool
	if err := db.QueryRowContext(ctx, `
	SELECT retention_until <= CURRENT_TIMESTAMP + INTERVAL '30 days'
	       AND retention_until > CURRENT_TIMESTAMP + INTERVAL '29 days',
	       observed_at > CURRENT_TIMESTAMP + INTERVAL '300 days'
	FROM provider_catalog_snapshots WHERE id = $1`, first.ID).Scan(&deadlineWithinDatabaseWindow, &observedInFuture); err != nil {
		t.Fatal(err)
	}
	if !deadlineWithinDatabaseWindow || !observedInFuture {
		t.Fatalf("provider retention must use database time: deadline_in_window=%t observed_in_future=%t", deadlineWithinDatabaseWindow, observedInFuture)
	}

	service.now = func() time.Time { return time.Now().UTC() }
	service.SetProviderReader(ProviderMikan, catalogProviderReader{err: errors.New("temporary provider outage")})
	outage, err := service.RefreshProviderSeason(ctx, ProviderMikan, season)
	if err != nil {
		t.Fatalf("outage refresh: %v", err)
	}
	if outage.Status != domain.StatusOutage {
		t.Fatalf("outage snapshot status = %q, want outage", outage.Status)
	}
	service.SetProviderReader(ProviderMikan, catalogProviderReader{result: domain.ReadResult{
		ContractVersion: mikanSeasonContractVersion,
		Entries:         []domain.Entry{{ProviderEntryRef: "12345", Title: "Latest title"}},
	}})
	latest, err := service.RefreshProviderSeason(ctx, ProviderMikan, season)
	if err != nil {
		t.Fatalf("latest provider refresh: %v", err)
	}
	if latest.Version != 3 {
		t.Fatalf("latest snapshot version = %d, want 3", latest.Version)
	}
	if _, err := db.ExecContext(ctx, `
	INSERT INTO provider_catalog_snapshots(
	    id, provider, season, status, observed_at, contract_version, health_checked_at,
	    version, retention_until
	) VALUES ('running-retention-snapshot', $1, $2, 'running', CURRENT_TIMESTAMP, '', CURRENT_TIMESTAMP, 4,
	          CURRENT_TIMESTAMP - INTERVAL '1 day')`, ProviderMikan, season); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
	UPDATE provider_catalog_snapshots
	SET retention_until = CURRENT_TIMESTAMP - INTERVAL '1 day'
	WHERE provider = $1 AND season = $2`, ProviderMikan, season); err != nil {
		t.Fatal(err)
	}

	repository := persistence.NewRepository(db)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	purged, err := repository.PurgeExpiredProviderCatalogSnapshots(ctx, txscope.Wrap(tx), 500)
	if err != nil {
		tx.Rollback()
		t.Fatalf("purge provider catalog snapshots: %v", err)
	}
	if purged != 2 {
		tx.Rollback()
		t.Fatalf("purged provider snapshots = %d, want old good plus outage", purged)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var oldCount, outageCount, latestCount, runningCount, latestEntryCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM provider_catalog_snapshots WHERE id = $1`, first.ID).Scan(&oldCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM provider_catalog_snapshots WHERE id = $1`, outage.ID).Scan(&outageCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM provider_catalog_snapshots WHERE id = $1`, latest.ID).Scan(&latestCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM provider_catalog_snapshots WHERE id = 'running-retention-snapshot'`).Scan(&runningCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM provider_catalog_entries WHERE snapshot_id = $1`, latest.ID).Scan(&latestEntryCount); err != nil {
		t.Fatal(err)
	}
	if oldCount != 0 || outageCount != 0 {
		t.Fatalf("expired replaceable snapshots survived purge: old=%d outage=%d", oldCount, outageCount)
	}
	if latestCount != 1 || runningCount != 1 || latestEntryCount != 1 {
		t.Fatalf("latest/running/entry retention drifted: latest=%d running=%d entries=%d", latestCount, runningCount, latestEntryCount)
	}
}

func TestProviderRefreshPreferenceRemainsInert(t *testing.T) {
	db := openCatalogTestDB(t)
	ctx := context.Background()
	season := "pref-test-" + time.Now().UTC().Format("150405.000000")
	reader := &countingCatalogProviderReader{}
	service := NewService(db, nil)
	service.SetProviderReader(ProviderMikan, reader)

	created, err := service.SetProviderPreference(ctx, ProviderMikan, domain.Preference{
		Season:         season,
		RefreshEnabled: true,
		Schedule:       "0 */6 * * *",
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !created.RefreshEnabled || created.Schedule != "0 */6 * * *" || created.Version != 1 {
		t.Fatalf("reserved preference was not preserved: %+v", created)
	}

	stored, err := service.ReadProviderPreference(ctx, ProviderMikan, season)
	if err != nil {
		t.Fatal(err)
	}
	if !stored.RefreshEnabled || stored.Schedule != created.Schedule || stored.Version != created.Version {
		t.Fatalf("reserved preference did not round trip: %+v", stored)
	}
	if reader.Calls != 0 {
		t.Fatalf("preference write/read called provider %d times, want 0", reader.Calls)
	}

	workspace, err := service.WorkspaceProvider(ctx, ProviderMikan, season)
	if err != nil {
		t.Fatal(err)
	}
	if workspace.Season != season || reader.Calls != 0 {
		t.Fatalf("workspace GET called provider: workspace=%+v calls=%d", workspace, reader.Calls)
	}

	if _, err := service.RefreshProviderSeason(ctx, ProviderMikan, season); err != nil {
		t.Fatal(err)
	}
	if reader.Calls != 1 {
		t.Fatalf("manual refresh called provider %d times, want exactly 1", reader.Calls)
	}
}

// TestWorkspaceOverlaysSubscriptionLinkage proves the catalog workspace
// surfaces real subscription state: an entry whose provider ref matches a
// subscription's (origin, origin_entry_ref) carries managed/subscription refs
// and open_workbench actions; stopped subscriptions offer start; unmanaged
// entries keep prepare_subscribe.
func TestWorkspaceOverlaysSubscriptionLinkage(t *testing.T) {
	db := openCatalogTestDB(t)
	ctx := context.Background()
	season := "overlay-test-" + time.Now().UTC().Format("150405.000000")
	suffix := time.Now().UTC().Format("150405000000")
	// Provider entry refs must be normalized positive integers; derive unique
	// ones from the clock so repeated runs never cross-match subscriptions.
	base := time.Now().UnixMilli()
	activeRef := strconv.FormatInt(base, 10)
	stoppedRef := strconv.FormatInt(base+1, 10)
	freeRef := strconv.FormatInt(base+2, 10)

	insertManagedSubscription := func(t *testing.T, animeID, subID, ref, state string) {
		t.Helper()
		if _, err := db.ExecContext(ctx, `INSERT INTO managed_anime(id, title) VALUES ($1, $2)`, animeID, "Title "+animeID); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO subscriptions(id, managed_anime_id, origin, origin_entry_ref, state) VALUES ($1, $2, 'mikan', $3, $4)`, subID, animeID, ref, state); err != nil {
			t.Fatal(err)
		}
	}
	insertManagedSubscription(t, "overlay-anime-a-"+suffix, "overlay-sub-a-"+suffix, activeRef, "active")
	insertManagedSubscription(t, "overlay-anime-b-"+suffix, "overlay-sub-b-"+suffix, stoppedRef, "stopped")

	reader := catalogProviderReader{result: domain.ReadResult{
		SourceVersion: "mikan-overlay-1", ContractVersion: mikanSeasonContractVersion,
		Entries: []domain.Entry{
			{ProviderEntryRef: activeRef, Title: "Active show"},
			{ProviderEntryRef: stoppedRef, Title: "Stopped show"},
			{ProviderEntryRef: freeRef, Title: "Free show"},
		},
	}}
	service := NewProviderService(db, map[string]Reader{ProviderMikan: reader}, NewSubscriptionItemReader(db))
	if _, err := service.RefreshProviderSeason(ctx, ProviderMikan, season); err != nil {
		t.Fatal(err)
	}
	workspace, err := service.WorkspaceProvider(ctx, ProviderMikan, season)
	if err != nil {
		t.Fatal(err)
	}
	byRef := map[string]domain.Item{}
	for _, item := range workspace.Items {
		byRef[item.ProviderEntryRef] = item
	}
	active := byRef[activeRef]
	if active.ManagedAnimeRef != "overlay-anime-a-"+suffix || active.SubscriptionRef != "overlay-sub-a-"+suffix || active.SubscriptionState != "active" {
		t.Fatalf("active subscription linkage missing: %+v", active)
	}
	if !reflect.DeepEqual(active.AllowedActions, []string{"open_workbench", "stop"}) {
		t.Fatalf("active actions drifted: %v", active.AllowedActions)
	}
	stopped := byRef[stoppedRef]
	if stopped.SubscriptionState != "stopped" || !reflect.DeepEqual(stopped.AllowedActions, []string{"open_workbench", "start"}) {
		t.Fatalf("stopped linkage drifted: %+v", stopped)
	}
	free := byRef[freeRef]
	if free.ManagedAnimeRef != "" || free.SubscriptionRef != "" || free.SubscriptionState != "" || !reflect.DeepEqual(free.AllowedActions, []string{"prepare_subscribe"}) {
		t.Fatalf("unmanaged entry picked up an overlay: %+v", free)
	}
}

// TestWorkspaceProviderAssociatesDistinctOwnersForOneProviderEntry proves the
// catalog projection never collapses several already-created formal owners
// associated with one external provider entry: the authoritative `instances`
// array carries all associations and the singular compatibility summary stays
// empty instead of silently exposing an arbitrary first owner.
func TestWorkspaceProviderAssociatesDistinctOwnersForOneProviderEntry(t *testing.T) {
	db := openCatalogTestDB(t)
	ctx := context.Background()
	season := "shared-owner-entry-" + time.Now().UTC().Format("150405.000000")
	suffix := time.Now().UTC().Format("150405000000")
	sharedRef := strconv.FormatInt(time.Now().UnixMilli()+9_000_000, 10)

	insertManagedSubscription := func(t *testing.T, animeID, subID, state string) {
		t.Helper()
		if _, err := db.ExecContext(ctx, `INSERT INTO managed_anime(id, title) VALUES ($1, $2)`, animeID, "Title "+animeID); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO subscriptions(id, managed_anime_id, origin, origin_entry_ref, state) VALUES ($1, $2, 'mikan', $3, $4)`, subID, animeID, sharedRef, state); err != nil {
			t.Fatal(err)
		}
	}
	insertManagedSubscription(t, "multi-anime-one-"+suffix, "multi-sub-one-"+suffix, "active")
	insertManagedSubscription(t, "multi-anime-two-"+suffix, "multi-sub-two-"+suffix, "stopped")

	reader := catalogProviderReader{result: domain.ReadResult{
		SourceVersion: "mikan-multi-1", ContractVersion: mikanSeasonContractVersion,
		Entries: []domain.Entry{{ProviderEntryRef: sharedRef, Title: "Shared show"}},
	}}
	service := NewProviderService(db, map[string]Reader{ProviderMikan: reader}, NewSubscriptionItemReader(db))
	if _, err := service.RefreshProviderSeason(ctx, ProviderMikan, season); err != nil {
		t.Fatal(err)
	}
	workspace, err := service.WorkspaceProvider(ctx, ProviderMikan, season)
	if err != nil {
		t.Fatal(err)
	}
	var shared *domain.Item
	for index := range workspace.Items {
		if workspace.Items[index].ProviderEntryRef == sharedRef {
			shared = &workspace.Items[index]
			break
		}
	}
	if shared == nil {
		t.Fatalf("shared provider entry missing from workspace")
	}
	if len(shared.Instances) != 2 {
		t.Fatalf("expected 2 owner associations for one provider entry, got %d (%+v)", len(shared.Instances), shared.Instances)
	}
	// Active is first (ordering contract), and both formal owners are present.
	if shared.Instances[0].SubscriptionState != "active" || shared.Instances[1].SubscriptionState != "stopped" {
		t.Fatalf("owner associations are not ordered active-first: %+v", shared.Instances)
	}
	seen := map[string]string{}
	for _, association := range shared.Instances {
		seen[association.ManagedAnimeRef] = association.SubscriptionRef
		// Every owner association carries its own Subscription CAS version so a
		// client can act on exactly one owner.
		if association.SubscriptionVersion < 1 {
			t.Fatalf("owner association %s lost its subscription version: %+v", association.ManagedAnimeRef, association)
		}
	}
	if seen["multi-anime-one-"+suffix] != "multi-sub-one-"+suffix || seen["multi-anime-two-"+suffix] != "multi-sub-two-"+suffix {
		t.Fatalf("owner association identities are wrong: %+v", shared.Instances)
	}
	if shared.Instances[0].ManagedAnimeRef == shared.Instances[1].ManagedAnimeRef {
		t.Fatalf("one external entry must retain distinct managed owner IDs: %+v", shared.Instances)
	}
	if shared.Instances[0].SubscriptionRef == shared.Instances[1].SubscriptionRef {
		t.Fatalf("one external entry must retain distinct subscription IDs: %+v", shared.Instances)
	}
	// The singular summary must NOT pick an arbitrary owner.
	if shared.ManagedAnimeRef != "" || shared.SubscriptionRef != "" || shared.SubscriptionState != "" {
		t.Fatalf("multi-owner entry leaked an arbitrary singular summary: %+v", shared)
	}
	if !reflect.DeepEqual(shared.AllowedActions, []string{"prepare_subscribe"}) {
		t.Fatalf("multiple-owner entry actions drifted: %v", shared.AllowedActions)
	}
}

// TestWorkspaceOverlaysRetiredOwnerAssociation proves the catalog owner
// association carries the managed lifecycle state/epoch and that a retired
// owner is presented as read-only history: no start/stop/config action, but the
// provider browse action (assembling a brand-new candidate and owner) stays
// available.
func TestWorkspaceOverlaysRetiredOwnerAssociation(t *testing.T) {
	db := openCatalogTestDB(t)
	ctx := context.Background()
	season := "overlay-retired-" + time.Now().UTC().Format("150405.000000")
	suffix := time.Now().UTC().Format("150405000000")
	ref := strconv.FormatInt(time.Now().UnixMilli()+12_000_000, 10)

	animeID := "retired-anime-" + suffix
	subID := "retired-sub-" + suffix
	if _, err := db.ExecContext(ctx, `INSERT INTO managed_anime(id, title) VALUES ($1, $2)`, animeID, "Retired show"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO subscriptions(id, managed_anime_id, origin, origin_entry_ref, state)
VALUES ($1, $2, 'mikan', $3, 'stopped')`, subID, animeID, ref); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
UPDATE managed_anime
SET lifecycle_state='retired', fence_epoch=fence_epoch+1, retired_at=CURRENT_TIMESTAMP,
    retired_subscription_version=1, lifecycle_version=lifecycle_version+1
WHERE id=$1`, animeID); err != nil {
		t.Fatal(err)
	}

	reader := catalogProviderReader{result: domain.ReadResult{
		SourceVersion: "mikan-retired-1", ContractVersion: mikanSeasonContractVersion,
		Entries: []domain.Entry{{ProviderEntryRef: ref, Title: "Retired show"}},
	}}
	service := NewProviderService(db, map[string]Reader{ProviderMikan: reader}, NewSubscriptionItemReader(db))
	if _, err := service.RefreshProviderSeason(ctx, ProviderMikan, season); err != nil {
		t.Fatal(err)
	}
	workspace, err := service.WorkspaceProvider(ctx, ProviderMikan, season)
	if err != nil {
		t.Fatal(err)
	}
	var item *domain.Item
	for index := range workspace.Items {
		if workspace.Items[index].ProviderEntryRef == ref {
			item = &workspace.Items[index]
			break
		}
	}
	if item == nil {
		t.Fatal("retired owner's provider entry missing from workspace")
	}
	if item.ManagedAnimeRef != animeID || item.SubscriptionState != "stopped" {
		t.Fatalf("retired association summary drifted: %+v", item)
	}
	if len(item.Instances) != 1 || item.Instances[0].LifecycleState != "retired" || item.Instances[0].LifecycleEpoch != 2 {
		t.Fatalf("retired association lost its lifecycle: %+v", item.Instances)
	}
	if !reflect.DeepEqual(item.AllowedActions, []string{"prepare_subscribe"}) {
		t.Fatalf("retired owner must expose only the browse action, got %v", item.AllowedActions)
	}
}

// scriptedCatalogReader runs a per-call hook so tests can cancel the caller or
// return an answer that fails persistence, then counts upstream calls.
type scriptedCatalogReader struct {
	Calls int
	hook  func(context.Context, int) (domain.ReadResult, error)
}

func (r *scriptedCatalogReader) ReadSeason(ctx context.Context, _ string, _ domain.ReadHints) (domain.ReadResult, error) {
	r.Calls++
	return r.hook(ctx, r.Calls)
}

func countRunningProviderClaims(t *testing.T, db *sql.DB, provider, season string) int {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM provider_catalog_snapshots WHERE provider = $1 AND season = $2 AND status = 'running'`, provider, season).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func goodMikanResult() domain.ReadResult {
	return domain.ReadResult{ContractVersion: mikanSeasonContractVersion, Entries: []domain.Entry{{ProviderEntryRef: "4242", Title: "Recovered title"}}}
}

// TestProviderRefreshReleasesClaimOnCancelAndError proves that neither a
// caller cancellation during the upstream read nor a persistence error leaves
// a running claim behind: the next refresh reaches the upstream again.
func TestProviderRefreshReleasesClaimOnCancelAndError(t *testing.T) {
	db := openCatalogTestDB(t)
	season := "claim-release-" + time.Now().UTC().Format("150405.000000")
	service := NewService(db, nil)

	cancelCtx, cancel := context.WithCancel(context.Background())
	reader := &scriptedCatalogReader{hook: func(ctx context.Context, call int) (domain.ReadResult, error) {
		switch call {
		case 1:
			cancel()
			return domain.ReadResult{}, ctx.Err()
		case 2:
			// No previous snapshot exists, so a 304 answer cannot be
			// persisted: an injected persistence error.
			return domain.ReadResult{NotModified: true}, nil
		default:
			return goodMikanResult(), nil
		}
	}}
	service.SetProviderReader(ProviderMikan, reader)

	if _, err := service.RefreshProviderSeason(cancelCtx, ProviderMikan, season); err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled refresh error = %v, want context.Canceled", err)
	}
	if running := countRunningProviderClaims(t, db, ProviderMikan, season); running != 0 {
		t.Fatalf("canceled refresh left %d running claims", running)
	}
	var snapshots int
	if err := db.QueryRow(`SELECT COUNT(*) FROM provider_catalog_snapshots WHERE provider = $1 AND season = $2`, ProviderMikan, season).Scan(&snapshots); err != nil {
		t.Fatal(err)
	}
	if snapshots != 0 {
		t.Fatalf("caller cancellation was recorded as %d snapshot rows, want none", snapshots)
	}

	ctx := context.Background()
	if _, err := service.RefreshProviderSeason(ctx, ProviderMikan, season); err == nil {
		t.Fatal("unpersistable 304 answer must fail")
	}
	if running := countRunningProviderClaims(t, db, ProviderMikan, season); running != 0 {
		t.Fatalf("failed persistence left %d running claims", running)
	}

	snapshot, err := service.RefreshProviderSeason(ctx, ProviderMikan, season)
	if err != nil {
		t.Fatal(err)
	}
	if reader.Calls != 3 || snapshot.Status != domain.StatusFresh || len(snapshot.Entries) != 1 {
		t.Fatalf("refresh did not recover: calls=%d snapshot=%+v", reader.Calls, snapshot)
	}
}

// TestProviderRefreshLeaseExpiresAbandonedClaim proves a running claim older
// than the lease is taken over, while a live claim still deduplicates a
// concurrent refresh without calling the upstream.
func TestProviderRefreshLeaseExpiresAbandonedClaim(t *testing.T) {
	db := openCatalogTestDB(t)
	ctx := context.Background()
	season := "claim-lease-" + time.Now().UTC().Format("150405.000000")
	reader := &scriptedCatalogReader{hook: func(context.Context, int) (domain.ReadResult, error) { return goodMikanResult(), nil }}
	service := NewService(db, nil)
	service.SetProviderReader(ProviderMikan, reader)
	insertClaim := func(id string, startedAt time.Time) {
		t.Helper()
		if _, err := db.ExecContext(ctx, `
INSERT INTO provider_catalog_snapshots(id, provider, season, status, observed_at, contract_version, health_checked_at, version)
VALUES ($1, $2, $3, 'running', $4, '', $4, (SELECT COALESCE(MAX(version), 0) + 1 FROM provider_catalog_snapshots WHERE provider = $2 AND season = $3))`,
			id, ProviderMikan, season, startedAt); err != nil {
			t.Fatal(err)
		}
	}

	insertClaim("live-claim", time.Now().UTC())
	deduped, err := service.RefreshProviderSeason(ctx, ProviderMikan, season)
	if err != nil {
		t.Fatal(err)
	}
	if reader.Calls != 0 || deduped.RefreshID != "live-claim" {
		t.Fatalf("live claim did not deduplicate: calls=%d snapshot=%+v", reader.Calls, deduped)
	}

	if _, err := db.ExecContext(ctx, `UPDATE provider_catalog_snapshots SET observed_at = $2 WHERE id = $1`, "live-claim", time.Now().UTC().Add(-providerRefreshLeaseTTL-time.Minute)); err != nil {
		t.Fatal(err)
	}
	snapshot, err := service.RefreshProviderSeason(ctx, ProviderMikan, season)
	if err != nil {
		t.Fatal(err)
	}
	if reader.Calls != 1 || snapshot.Status != domain.StatusFresh || snapshot.RefreshID == "live-claim" {
		t.Fatalf("expired claim was not taken over: calls=%d snapshot=%+v", reader.Calls, snapshot)
	}
	if running := countRunningProviderClaims(t, db, ProviderMikan, season); running != 0 {
		t.Fatalf("takeover left %d running claims", running)
	}
}

// TestProviderSnapshotFreshnessFollowsLastSuccess proves the read surface
// judges freshness by the latest successful observation: an old success is
// stale, a 304 revalidation re-freshens it without new entries, and a later
// outage keeps the previous success time.
func TestProviderSnapshotFreshnessFollowsLastSuccess(t *testing.T) {
	db := openCatalogTestDB(t)
	ctx := context.Background()
	season := "freshness-" + time.Now().UTC().Format("150405.000000")
	service := NewService(db, nil)

	empty, err := service.WorkspaceProvider(ctx, ProviderMikan, season)
	if err != nil {
		t.Fatal(err)
	}
	if !empty.Snapshot.Stale || empty.Snapshot.LastSuccessAt != nil {
		t.Fatalf("never-refreshed season must be stale without a success time: %+v", empty.Snapshot)
	}

	old := time.Now().UTC().Add(-domain.CatalogFreshFor - time.Hour)
	service.now = func() time.Time { return old }
	service.SetProviderReader(ProviderMikan, catalogProviderReader{result: domain.ReadResult{ETag: `"v1"`, ContractVersion: mikanSeasonContractVersion, Entries: []domain.Entry{{ProviderEntryRef: "77", Title: "Old title"}}}})
	if _, err := service.RefreshProviderSeason(ctx, ProviderMikan, season); err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return time.Now().UTC() }
	aged, err := service.WorkspaceProvider(ctx, ProviderMikan, season)
	if err != nil {
		t.Fatal(err)
	}
	if !aged.Snapshot.Stale || aged.Snapshot.LastSuccessAt == nil || aged.Snapshot.LastSuccessAt.Sub(old).Abs() > time.Millisecond {
		t.Fatalf("25-hour-old success must be stale: %+v", aged.Snapshot)
	}

	service.SetProviderReader(ProviderMikan, catalogProviderReader{result: domain.ReadResult{ETag: `"v1"`, NotModified: true}})
	revalidated, err := service.RefreshProviderSeason(ctx, ProviderMikan, season)
	if err != nil {
		t.Fatal(err)
	}
	if revalidated.Stale || revalidated.LastSuccessAt == nil || revalidated.ID != aged.Snapshot.ID {
		t.Fatalf("304 revalidation must re-freshen the same snapshot: %+v", revalidated)
	}
	var entries int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM provider_catalog_entries WHERE snapshot_id = $1`, aged.Snapshot.ID).Scan(&entries); err != nil {
		t.Fatal(err)
	}
	if entries != 1 {
		t.Fatalf("304 revalidation rewrote entries: %d", entries)
	}

	service.SetProviderReader(ProviderMikan, catalogProviderReader{err: errors.New("temporary outage")})
	outage, err := service.RefreshProviderSeason(ctx, ProviderMikan, season)
	if err != nil {
		t.Fatal(err)
	}
	if outage.Status != domain.StatusOutage || outage.Stale || outage.LastSuccessAt == nil || !outage.LastSuccessAt.Equal(*revalidated.LastSuccessAt) {
		t.Fatalf("outage must keep the previous success time: %+v (previous %v)", outage, revalidated.LastSuccessAt)
	}
}
