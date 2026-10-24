package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	accessapplication "bansoko/internal/accesscontrol/application"
	"bansoko/internal/filemaintenance"
	reconciliationapplication "bansoko/internal/reconciliation/application"
	"bansoko/internal/testutil/disposabledb"
	"bansoko/internal/transport/httpapi/generated"
)

// TestGetFileMaintenanceOverviewAssemblesOwnerTruth proves the overview
// endpoint aggregates the scan owner, consumer outcomes, placements,
// decisions, cleanups, alignments and the complete retention policy from the
// typed owner services — never scanning or mutating anything.
func TestGetFileMaintenanceOverviewAssemblesOwnerTruth(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO managed_anime(id, title) VALUES ('anime-ov', 'Show')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
INSERT INTO subscriptions(id, managed_anime_id, origin, instance_label, state)
VALUES ('sub-ov', 'anime-ov', 'custom', 'Show', 'stopped')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
INSERT INTO anime_business_configs
(managed_anime_id, admission_mode, quality_profile, cleanup_policy, retention_source_default_version, retention_version, updated_at)
VALUES ('anime-ov', 'all', '{"version":"profile-v1","items":[]}', '{"version":"cleanup-v1","effect":"quarantine","grace_seconds":3600}', 1, 1, CURRENT_TIMESTAMP)
ON CONFLICT (managed_anime_id) DO UPDATE SET admission_mode=EXCLUDED.admission_mode, quality_profile=EXCLUDED.quality_profile, cleanup_policy=EXCLUDED.cleanup_policy, retention_source_default_version=EXCLUDED.retention_source_default_version, retention_version=EXCLUDED.retention_version, updated_at=EXCLUDED.updated_at`); err != nil {
		t.Fatal(err)
	}
	// A completed scan generation + its consumer outcomes + one placement +
	// one stale decision + one quarantined cleanup + one completed alignment.
	scope, _ := json.Marshal(map[string]any{"tree_top": "/library"})
	progress, _ := json.Marshal(map[string]any{"last_committed_batch": "done"})
	if _, err := db.Exec(`
INSERT INTO reconciliation_scans
(managed_anime_id, state, generation, scan_scope, progress, capacity_stats, input_fingerprint, version, started_at, completed_at, updated_at,
 alignment_checkpoint_generation, alignment_checkpoint_state, alignment_checkpoint_reason_code, alignment_checkpoint_version, alignment_checkpoint_updated_at,
 replacement_checkpoint_generation, replacement_checkpoint_state, replacement_checkpoint_reason_code, replacement_checkpoint_version, replacement_checkpoint_updated_at)
VALUES ('anime-ov', 'completed', 2, $1, $2, '{}', 'fp', 1, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP,
        2, 'blocked', 'alignment_disabled', 1, CURRENT_TIMESTAMP,
        2, 'blocked', 'subscription_stopped', 1, CURRENT_TIMESTAMP)`, scope, progress); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
INSERT INTO placements
(id, physical_path, managed_anime_id, source_evidence_type, source_owner_ref, source_fingerprint,
 target_fingerprint, quality_facts_ref, facts_version, version, created_at, updated_at)
VALUES ('plc-ov', '/library/Show/EP01.mkv', 'anime-ov', 'reconciliation', 'obs-1', 'fp', 'fp', NULL, 'parser-v1', 1, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
INSERT INTO replacement_decisions
(id, idempotency_key, managed_anime_id, candidate_release_id, policy_version, profile_version,
 candidate_facts_ref, coverage_key, coverage_keys, admission_mode, pools, verdict, state, stale_reason, version, created_at, updated_at)
VALUES ('dec-ov', 'repl:ov:x', 'anime-ov', 'rel-x', 1, 'profile-v1', 'rel-x:1', 'tv:s1:e1', '["tv:s1:e1"]',
 'single_copy', '[]', 'replace', 'stale', 'subscription_stopped', 1, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
INSERT INTO cleanup_operations
(id, idempotency_key, placement_id, managed_anime_id, coverage_keys, replacement_ref, policy_version,
 retention_mode, source_path, source_fingerprint, effect, state, version, created_at, updated_at)
VALUES ('cln-ov', 'cln:plc-ov:dec-ov', 'plc-ov', 'anime-ov', '["tv:s1:e1"]', 'dec-ov', 1,
 'single_copy', '/library/Show/EP01.mkv', 'fp', 'quarantine', 'quarantined', 1, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
INSERT INTO alignment_operations
(id, idempotency_key, managed_anime_id, scan_generation, observation_ref, candidate_ref, policy_version,
 metadata_version, rules_version, naming_version, source_path, source_fingerprint, target_path,
 target_contract, dry_run_result, state, version, created_at, updated_at)
VALUES ('alg-ov', 'align:ov:x', 'anime-ov', 2, 'anime-ov:2:/library/Show/EP01.mkv', 'c', 1, 1, 1, 1,
 '/library/Show/EP01.mkv', 'fp', '/library/Show/Season 01/EP01.mkv', '{}', '{}', 'completed', 1, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}

	// Build the file maintenance service with the typed scan reader port.
	retention := filemaintenance.NewRetentionPolicyService(db)
	placements := filemaintenance.NewPlacementService(db, nil, nil)
	decisions := filemaintenance.NewReplacementDecisionService(db)
	cleanup := filemaintenance.NewCleanupService(db)
	alignment := filemaintenance.NewAlignmentService(db)
	calibration := filemaintenance.NewCalibrationService(db, placements, filemaintenance.NewPolicyService(db), nil, nil)
	service := filemaintenance.NewService(filemaintenance.NewPolicyService(db), retention, nil, placements, decisions)
	service.SetOwnerTruthWires(calibration, cleanup, alignment, currentScanReaderStub{})
	// Wire the same identity gate as production so unknown instances answer
	// a typed 404 instead of an empty 200.
	service.SetAnimeExistence(filemaintenance.NewSQLAnimeExistenceReader(db))

	access := accessapplication.NewServiceWithSessionSecret(db, []byte("test-session-secret-0123456789abcdef"))
	server, err := NewServer(Dependencies{DB: db, Access: access, FileMaintenance: service})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/file-maintenance/overview/anime-ov", nil)
	recorder := httptest.NewRecorder()
	server.GetFileMaintenanceOverview(recorder, req, "anime-ov")
	if recorder.Code != http.StatusOK {
		t.Fatalf("overview status: %d body=%s", recorder.Code, recorder.Body.String())
	}
	var view generated.FileMaintenanceOverviewView
	if err := json.Unmarshal(recorder.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.ManagedAnimeId != "anime-ov" {
		t.Fatalf("managed anime id: %s", view.ManagedAnimeId)
	}
	if view.Scan == nil || view.Scan.Generation != 2 || view.Scan.State != "completed" {
		t.Fatalf("scan truth: %+v", view.Scan)
	}
	if view.Consumers == nil || len(*view.Consumers) != 2 {
		t.Fatalf("consumer checkpoints: %+v", view.Consumers)
	}
	if view.Placements == nil || len(*view.Placements) != 1 {
		t.Fatalf("placements: %+v", view.Placements)
	}
	if view.Decisions == nil || len(*view.Decisions) != 1 {
		t.Fatalf("decisions: %+v", view.Decisions)
	}
	if view.Decisions != nil && ((*view.Decisions)[0].StaleReason == nil || *(*view.Decisions)[0].StaleReason != "subscription_stopped") {
		t.Fatalf("decision stale truth: %+v", view.Decisions)
	}
	if view.Cleanups == nil || len(*view.Cleanups) != 1 {
		t.Fatalf("cleanups: %+v", view.Cleanups)
	}
	if view.Alignments == nil || len(*view.Alignments) != 1 {
		t.Fatalf("alignments: %+v", view.Alignments)
	}
	if view.InstancePolicy == nil || view.InstancePolicy.AdmissionMode != "all" {
		t.Fatalf("instance policy truth: %+v", view.InstancePolicy)
	}
}

// TestGetFileMaintenanceOverviewRejectsUnknownAnime proves the overview
// endpoint answers a structured 404 (never an unknown-500) for identifiers
// that do not exist — including malformed fixture ids that the TEXT id column
// never carried. No stack or driver internals may leak in the body.
func TestGetFileMaintenanceOverviewRejectsUnknownAnime(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO managed_anime(id, title) VALUES ('anime-real', 'Show')`); err != nil {
		t.Fatal(err)
	}
	retention := filemaintenance.NewRetentionPolicyService(db)
	service := filemaintenance.NewService(filemaintenance.NewPolicyService(db), retention, nil, nil, nil)
	service.SetAnimeExistence(filemaintenance.NewSQLAnimeExistenceReader(db))
	access := accessapplication.NewServiceWithSessionSecret(db, []byte("test-session-secret-0123456789abcdef"))
	server, err := NewServer(Dependencies{DB: db, Access: access, FileMaintenance: service})
	if err != nil {
		t.Fatal(err)
	}

	for _, animeID := range []string{"not-a-uuid", "missing-id"} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/file-maintenance/overview/"+animeID, nil)
		recorder := httptest.NewRecorder()
		server.GetFileMaintenanceOverview(recorder, req, generated.ManagedAnimeID(animeID))
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("%s: overview status: %d body=%s", animeID, recorder.Code, recorder.Body.String())
		}
		body := recorder.Body.String()
		var response generated.ErrorResponse
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatalf("%s: structured error expected: %v", animeID, err)
		}
		if response.Code != "NOT_FOUND" {
			t.Fatalf("%s: error code: %s", animeID, response.Code)
		}
		for _, leaked := range []string{"sql:", "Stack", "error"} {
			if strings.Contains(body, leaked) {
				t.Fatalf("%s: internals leaked in body %q", animeID, body)
			}
		}
	}
}

// TestGetFileMaintenanceOverviewWithoutInstancePolicySucceeds proves a real
// instance without its own retention policy row aggregates a 200 with no
// instance policy section instead of failing on the absent row.
func TestGetFileMaintenanceOverviewWithoutInstancePolicySucceeds(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO managed_anime(id, title) VALUES ('anime-nop', 'Show')`); err != nil {
		t.Fatal(err)
	}
	retention := filemaintenance.NewRetentionPolicyService(db)
	placements := filemaintenance.NewPlacementService(db, nil, nil)
	calibration := filemaintenance.NewCalibrationService(db, placements, filemaintenance.NewPolicyService(db), nil, nil)
	service := filemaintenance.NewService(filemaintenance.NewPolicyService(db), retention, nil, placements, nil)
	service.SetOwnerTruthWires(calibration, nil, nil, currentScanReaderStub{})
	service.SetAnimeExistence(filemaintenance.NewSQLAnimeExistenceReader(db))
	access := accessapplication.NewServiceWithSessionSecret(db, []byte("test-session-secret-0123456789abcdef"))
	server, err := NewServer(Dependencies{DB: db, Access: access, FileMaintenance: service})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/file-maintenance/overview/anime-nop", nil)
	recorder := httptest.NewRecorder()
	server.GetFileMaintenanceOverview(recorder, req, "anime-nop")
	if recorder.Code != http.StatusOK {
		t.Fatalf("overview status: %d body=%s", recorder.Code, recorder.Body.String())
	}
	var view generated.FileMaintenanceOverviewView
	if err := json.Unmarshal(recorder.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.ManagedAnimeId != "anime-nop" {
		t.Fatalf("managed anime id: %s", view.ManagedAnimeId)
	}
	if view.InstancePolicy != nil {
		t.Fatalf("instance policy must stay absent: %+v", view.InstancePolicy)
	}
}

// currentScanReaderStub returns a fixed scan view (the real adapter maps the
// reconciliation GetScan not-found to nil).
type currentScanReaderStub struct{}

func (currentScanReaderStub) CurrentScan(ctx context.Context, animeID string) (*reconciliationapplication.ScanView, error) {
	return &reconciliationapplication.ScanView{ManagedAnimeID: animeID, State: reconciliationapplication.ScanCompleted, Generation: 2}, nil
}
