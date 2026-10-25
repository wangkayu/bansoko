package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"bansoko/internal/catalog/domain"
	"bansoko/internal/events"
	"github.com/google/uuid"
)

const (
	ProviderAniBT = "anibt"
	ProviderMikan = "mikan"
)

func validateCatalogProvider(provider string) error {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case ProviderAniBT, ProviderMikan:
		return nil
	default:
		return fmt.Errorf("unsupported catalog provider %q", provider)
	}
}

func (s *Service) providerReader(provider string) (Reader, error) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if err := validateCatalogProvider(provider); err != nil {
		return nil, err
	}
	if s != nil && s.providerReaders != nil {
		if reader := s.providerReaders[provider]; reader != nil {
			return reader, nil
		}
	}
	if provider == ProviderAniBT && s != nil && s.reader != nil {
		return s.reader, nil
	}
	return nil, fmt.Errorf("catalog reader for provider %q is not configured", provider)
}

// SearchProvider is the side-effect-free provider keyword search entry. It
// returns the raw catalog entry shape; no snapshot, WorkItem, or product row
// is created. Providers without a public search surface return
// ErrSearchNotSupported.
func (s *Service) SearchProvider(ctx context.Context, provider, query string) (domain.ReadResult, error) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	query = strings.TrimSpace(query)
	if err := validateCatalogProvider(provider); err != nil {
		return domain.ReadResult{}, err
	}
	if query == "" || utf8.RuneCountInString(query) > 256 {
		// The OpenAPI contract bounds q to 1..256 characters; a violating
		// query is caller input, not a provider capability problem, so it
		// must not reuse ErrSearchNotSupported (issue #9).
		return domain.ReadResult{}, fmt.Errorf("%w: query is required and must be at most 256 characters", ErrSearchQueryInvalid)
	}
	reader, err := s.providerReader(provider)
	if err != nil {
		return domain.ReadResult{}, err
	}
	searcher, ok := reader.(Searcher)
	if !ok {
		return domain.ReadResult{}, fmt.Errorf("%w: %s", ErrSearchNotSupported, provider)
	}
	return searcher.Search(ctx, query, domain.ReadHints{})
}

// RefreshProviderSeason refreshes a provider-scoped catalog snapshot while
// preserving provider-specific entry identity in the shared catalog model.
func (s *Service) RefreshProviderSeason(ctx context.Context, provider, season string) (domain.Snapshot, error) {
	return s.RefreshProviderSeasonWithTrace(ctx, provider, season, "")
}

func (s *Service) RefreshProviderSeasonWithTrace(ctx context.Context, provider, season, traceID string) (domain.Snapshot, error) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	season = strings.TrimSpace(season)
	if s == nil || s.db == nil {
		return domain.Snapshot{}, errors.New("catalog database is nil")
	}
	if err := validateCatalogProvider(provider); err != nil {
		return domain.Snapshot{}, err
	}
	if season == "" || len(season) > 32 {
		return domain.Snapshot{}, errors.New("season is required and must be bounded")
	}
	reader, err := s.providerReader(provider)
	if err != nil {
		return domain.Snapshot{}, err
	}
	claim, err := s.claimProviderRefresh(ctx, provider, season, traceID)
	if err != nil {
		return domain.Snapshot{}, err
	}
	if claim.Active {
		snapshot, readErr := s.readProviderWorkspaceSnapshot(ctx, provider, season)
		if readErr != nil {
			return domain.Snapshot{}, readErr
		}
		snapshot.RefreshID, snapshot.TraceID = claim.ID, claim.TraceID
		return snapshot, nil
	}
	// The claim row is committed on its own, so every exit that does not
	// complete it in place (error, cancellation, panic) must release it;
	// otherwise the running row would block this (provider, season) forever.
	completed := false
	defer func() {
		if !completed {
			s.releaseProviderRefreshClaim(ctx, claim.ID)
		}
	}()
	hints, err := s.providerRefreshHints(ctx, provider, season)
	if err != nil {
		return domain.Snapshot{}, err
	}
	result, readErr := reader.ReadSeason(ctx, season, hints)
	if readErr != nil && ctx.Err() != nil {
		// The caller went away; this is not a provider outage fact.
		return domain.Snapshot{}, fmt.Errorf("provider catalog refresh canceled: %w", ctx.Err())
	}
	// Once the upstream answer is in hand, persist it independently of the
	// caller's cancellation so a dropped HTTP request cannot strand the claim.
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), providerRefreshPersistTimeout)
	defer cancel()
	snapshot, persistErr := s.persistProviderRefresh(persistCtx, provider, season, claim, result, readErr)
	if persistErr != nil {
		return domain.Snapshot{}, persistErr
	}
	completed = true
	if err := s.applyProviderSnapshotFreshness(persistCtx, &snapshot); err != nil {
		return domain.Snapshot{}, err
	}
	if s.eventHub != nil {
		s.eventHub.Publish(events.Event{Target: events.TargetCatalog})
	}
	return snapshot, nil
}

const (
	// providerRefreshLeaseTTL bounds how long a running claim row blocks other
	// refreshes. Readers time out well inside this window, so an older running
	// row belongs to a refresh that was abandoned (crash, lost cleanup).
	providerRefreshLeaseTTL = 2 * time.Minute
	// providerRefreshPersistTimeout bounds the detached persistence and claim
	// release work that runs after the caller may have gone away.
	providerRefreshPersistTimeout = 30 * time.Second
)

// releaseProviderRefreshClaim deletes a running claim row that will not be
// completed. It uses a detached context because it typically runs after the
// caller's context was canceled.
func (s *Service) releaseProviderRefreshClaim(ctx context.Context, claimID string) {
	releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), providerRefreshPersistTimeout)
	defer cancel()
	// Best effort: a failed release is still recovered by the lease TTL.
	_, _ = s.db.ExecContext(releaseCtx, `DELETE FROM provider_catalog_snapshots WHERE id = $1 AND status = 'running'`, claimID)
}

type providerRefreshClaim struct {
	ID      string
	TraceID string
	Active  bool
	Version int64
}

func (s *Service) claimProviderRefresh(ctx context.Context, provider, season, traceID string) (providerRefreshClaim, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return providerRefreshClaim{}, fmt.Errorf("begin provider catalog refresh claim: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "bansoko:provider-catalog:"+provider+":"+season); err != nil {
		return providerRefreshClaim{}, fmt.Errorf("lock provider catalog refresh: %w", err)
	}
	now := s.now()
	var id, existingTrace string
	var startedAt time.Time
	err = tx.QueryRowContext(ctx, `
SELECT id, COALESCE(trace_id, ''), observed_at FROM provider_catalog_snapshots
WHERE provider = $1 AND season = $2 AND status = 'running'
ORDER BY observed_at DESC LIMIT 1`, provider, season).Scan(&id, &existingTrace, &startedAt)
	if err == nil && now.Sub(startedAt) < providerRefreshLeaseTTL {
		if err := tx.Commit(); err != nil {
			return providerRefreshClaim{}, fmt.Errorf("commit existing provider refresh claim: %w", err)
		}
		return providerRefreshClaim{ID: id, TraceID: existingTrace, Active: true}, nil
	}
	if err == nil {
		// The lease expired: the owner abandoned the claim. Drop it so this
		// refresh can take over; a late completion of the old claim fails its
		// status = 'running' guard instead of overwriting the new result.
		if _, err := tx.ExecContext(ctx, `DELETE FROM provider_catalog_snapshots WHERE id = $1 AND status = 'running'`, id); err != nil {
			return providerRefreshClaim{}, fmt.Errorf("drop expired provider refresh claim: %w", err)
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return providerRefreshClaim{}, fmt.Errorf("read provider refresh claim: %w", err)
	}
	id = uuid.NewString()
	traceID = strings.TrimSpace(traceID)
	var version int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) + 1 FROM provider_catalog_snapshots WHERE provider = $1 AND season = $2`, provider, season).Scan(&version); err != nil {
		return providerRefreshClaim{}, fmt.Errorf("allocate provider refresh claim version: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO provider_catalog_snapshots(id, provider, season, status, observed_at, contract_version, health_checked_at, version, trace_id, retention_until)
VALUES ($1, $2, $3, 'running', $4, '', $4, $5, NULLIF($6, ''), CURRENT_TIMESTAMP + INTERVAL '30 days')`, id, provider, season, now, version, traceID); err != nil {
		return providerRefreshClaim{}, fmt.Errorf("create provider refresh claim: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return providerRefreshClaim{}, fmt.Errorf("commit provider refresh claim: %w", err)
	}
	return providerRefreshClaim{ID: id, TraceID: traceID, Version: version}, nil
}

func (s *Service) providerRefreshHints(ctx context.Context, provider, season string) (domain.ReadHints, error) {
	var hints domain.ReadHints
	err := s.db.QueryRowContext(ctx, `
SELECT COALESCE(etag, ''), COALESCE(last_modified, ''), COALESCE(cursor, '')
FROM provider_catalog_snapshots
WHERE provider = $1 AND season = $2 AND status <> 'running'
ORDER BY version DESC LIMIT 1`, provider, season).Scan(&hints.ETag, &hints.LastModified, &hints.Cursor)
	if errors.Is(err, sql.ErrNoRows) {
		return hints, nil
	}
	if err != nil {
		return domain.ReadHints{}, fmt.Errorf("read provider catalog refresh hints: %w", err)
	}
	return hints, nil
}

func (s *Service) persistProviderRefresh(ctx context.Context, provider, season string, claim providerRefreshClaim, result domain.ReadResult, readErr error) (domain.Snapshot, error) {
	if readErr == nil {
		seen := make(map[string]struct{}, len(result.Entries))
		for _, entry := range result.Entries {
			ref := strings.TrimSpace(entry.ProviderEntryRef)
			if ref == "" {
				ref = strings.TrimSpace(entry.BGMID)
			}
			if err := validateProviderCatalogEntryRef(provider, ref); err != nil {
				readErr = err
				break
			}
			if strings.TrimSpace(entry.Title) == "" {
				readErr = fmt.Errorf("provider %s catalog entry %s has no title", provider, ref)
				break
			}
			if _, exists := seen[ref]; exists {
				readErr = fmt.Errorf("provider %s catalog contains duplicate entry %s", provider, ref)
				break
			}
			seen[ref] = struct{}{}
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Snapshot{}, fmt.Errorf("begin provider catalog persistence: %w", err)
	}
	defer tx.Rollback()
	if readErr == nil && result.NotModified {
		snapshot, err := s.readLatestProviderSnapshotTx(ctx, tx, provider, season)
		if err != nil {
			return domain.Snapshot{}, fmt.Errorf("read unchanged provider snapshot: %w", err)
		}
		now := s.now()
		if result.ETag != "" {
			snapshot.ETag = result.ETag
		}
		if result.LastModified != "" {
			snapshot.LastModified = result.LastModified
		}
		if result.Cursor != "" {
			snapshot.Cursor = result.Cursor
		}
		if result.ContractVersion != "" {
			snapshot.ContractVersion = result.ContractVersion
		}
		if _, err := tx.ExecContext(ctx, `
UPDATE provider_catalog_snapshots
SET observed_at = $2, etag = NULLIF($3, ''), last_modified = NULLIF($4, ''), cursor = NULLIF($5, ''),
    contract_version = $6, health_state = 'ready', health_code = NULL, health_message = NULL,
    health_checked_at = $2, error_code = NULL, error_message = NULL,
    retention_until = CURRENT_TIMESTAMP + INTERVAL '30 days'
WHERE id = $1`, snapshot.ID, now, snapshot.ETag, snapshot.LastModified, snapshot.Cursor, snapshot.ContractVersion); err != nil {
			return domain.Snapshot{}, fmt.Errorf("refresh unchanged provider snapshot: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM provider_catalog_snapshots WHERE id = $1 AND status = 'running'`, claim.ID); err != nil {
			return domain.Snapshot{}, fmt.Errorf("abandon unchanged provider refresh claim: %w", err)
		}
		snapshot.ObservedAt, snapshot.HealthCheckedAt = now, now
		snapshot.HealthState, snapshot.HealthCode, snapshot.HealthMessage = domain.HealthReady, "", ""
		snapshot.RefreshID, snapshot.TraceID = claim.ID, claim.TraceID
		if err := tx.Commit(); err != nil {
			return domain.Snapshot{}, fmt.Errorf("commit unchanged provider refresh: %w", err)
		}
		return snapshot, nil
	}
	previous, err := s.previousProviderEntriesTx(ctx, tx, provider, season)
	if err != nil {
		return domain.Snapshot{}, err
	}
	status := domain.StatusFresh
	healthState := domain.HealthReady
	errorCode, errorMessage := "", ""
	entries := result.Entries
	if readErr != nil {
		status, healthState = domain.StatusOutage, domain.HealthFailed
		errorCode, errorMessage = "provider_unavailable", readErr.Error()
		var classified *domain.ReadError
		if errors.As(readErr, &classified) && classified.Code != "" {
			errorCode = classified.Code
		}
		entries = nil
	} else if len(entries) == 0 {
		if len(previous) > 0 {
			status = domain.StatusWithdrawn
		} else {
			status = domain.StatusZeroResult
		}
	}
	now := s.now()
	contractVersion := result.ContractVersion
	if contractVersion == "" {
		if provider == ProviderMikan {
			contractVersion = mikanSeasonContractVersion
		} else {
			contractVersion = anibtSeasonContractVersion
		}
	}
	sourceVersion := result.SourceVersion
	if sourceVersion == "" {
		sourceVersion = season
	}
	snapshot := domain.Snapshot{ID: claim.ID, Provider: provider, Season: season, Status: status, ObservedAt: now, SourceVersion: sourceVersion, Cursor: result.Cursor, ETag: result.ETag, LastModified: result.LastModified, ContractVersion: contractVersion, HealthState: healthState, HealthCode: errorCode, HealthMessage: errorMessage, HealthCheckedAt: now, ErrorCode: errorCode, ErrorMessage: errorMessage, Version: claim.Version, RefreshID: claim.ID, TraceID: claim.TraceID}
	completed, err := tx.ExecContext(ctx, `
UPDATE provider_catalog_snapshots
SET status = $2, observed_at = $3, source_version = $4, cursor = NULLIF($5, ''),
    etag = NULLIF($6, ''), last_modified = NULLIF($7, ''), contract_version = $8,
    health_state = $9, health_code = NULLIF($10, ''), health_message = NULLIF($11, ''),
    health_checked_at = $3, error_code = NULLIF($10, ''), error_message = NULLIF($11, ''),
    trace_id = NULLIF($12, ''), retention_until = CURRENT_TIMESTAMP + INTERVAL '30 days'
WHERE id = $1 AND status = 'running'`,
		snapshot.ID, status, now, sourceVersion, snapshot.Cursor, snapshot.ETag, snapshot.LastModified,
		contractVersion, healthState, errorCode, errorMessage, claim.TraceID)
	if err != nil {
		return domain.Snapshot{}, fmt.Errorf("persist provider catalog snapshot: %w", err)
	}
	if changed, err := completed.RowsAffected(); err != nil || changed != 1 {
		return domain.Snapshot{}, fmt.Errorf("provider refresh claim row is no longer running")
	}
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		ref := strings.TrimSpace(entry.ProviderEntryRef)
		if ref == "" {
			ref = strings.TrimSpace(entry.BGMID)
		}
		if ref == "" || strings.TrimSpace(entry.Title) == "" {
			continue
		}
		if _, exists := seen[ref]; exists {
			return domain.Snapshot{}, fmt.Errorf("provider %s catalog contains duplicate entry %s", provider, ref)
		}
		seen[ref] = struct{}{}
		entry.ProviderEntryRef = ref
		if provider == ProviderAniBT {
			entry.BGMID = ref
		} else {
			entry.BGMID = strings.TrimSpace(entry.BGMID)
		}
		entry.ID, entry.SnapshotID, entry.State, entry.ObservedAt = uuid.NewString(), snapshot.ID, domain.EntryPresent, now
		if err := insertProviderEntryTx(ctx, tx, provider, entry); err != nil {
			return domain.Snapshot{}, err
		}
		snapshot.Entries = append(snapshot.Entries, entry)
		delete(previous, ref)
	}
	if readErr == nil {
		for ref, entry := range previous {
			entry.ID, entry.SnapshotID, entry.State, entry.ObservedAt = uuid.NewString(), snapshot.ID, domain.EntryWithdrawn, now
			entry.ProviderEntryRef = ref
			if err := insertProviderEntryTx(ctx, tx, provider, entry); err != nil {
				return domain.Snapshot{}, err
			}
			snapshot.Entries = append(snapshot.Entries, entry)
		}
	}
	if err := tx.Commit(); err != nil {
		return domain.Snapshot{}, fmt.Errorf("commit provider catalog refresh: %w", err)
	}
	return snapshot, nil
}

func validateProviderCatalogEntryRef(provider, ref string) error {
	ref = strings.TrimSpace(ref)
	parsed, err := strconv.ParseUint(ref, 10, 64)
	if err != nil || parsed == 0 || strconv.FormatUint(parsed, 10) != ref {
		return fmt.Errorf("provider %s catalog entry reference must be a normalized positive integer", provider)
	}
	return nil
}

func insertProviderEntryTx(ctx context.Context, tx *sql.Tx, provider string, entry domain.Entry) error {
	hint, _ := json.Marshal(entry.SourceHint)
	var bgm any
	if strings.TrimSpace(entry.BGMID) != "" {
		bgm = entry.BGMID
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO provider_catalog_entries(id, snapshot_id, provider, provider_entry_ref, bgm_id, title, original_title, source_hint, state, observed_at)
VALUES ($1, $2, $3, $4, $5, $6, NULLIF($7, ''), $8, $9, $10)`, entry.ID, entry.SnapshotID, provider, entry.ProviderEntryRef, bgm, entry.Title, entry.OriginalTitle, hint, entry.State, entry.ObservedAt); err != nil {
		return fmt.Errorf("persist provider catalog entry: %w", err)
	}
	return nil
}

func (s *Service) readLatestProviderSnapshotTx(ctx context.Context, tx *sql.Tx, provider, season string) (domain.Snapshot, error) {
	return readProviderSnapshotRow(ctx, tx.QueryRowContext(ctx, `
SELECT id, provider, season, status, observed_at, source_version, COALESCE(cursor, ''), COALESCE(etag, ''),
       COALESCE(last_modified, ''), contract_version, health_state, COALESCE(health_code, ''),
       COALESCE(health_message, ''), health_checked_at, COALESCE(error_code, ''), COALESCE(error_message, ''),
       version, id, COALESCE(trace_id, '')
FROM provider_catalog_snapshots
WHERE provider = $1 AND season = $2 AND status <> 'running' ORDER BY version DESC LIMIT 1`, provider, season), tx, provider, season)
}

type catalogRowScanner interface {
	Scan(...any) error
}

func readProviderSnapshotRow(ctx context.Context, row catalogRowScanner, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, provider, season string) (domain.Snapshot, error) {
	var snapshot domain.Snapshot
	var status, health string
	// The refresh identity column position reads the row id: in the merged
	// model the refresh run IS the snapshot row.
	if err := row.Scan(&snapshot.ID, &snapshot.Provider, &snapshot.Season, &status, &snapshot.ObservedAt, &snapshot.SourceVersion, &snapshot.Cursor, &snapshot.ETag, &snapshot.LastModified, &snapshot.ContractVersion, &health, &snapshot.HealthCode, &snapshot.HealthMessage, &snapshot.HealthCheckedAt, &snapshot.ErrorCode, &snapshot.ErrorMessage, &snapshot.Version, &snapshot.RefreshID, &snapshot.TraceID); err != nil {
		return domain.Snapshot{}, err
	}
	snapshot.Status, snapshot.HealthState = domain.SnapshotStatus(status), domain.HealthState(health)
	entriesSnapshot := snapshot.ID
	if snapshot.Status == domain.StatusOutage {
		_ = queryer.QueryRowContext(ctx, `SELECT id FROM provider_catalog_snapshots WHERE provider = $1 AND season = $2 AND status NOT IN ('outage', 'running') ORDER BY version DESC LIMIT 1`, provider, season).Scan(&entriesSnapshot)
	}
	rows, err := queryer.QueryContext(ctx, `
SELECT id, snapshot_id, provider_entry_ref, COALESCE(bgm_id, ''), title, COALESCE(original_title, ''), source_hint, state, observed_at
FROM provider_catalog_entries WHERE snapshot_id = $1 ORDER BY title, provider_entry_ref`, entriesSnapshot)
	if err != nil {
		return domain.Snapshot{}, fmt.Errorf("read provider catalog entries: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var entry domain.Entry
		var hint []byte
		if err := rows.Scan(&entry.ID, &entry.SnapshotID, &entry.ProviderEntryRef, &entry.BGMID, &entry.Title, &entry.OriginalTitle, &hint, &entry.State, &entry.ObservedAt); err != nil {
			return domain.Snapshot{}, fmt.Errorf("scan provider catalog entry: %w", err)
		}
		_ = json.Unmarshal(hint, &entry.SourceHint)
		snapshot.Entries = append(snapshot.Entries, entry)
	}
	return snapshot, rows.Err()
}

func (s *Service) previousProviderEntriesTx(ctx context.Context, tx *sql.Tx, provider, season string) (map[string]domain.Entry, error) {
	var snapshotID string
	err := tx.QueryRowContext(ctx, `SELECT id FROM provider_catalog_snapshots WHERE provider = $1 AND season = $2 AND status NOT IN ('outage', 'running') ORDER BY version DESC LIMIT 1`, provider, season).Scan(&snapshotID)
	if errors.Is(err, sql.ErrNoRows) {
		return map[string]domain.Entry{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read previous provider catalog snapshot: %w", err)
	}
	rows, err := tx.QueryContext(ctx, `SELECT provider_entry_ref, COALESCE(bgm_id, ''), title, COALESCE(original_title, ''), source_hint, observed_at FROM provider_catalog_entries WHERE snapshot_id = $1 AND state = 'present'`, snapshotID)
	if err != nil {
		return nil, fmt.Errorf("read previous provider catalog entries: %w", err)
	}
	defer rows.Close()
	previous := make(map[string]domain.Entry)
	for rows.Next() {
		var entry domain.Entry
		var hint []byte
		if err := rows.Scan(&entry.ProviderEntryRef, &entry.BGMID, &entry.Title, &entry.OriginalTitle, &hint, &entry.ObservedAt); err != nil {
			return nil, fmt.Errorf("scan previous provider catalog entry: %w", err)
		}
		_ = json.Unmarshal(hint, &entry.SourceHint)
		previous[entry.ProviderEntryRef] = entry
	}
	return previous, rows.Err()
}

func (s *Service) readProviderWorkspaceSnapshot(ctx context.Context, provider, season string) (domain.Snapshot, error) {
	if err := validateCatalogProvider(provider); err != nil {
		return domain.Snapshot{}, err
	}
	row := s.db.QueryRowContext(ctx, `
SELECT id, provider, season, status, observed_at, source_version, COALESCE(cursor, ''), COALESCE(etag, ''),
       COALESCE(last_modified, ''), contract_version, health_state, COALESCE(health_code, ''),
       COALESCE(health_message, ''), health_checked_at, COALESCE(error_code, ''), COALESCE(error_message, ''),
       version, id, COALESCE(trace_id, '')
FROM provider_catalog_snapshots
WHERE provider = $1 AND season = $2 AND status <> 'running' ORDER BY version DESC LIMIT 1`, provider, season)
	snapshot, err := readProviderSnapshotRow(ctx, row, s.db, provider, season)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Snapshot{Provider: provider, Season: season, Status: domain.StatusStale, HealthState: domain.HealthUnknown, Stale: true}, nil
	}
	if err != nil {
		return domain.Snapshot{}, fmt.Errorf("read provider catalog snapshot: %w", err)
	}
	if err := s.applyProviderSnapshotFreshness(ctx, &snapshot); err != nil {
		return domain.Snapshot{}, err
	}
	return snapshot, nil
}

// applyProviderSnapshotFreshness stamps the time-based freshness of the
// provider season: the latest successful observation (an outage keeps the
// previous success time) and whether it is older than CatalogFreshFor.
func (s *Service) applyProviderSnapshotFreshness(ctx context.Context, snapshot *domain.Snapshot) error {
	var observedAt time.Time
	err := s.db.QueryRowContext(ctx, `
SELECT observed_at FROM provider_catalog_snapshots
WHERE provider = $1 AND season = $2 AND status IN ('fresh', 'withdrawn', 'zero_result')
ORDER BY version DESC LIMIT 1`, snapshot.Provider, snapshot.Season).Scan(&observedAt)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		snapshot.LastSuccessAt = nil
	case err != nil:
		return fmt.Errorf("read provider catalog freshness: %w", err)
	default:
		snapshot.LastSuccessAt = &observedAt
	}
	snapshot.Stale = domain.CatalogSnapshotStale(snapshot.LastSuccessAt, s.now())
	return nil
}

// WorkspaceProvider is the side-effect-free provider catalog projection used
// by both the AniBT and Mikan catalog screens.
func (s *Service) WorkspaceProvider(ctx context.Context, provider, season string) (domain.Workspace, error) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	season = strings.TrimSpace(season)
	snapshot, err := s.readProviderWorkspaceSnapshot(ctx, provider, season)
	if err != nil {
		return domain.Workspace{}, err
	}
	providerRefs := make([]string, 0, len(snapshot.Entries))
	for _, entry := range snapshot.Entries {
		providerRefs = append(providerRefs, entry.ProviderEntryRef)
	}
	suppressed, err := s.suppressedProviderRefs(ctx, provider)
	if err != nil {
		return domain.Workspace{}, err
	}
	overlays := map[string][]domain.InstanceLinkage{}
	var custom []domain.Item
	if s.items != nil {
		overlays, custom, err = s.items.ReadProviderCatalogItems(ctx, provider, providerRefs)
		if err != nil {
			return domain.Workspace{}, err
		}
	}
	items := make([]domain.Item, 0, len(snapshot.Entries)+len(custom))
	for _, entry := range snapshot.Entries {
		observedAt := entry.ObservedAt
		kind, origin := domain.ItemAniBTEntry, "anibt_catalog"
		if provider == ProviderMikan {
			kind, origin = domain.ItemMikanEntry, "mikan_catalog"
		}
		preference := domain.PreferenceNeutral
		if _, ok := suppressed[entry.ProviderEntryRef]; ok {
			preference = domain.PreferenceIgnored
		}
		item := domain.Item{Kind: kind, Origin: origin, Provider: provider, Title: entry.Title, OriginalTitle: entry.OriginalTitle, ProviderEntryRef: entry.ProviderEntryRef, BGMID: entry.BGMID, SourceHint: entry.SourceHint, CatalogState: string(entry.State), ObservedAt: &observedAt, Preference: preference, SourceHealth: catalogSnapshotHealth(snapshot), AllowedActions: []string{"prepare_subscribe"}}
		// The card renders the provider cover first (transport rewrites it to the
		// same-origin cover proxy path), the BGM poster proxy as fallback; with
		// neither, the frontend falls back to a placeholder.
		item.CoverURL = HintString(entry.SourceHint, "cover")
		if entry.BGMID != "" {
			item.PosterURL = "/api/v1/images/poster/" + entry.BGMID
		}
		item.BroadcastDay = BroadcastDayKey(provider, entry.SourceHint)
		item.ReleaseFormat = ReleaseFormatKey(provider, entry.SourceHint)
		ApplyReleaseAvailability(&item)
		applyInstanceOverlay(&item, overlays[entry.ProviderEntryRef])
		items = append(items, item)
	}
	items = append(items, custom...)
	return domain.Workspace{Season: season, Snapshot: snapshot, Items: items}, nil
}

// applyInstanceOverlay attaches every existing formal-owner association for the
// external provider entry. The provider entry is only a browse attribute and
// may associate with several owners, so the array is authoritative. The
// singular ManagedAnimeRef/SubscriptionRef/SubscriptionState fields are a
// compatibility summary written ONLY when exactly one association exists; with
// several associations they stay empty so no consumer can act on an arbitrary
// first row. ManagedAnimeRef is the referenced managed_anime.id and the sole
// formal owner identity.
func applyInstanceOverlay(item *domain.Item, links []domain.InstanceLinkage) {
	if item == nil || len(links) == 0 {
		return
	}
	item.Instances = links
	if len(links) > 1 {
		// Several instances share this provider entry. Action legality collapses
		// to the browse action: every per-instance action must name its own
		// managed_anime_ref.
		item.AllowedActions = []string{"prepare_subscribe"}
		return
	}
	item.ManagedAnimeRef, item.SubscriptionRef = links[0].ManagedAnimeRef, links[0].SubscriptionRef
	item.SubscriptionState = links[0].SubscriptionState
	item.AllowedActions = instanceActions(links[0].SubscriptionState, links[0].LifecycleState)
}

// instanceActions is the per-owner action legality. A retired owner is retained
// for historical inspection but is read-only: start/stop, config and
// unsubscribe all target a live lifecycle, so the only legal action left is
// assembling a brand-new candidate and owner from the provider browse surface.
// It never widens or rewrites Subscription.state, which keeps meaning
// reversible automation intent.
func instanceActions(subscriptionState, lifecycleState string) []string {
	if lifecycleState == "retired" {
		return []string{"prepare_subscribe"}
	}
	return subscriptionActions(subscriptionState)
}

func (s *Service) ReadProviderPreference(ctx context.Context, provider, season string) (domain.Preference, error) {
	provider, season = strings.ToLower(strings.TrimSpace(provider)), strings.TrimSpace(season)
	if s == nil || s.db == nil {
		return domain.Preference{}, errors.New("catalog database is nil")
	}
	if err := validateCatalogProvider(provider); err != nil {
		return domain.Preference{}, err
	}
	if season == "" || len(season) > 32 {
		return domain.Preference{}, errors.New("season is required and must be bounded")
	}
	var result domain.Preference
	err := s.db.QueryRowContext(ctx, `SELECT season, refresh_enabled, COALESCE(schedule, ''), version, updated_at FROM provider_catalog_preferences WHERE provider = $1 AND season = $2`, provider, season).Scan(&result.Season, &result.RefreshEnabled, &result.Schedule, &result.Version, &result.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		// No saved row for this season: show the provider's effective switch
		// (latest saved row of any season, enabled when none exists).
		enabled, err := s.ProviderRefreshEnabled(ctx, provider)
		if err != nil {
			return domain.Preference{}, err
		}
		return domain.Preference{Season: season, RefreshEnabled: enabled}, nil
	}
	if err != nil {
		return domain.Preference{}, fmt.Errorf("read provider catalog preference: %w", err)
	}
	return result, nil
}

// ProviderRefreshEnabled reports whether scheduled catalog refresh is enabled
// for a provider. The switch is the most recently saved preference row of the
// provider across all seasons, so an opt-out survives the season rollover; a
// provider with no saved row is enabled.
func (s *Service) ProviderRefreshEnabled(ctx context.Context, provider string) (bool, error) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if s == nil || s.db == nil {
		return false, errors.New("catalog database is nil")
	}
	if err := validateCatalogProvider(provider); err != nil {
		return false, err
	}
	var enabled bool
	err := s.db.QueryRowContext(ctx, `
SELECT refresh_enabled FROM provider_catalog_preferences
WHERE provider = $1 ORDER BY updated_at DESC, version DESC LIMIT 1`, provider).Scan(&enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("read provider catalog refresh switch: %w", err)
	}
	return enabled, nil
}

func (s *Service) SetProviderPreference(ctx context.Context, provider string, preference domain.Preference, expectedVersion int64) (domain.Preference, error) {
	provider, preference.Season = strings.ToLower(strings.TrimSpace(provider)), strings.TrimSpace(preference.Season)
	preference.Schedule = strings.TrimSpace(preference.Schedule)
	if s == nil || s.db == nil {
		return domain.Preference{}, errors.New("catalog database is nil")
	}
	if err := validateCatalogProvider(provider); err != nil {
		return domain.Preference{}, err
	}
	if preference.Season == "" || len(preference.Season) > 32 || len(preference.Schedule) > 128 {
		return domain.Preference{}, errors.New("catalog preference fields are invalid")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Preference{}, fmt.Errorf("begin provider catalog preference update: %w", err)
	}
	defer tx.Rollback()
	var currentVersion int64
	err = tx.QueryRowContext(ctx, `SELECT version FROM provider_catalog_preferences WHERE provider = $1 AND season = $2 FOR UPDATE`, provider, preference.Season).Scan(&currentVersion)
	if errors.Is(err, sql.ErrNoRows) {
		if expectedVersion != 0 {
			return domain.Preference{}, ErrPreferenceVersionConflict
		}
		preference.Version = 1
		_, err = tx.ExecContext(ctx, `INSERT INTO provider_catalog_preferences(id, provider, season, refresh_enabled, schedule, version, updated_at) VALUES ($1, $2, $3, $4, NULLIF($5, ''), 1, NOW())`, uuid.NewString(), provider, preference.Season, preference.RefreshEnabled, preference.Schedule)
	} else if err != nil {
		return domain.Preference{}, fmt.Errorf("read provider catalog preference for update: %w", err)
	} else {
		if currentVersion != expectedVersion {
			return domain.Preference{}, ErrPreferenceVersionConflict
		}
		preference.Version = currentVersion + 1
		_, err = tx.ExecContext(ctx, `UPDATE provider_catalog_preferences SET refresh_enabled = $3, schedule = NULLIF($4, ''), version = $5, updated_at = NOW() WHERE provider = $1 AND season = $2`, provider, preference.Season, preference.RefreshEnabled, preference.Schedule, preference.Version)
	}
	if err != nil {
		return domain.Preference{}, fmt.Errorf("write provider catalog preference: %w", err)
	}
	if err := tx.QueryRowContext(ctx, `SELECT season, refresh_enabled, COALESCE(schedule, ''), version, updated_at FROM provider_catalog_preferences WHERE provider = $1 AND season = $2`, provider, preference.Season).Scan(&preference.Season, &preference.RefreshEnabled, &preference.Schedule, &preference.Version, &preference.UpdatedAt); err != nil {
		return domain.Preference{}, fmt.Errorf("read updated provider catalog preference: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return domain.Preference{}, fmt.Errorf("commit provider catalog preference: %w", err)
	}
	if s.eventHub != nil {
		s.eventHub.Publish(events.Event{Target: events.TargetCatalog})
	}
	return preference, nil
}

// HintString reads a trimmed string fact from the provider source hint.
func HintString(hint map[string]any, key string) string {
	if hint == nil {
		return ""
	}
	value, ok := hint[key].(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(value)
}

// mikanUpdateTextMaxRunes bounds the raw Mikan update string exposed on the
// API; anything longer is not a date-like display fact and stays absent.
const mikanUpdateTextMaxRunes = 64

// ApplyReleaseAvailability copies the provider's own release-availability
// facts from the stored source hint onto the item. AniBT states hasRelease and
// rssReleaseCount on the season list; Mikan marks entries with no works as
// greyout (recorded at parse time as the boolean "greyout") and shows an update
// string. A missing or mistyped fact stays unknown: nothing is inferred from
// titles or other fields, and snapshots parsed before the greyout flag existed
// read as unknown until the next refresh.
func ApplyReleaseAvailability(item *domain.Item) {
	hint := item.SourceHint
	if hint == nil {
		return
	}
	switch item.Provider {
	case ProviderAniBT:
		if has, ok := hint["hasRelease"].(bool); ok {
			item.ReleaseState = releaseStateFor(has)
		}
		if count, ok := hintNonNegativeInt(hint, "rssReleaseCount"); ok {
			item.ReleaseCount = &count
		}
	case ProviderMikan:
		if greyout, ok := hint["greyout"].(bool); ok {
			item.ReleaseState = releaseStateFor(!greyout)
		}
		if text := HintString(hint, "update_text"); text != "" && utf8.RuneCountInString(text) <= mikanUpdateTextMaxRunes {
			item.UpdateText = text
		}
	}
}

func releaseStateFor(has bool) string {
	if has {
		return domain.ReleaseStateHasReleases
	}
	return domain.ReleaseStateNoReleases
}

func hintNonNegativeInt(hint map[string]any, key string) (int, bool) {
	var value float64
	switch typed := hint[key].(type) {
	case float64:
		value = typed
	case int:
		value = float64(typed)
	case int64:
		value = float64(typed)
	case json.Number:
		parsed, err := typed.Float64()
		if err != nil {
			return 0, false
		}
		value = parsed
	default:
		return 0, false
	}
	if value < 0 || value != math.Trunc(value) || value > math.MaxInt32 {
		return 0, false
	}
	return int(value), true
}

// broadcastDayKeys maps the raw provider schedule fact onto the stable
// BroadcastDay vocabulary; the persisted source hint keeps the raw value and this
// is the only translation. Mikan ships the numeric data-dayofweek
// (0=Sunday … 6=Saturday); its 7 (movie) and 8 (OVA) sections are release
// formats, not days, and read as
// none. AniBT ships the Chinese weekdayLabel. A custom program and any provider
// without schedule data read as none.
var mikanBroadcastDayKeys = map[string]string{
	"0": "sun", "1": "mon", "2": "tue", "3": "wed", "4": "thu", "5": "fri", "6": "sat",
}

var anibtBroadcastDayKeys = map[string]string{
	"周一": "mon", "周二": "tue", "周三": "wed", "周四": "thu", "周五": "fri", "周六": "sat", "周日": "sun",
	"未定": "tbd",
}

// BroadcastDayKey renders the provider's schedule fact as a stable weekday key.
func BroadcastDayKey(provider string, hint map[string]any) string {
	var key string
	var ok bool
	switch provider {
	case ProviderAniBT:
		key, ok = anibtBroadcastDayKeys[strings.TrimSpace(HintString(hint, "weekdayLabel"))]
	case ProviderMikan:
		key, ok = mikanBroadcastDayKeys[strings.TrimSpace(HintString(hint, "weekday"))]
	}
	if ok {
		return key
	}
	return "none"
}

// Release formats a provider catalog states for an entry.
const (
	ReleaseFormatTV    = "tv"
	ReleaseFormatMovie = "movie"
	ReleaseFormatOVA   = "ova"
	ReleaseFormatONA   = "ona"
)

// mikanReleaseFormats maps Mikan's homepage section (data-dayofweek, stored as
// the hint weekday): 0-6 are the weekly TV days, 7 is the movie section and
// 8 is OVA.
var mikanReleaseFormats = map[string]string{
	"0": ReleaseFormatTV, "1": ReleaseFormatTV, "2": ReleaseFormatTV, "3": ReleaseFormatTV,
	"4": ReleaseFormatTV, "5": ReleaseFormatTV, "6": ReleaseFormatTV,
	"7": ReleaseFormatMovie, "8": ReleaseFormatOVA,
}

// anibtReleaseFormats maps the AniBT season-list format field.
var anibtReleaseFormats = map[string]string{
	"TV": ReleaseFormatTV, "MOVIE": ReleaseFormatMovie, "OVA": ReleaseFormatOVA, "ONA": ReleaseFormatONA,
}

// ReleaseFormatKey returns the release format the provider catalog states for
// an entry, or "" when it states none. It is a read-only provider fact: never
// edited, never filled from metadata search, never guessed from a title.
func ReleaseFormatKey(provider string, hint map[string]any) string {
	switch provider {
	case ProviderAniBT:
		return anibtReleaseFormats[strings.ToUpper(strings.TrimSpace(HintString(hint, "format")))]
	case ProviderMikan:
		return mikanReleaseFormats[strings.TrimSpace(HintString(hint, "weekday"))]
	}
	return ""
}
