package migrations

import "testing"

// A provider catalog refresh run is a snapshot row with status 'running'; the
// partial unique index keeps one running row per (provider, season).
func TestProviderCatalogSnapshotRunningConstraints(t *testing.T) {
	p := newConstraintProbe(t)
	p.accept("running catalog snapshot", `
		INSERT INTO provider_catalog_snapshots(id, provider, season, status, observed_at, contract_version, health_checked_at, version, trace_id)
		VALUES ('cat-run-1', 'mikan', '2026-summer', 'running', CURRENT_TIMESTAMP, '', CURRENT_TIMESTAMP, 1, 'trace-1')`)
	p.reject("second running snapshot for the same provider and season",
		`INSERT INTO provider_catalog_snapshots(id, provider, season, status, observed_at, contract_version, health_checked_at, version)
		 VALUES ('cat-run-2', 'mikan', '2026-summer', 'running', CURRENT_TIMESTAMP, '', CURRENT_TIMESTAMP, 2)`)
	p.accept("running snapshot for another season", `
		INSERT INTO provider_catalog_snapshots(id, provider, season, status, observed_at, contract_version, health_checked_at, version)
		VALUES ('cat-run-3', 'mikan', '2026-autumn', 'running', CURRENT_TIMESTAMP, '', CURRENT_TIMESTAMP, 1)`)
	p.accept("terminal snapshot beside a running row", `
		INSERT INTO provider_catalog_snapshots(id, provider, season, status, observed_at, contract_version, health_checked_at, version)
		VALUES ('cat-final-1', 'mikan', '2026-summer', 'fresh', CURRENT_TIMESTAMP, 'contract-1', CURRENT_TIMESTAMP, 2)`)
	p.reject("unknown catalog snapshot status",
		`INSERT INTO provider_catalog_snapshots(id, provider, season, status, observed_at, contract_version, health_checked_at, version)
		 VALUES ('cat-bad-status', 'mikan', '2026-winter', 'queued', CURRENT_TIMESTAMP, '', CURRENT_TIMESTAMP, 1)`)
}
