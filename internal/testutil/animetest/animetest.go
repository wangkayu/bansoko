package animetest

import (
	"bansoko/internal/testutil/acquisitionfixture"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"bansoko/internal/apperrors"
	"bansoko/internal/attrrules"
	attrpersistence "bansoko/internal/attrrules/adapter/persistence"
	"bansoko/internal/namingvars"
	varspersistence "bansoko/internal/namingvars/adapter/persistence"
	"bansoko/internal/testutil/disposabledb"
	"github.com/lib/pq"
)

// InstallRegistries installs the shared test attribute rules and naming vars.
// It is safe to call from package TestMain functions and individual tests.
func InstallRegistries() {
	rules := attrrules.Rules{
		Version:        1,
		Resolution:     []string{"2160p", "1440p", "1080p", "720p", "480p"},
		Quality:        []string{"remux", "web-dl", "webrip", "bluray", "bdrip", "hdtv"},
		Language:       []string{"chs", "cht", "sc", "tc", "jpn", "eng"},
		SpecialMarkers: []string{"special", "ncop", "nced"},
		GroupBrackets:  []string{"[", "]"},
	}
	rulesFingerprint, err := attrrules.Fingerprint(rules)
	if err != nil {
		panic(err)
	}
	if err := attrrules.SetActive(rules, rulesFingerprint); err != nil {
		panic(err)
	}

	vars := namingvars.Vars{"test": {"marker": "ok"}}
	varsFingerprint, err := namingvars.SourceFingerprint(`vars.test = {marker: "ok"};`)
	if err != nil {
		panic(err)
	}
	if err := namingvars.SetActive(vars, varsFingerprint); err != nil {
		panic(err)
	}
}

// OpenTestDB installs shared registries and resets the guarded disposable schema.
func OpenTestDB(t *testing.T) *sql.DB {
	t.Helper()
	InstallRegistries()
	db := disposabledb.ResetDisposableSchema(t)
	PublishRegistries(t, db)
	return db
}

// PublishRegistries persists the shared test attribute rules and naming vars
// source installed by InstallRegistries, so a business snapshot that pins their
// fingerprints can load them back by exact version. Packages with their own
// disposable-schema rig call it after InstallRegistries.
func PublishRegistries(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	rules, fp, err := attrrules.Current()
	if err != nil {
		t.Fatal(err)
	}
	if err := attrrules.NewStore(attrpersistence.NewRepository(db)).Publish(ctx, rules, fp); err != nil {
		t.Fatal(err)
	}
	source := `vars.test = {marker: "ok"};`
	vars, err := namingvars.Run(source, namingvars.DefaultInput, namingvars.DefaultTimeout)
	if err != nil {
		t.Fatal(err)
	}
	fp, err = namingvars.SourceFingerprint(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := namingvars.NewStore(varspersistence.NewRepository(db)).PublishSource(ctx, source, vars, fp); err != nil {
		t.Fatal(err)
	}
}

// CountRows returns the row count for a table in the test database.
func CountRows(t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	var count int
	for _, c := range table {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_') {
			t.Fatal("invalid test table identifier")
		}
	}
	if err := db.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM public."+pq.QuoteIdentifier(table)).Scan(&count); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return count
}

// AssertBusinessCode fails unless err is the typed business error status/code.
// It lives here rather than in one owner's test file because tests in several
// owners assert the same typed rejection shape.
func AssertBusinessCode(t *testing.T, err error, status int, code string) {
	t.Helper()
	var business *apperrors.Business
	if !errors.As(err, &business) {
		t.Fatalf("want typed business error %s, got %v", code, err)
	}
	if business.Status != status || business.Code != code {
		t.Fatalf("want %d/%s, got %d/%s", status, code, business.Status, business.Code)
	}
}

// SeedPlacementCoverage inserts one library placement of the anime with the
// given coverage keys (what episode progress counts when the placement is
// active and verified).
func SeedPlacementCoverage(t *testing.T, db *sql.DB, animeID, placementID, verification string, keys ...string) {
	t.Helper()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `
INSERT INTO placements(id, physical_path, managed_anime_id, source_evidence_type, source_owner_ref, source_fingerprint, target_fingerprint, facts_version, verification_state, replacement_eligible)
VALUES ($1, $2, $3, 'reconciliation', $5, 'source', 'target', 'facts-v1', $4, true)`,
		placementID, "/library/"+placementID+".mkv", animeID, verification, "scan:"+placementID); err != nil {
		t.Fatalf("insert placement: %v", err)
	}
	for ordinal, key := range keys {
		if _, err := db.ExecContext(ctx, `
INSERT INTO placement_coverage(placement_id, observation_key, coverage_ordinal, facts_version, evidence_ref)
VALUES ($1, $2, $3, 'facts-v1', $1)`, placementID, key, ordinal); err != nil {
			t.Fatalf("insert placement coverage: %v", err)
		}
	}
}

// seedCompletedFileOp inserts the full release -> task -> artifact ->
// artifact_file -> file_operation chain for one episode under sourceID. When
// opState is "completed" the file_operation counts toward episode_progress;
// otherwise (e.g. "failed") it must be excluded.
func SeedCompletedFileOp(t *testing.T, db *sql.DB, sourceID string, episode int, special bool, opState string) {
	t.Helper()
	ctx := context.Background()
	tag := "ep"
	if special {
		tag = "sp"
	}
	releaseID := fmt.Sprintf("rel-%s-%d", tag, episode)
	taskID := "task-" + releaseID
	artifactID := "art-" + releaseID
	fileID := "file-" + releaseID
	opID := "op-" + releaseID
	parsedFacts := fmt.Sprintf(`{"episode":%d}`, episode)
	if special {
		parsedFacts = fmt.Sprintf(`{"episode":%d,"special":true}`, episode)
	}
	relPath := fmt.Sprintf("show/%s%02d.mkv", tag, episode)
	srcPath := "/downloads/" + relPath
	tgtPath := "/library/" + relPath

	tx, err := disposabledb.Owner(t).BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `
INSERT INTO releases(id, rss_source_id, stable_item_key, raw_facts, source_facts, parsed_facts, verdict, reason_code)
VALUES ($1, $2, $3, '{"title":"x"}', '{}', $4::jsonb, 'accepted_pending_download', 'accepted')`,
		releaseID, sourceID, "guid:"+releaseID, parsedFacts); err != nil {
		t.Fatalf("insert release: %v", err)
	}
	manifest, err := json.Marshal([]any{map[string]any{"file_ref": "primary", "ordinal": 0, "relative_path": relPath, "exact_size": 1, "media_kind": "video", "identity_evidence": map[string]any{}}})
	if err != nil {
		t.Fatal(err)
	}
	acquisitionfixture.SeedObservedDownloadManifest(t, tx, "acq-"+taskID, taskID, releaseID, "idem:"+releaseID, `{}`, `{}`, "completed", `null`, string(manifest))
	if _, err := tx.ExecContext(ctx, `UPDATE releases SET download_task_id = $1, verdict = 'submitted' WHERE id = $2`, taskID, releaseID); err != nil {
		t.Fatalf("link release-task: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO artifacts(id, download_task_id, acquisition_id, inventory_snapshot, fingerprint_snapshot, state, verified_at)
SELECT $1, id, acquisition_id, '{}', '{"fingerprints":[]}', 'verified', CURRENT_TIMESTAMP FROM download_tasks WHERE id=$2`, artifactID, taskID); err != nil {
		t.Fatalf("insert artifact: %v", err)
	}
	fingerprint := `{"algorithm":"metadata","value":"meta:size=1","size_bytes":1}`
	if _, err := tx.ExecContext(ctx, `
INSERT INTO artifact_files(id, artifact_id, acquisition_id, file_ref, relative_path, source_path, media_kind, size_bytes, fingerprint, facts, subtitle_binding_state)
VALUES ($1, $2, (SELECT acquisition_id FROM artifacts WHERE id=$2), 'primary', $3, $4, 'video', 1, $5::jsonb, '{}', 'not_applicable')`,
		fileID, artifactID, relPath, srcPath, fingerprint); err != nil {
		t.Fatalf("insert artifact_file: %v", err)
	}
	acquisitionfixture.CommitOperations(t, tx, artifactID, []acquisitionfixture.PlanOperation{{ID: opID, FileID: fileID, FileRef: "primary", Source: srcPath, Target: tgtPath, Operation: "copy", Key: "idem-op:" + releaseID, Size: 1, Fingerprint: json.RawMessage(fingerprint), Render: json.RawMessage(`{}`), Config: json.RawMessage(`{}`)}})
	if _, err := tx.ExecContext(ctx, `UPDATE file_operations SET state=$2,target_fingerprint=CASE WHEN $2='completed' THEN source_fingerprint ELSE NULL END,observed_at=CASE WHEN $2='completed' THEN CURRENT_TIMESTAMP ELSE NULL END WHERE id=$1`, opID, opState); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `SELECT public.work_item_close(id,'succeeded') FROM work_items WHERE owner_type='file_operation' AND owner_id=$1`, opID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit seed: %v", err)
	}
}

// TestListAnimeExcludesOneShotTemporaryMounts proves the subscribed list hides
// only marker-anchored temporary one-shot mounts: the predicate is marker
// existence alone, so a persistent oneshot-origin subscription without a marker
// stays listed regardless of its origin or stopped state.

// SeedReleaseOwnerChain seeds the managed anime, subscription and RSS source a
// release joins through.
func SeedReleaseOwnerChain(t *testing.T, db *sql.DB, sourceID string) {
	t.Helper()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `
INSERT INTO managed_anime(id, title) VALUES ('anime-trace', 'Trace Show')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO subscriptions(id, managed_anime_id, origin, instance_label, state)
VALUES ('subscription-trace', 'anime-trace', 'custom', 'Trace', 'active')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO rss_sources(id, subscription_id, source_kind, locator, locator_display, group_name, title)
VALUES ($1, 'subscription-trace', 'custom_rss', 'https://feed.example.test/rss', 'https://feed.example.test/rss', 'Group', 'Trace Show')`, sourceID); err != nil {
		t.Fatal(err)
	}
}

// ValidTestTorrentBytes is the minimal well-formed single-file v1 torrent the
// owner tests parse: its title carries an episode number the default parser can
// read. The bytes are shared because several owners need the same fixture; the
// parsing wrapper is not, because it returns the kernel's TorrentPayload.
func ValidTestTorrentBytes() []byte {
	// bencode single-file v1 torrent:
	//   d4:infod4:name19:Show 01 [1080p].mkv6:lengthi12345eee
	// name is "Show 01 [1080p].mkv" (19 bytes), length 12345.
	return []byte("d4:infod4:name19:Show 01 [1080p].mkv6:lengthi12345eee")
}

// OwnerTestDB returns a handle to the guarded disposable schema as the owner
// role, for assertions that must see what a non-superuser session sees.
func OwnerTestDB(t *testing.T) *sql.DB {
	t.Helper()
	return disposabledb.Owner(t)
}

// SetOwnerYear sets the owner-edited year (anime_metadata.year) of an anime,
// the only value ${anime.year} renders. Call it after the metadata row is
// seeded and before the release is accepted, which freezes it into the Vars
// input.
func SetOwnerYear(t *testing.T, animeID string, year int) {
	t.Helper()
	result, err := OwnerTestDB(t).ExecContext(context.Background(), `
UPDATE anime_metadata SET year = $2 WHERE managed_anime_id = $1`, animeID, year)
	if err != nil {
		t.Fatalf("set the owner year of %s: %v", animeID, err)
	}
	if rows, _ := result.RowsAffected(); rows != 1 {
		t.Fatalf("set the owner year of %s: no metadata row", animeID)
	}
}

// StringPtr returns a pointer to value.
func StringPtr(value string) *string { return &value }

// Int64Ptr returns a pointer to value.
func Int64Ptr(value int64) *int64 { return &value }

// SHA256Hex returns the hex-encoded SHA-256 digest of payload.
func SHA256Hex(payload []byte) string {
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

// ProductRowCounts returns the row count of every table the anime owner set
// touches, so a test can assert "no runtime owner rows were created" without
// knowing which table each owner uses.
func ProductRowCounts(t *testing.T, db *sql.DB) map[string]int {
	t.Helper()
	counts := map[string]int{}
	for _, table := range []string{
		"managed_anime", "subscriptions", "anime_metadata", "rss_sources",
		"rss_source_runtime", "releases", "download_tasks", "artifacts",
		"file_operations", "work_items", "work_item_outcomes", "anime_business_configs", "anime_config_overrides",
		"idempotency_records",
	} {
		counts[table] = CountRows(t, db, table)
	}
	return counts
}

// SeedQbConnection inserts the configured qBittorrent connection the owner
// services resolve before they schedule work.
func SeedQbConnection(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `
INSERT INTO secret_values(ref, purpose, nonce, ciphertext)
VALUES ('secret-qb', 'qbittorrent', '012345678912', '0123456789abcdef')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO connections(id, kind, name, endpoint, secret_ref, state, config)
VALUES ('qb-1', 'qbittorrent', 'qb', 'http://qb.example.com:8080', 'secret-qb', 'configured',
    '{"username":"admin","category":"","save_root":"","downloader_root":"","user_tags":[]}'::jsonb)`); err != nil {
		t.Fatal(err)
	}
}

// ReadWorkspaceSourceID returns the single RSS source id of a subscription, the
// anchor the workspace and trace fixtures hang release chains from.
func ReadWorkspaceSourceID(t *testing.T, db *sql.DB, subscriptionID string) string {
	t.Helper()
	var sourceID string
	if err := db.QueryRowContext(context.Background(),
		`SELECT id FROM rss_sources WHERE subscription_id = $1 ORDER BY created_at, id LIMIT 1`, subscriptionID).
		Scan(&sourceID); err != nil {
		t.Fatalf("read subscription source: %v", err)
	}
	return sourceID
}

// SeedPlacementHandoff adds a durable placement obligation to one already
// completed file operation, with an optional terminal failed continuation item.
// It writes no product terminal state through a shortcut: the WorkItem row it
// creates is exactly the shape the real handler leaves behind when it exhausts
// its retry budget, which is what the retry endpoint must accept.
func SeedPlacementHandoff(t *testing.T, runtimeDB *sql.DB, operationID, obligationState string, attempts, maxAttempts int, continuationState string) (continuationID string) {
	t.Helper()
	fixtureDB := disposabledb.Owner(t)
	ctx := context.Background()
	var effectKey string
	if err := runtimeDB.QueryRowContext(ctx,
		`SELECT idempotency_key FROM file_operations WHERE id = $1`, operationID).Scan(&effectKey); err != nil {
		t.Fatal(err)
	}
	// 095 constrains INSERT to the pending start shape, so the seed creates the
	// row as pending and then applies the requested state through the same
	// allowed transition an execution would use.
	if _, err := fixtureDB.ExecContext(ctx, `
INSERT INTO placement_obligations(file_operation_id, state, max_attempts)
VALUES ($1, 'pending', $2)`, operationID, maxAttempts); err != nil {
		t.Fatalf("seed placement obligation: %v", err)
	}
	if obligationState != "pending" {
		reason := "NULL"
		if obligationState == "failed" || obligationState == "waiting_retry" {
			reason = "'placement_admission_failed'"
		}
		settledAt := "NULL"
		if obligationState == "admitted" {
			settledAt = "CURRENT_TIMESTAMP"
		}
		explanation := "NULL"
		if reason != "NULL" {
			explanation = "'placement stage failed'"
		}
		query := fmt.Sprintf(`
UPDATE placement_obligations
SET state = $2, attempts = $3, reason_code = %s, reason = %s, settled_at = %s, updated_at = CURRENT_TIMESTAMP
WHERE file_operation_id = $1`, reason, explanation, settledAt)
		if _, err := fixtureDB.ExecContext(ctx, query, operationID, obligationState, attempts); err != nil {
			t.Fatalf("seed placement obligation state: %v", err)
		}
	}
	if continuationState == "" {
		return ""
	}
	continuationID = "wi-" + operationID + "-placement"
	var planningWork string
	if err := fixtureDB.QueryRowContext(ctx, `SELECT id FROM work_item_history WHERE owner_type='file_operation' AND owner_id=$1 AND kind='file_execute' ORDER BY created_at,id LIMIT 1`, operationID).Scan(&planningWork); err != nil {
		t.Fatal(err)
	}
	// A completed seed has already closed its planning work; bring it back
	// to the live queue so the continuation shape below applies to it.
	if _, err := fixtureDB.ExecContext(ctx, `
WITH closed AS (DELETE FROM work_item_outcomes WHERE id=$1 RETURNING *)
INSERT INTO work_items(id,owner_type,owner_id,kind,state,available_at,fencing_token,idempotency_key,attempt,max_attempts,input_snapshot,expected_owner_version,last_error_code,last_error_reason,retention_days,created_at,updated_at,cancel_requested,managed_anime_id,owner_lifecycle_epoch,external_effect_admitted_at,recovery_blocked,recovery_reason,acquisition_id)
SELECT id,owner_type,owner_id,kind,'available',available_at,fencing_token,idempotency_key,attempt,max_attempts,input_snapshot,expected_owner_version,last_error_code,last_error_reason,retention_days,created_at,updated_at,cancel_requested,managed_anime_id,owner_lifecycle_epoch,external_effect_admitted_at,recovery_blocked,recovery_reason,acquisition_id FROM closed`, planningWork); err != nil {
		t.Fatalf("reopen planning work: %v", err)
	}
	continuationID = planningWork
	snapshot := fmt.Sprintf(
		`{"schema_version":"file-execute-continuation/v1","continuation":"placement","placement_retry":true,"file_operation_id":%q}`,
		operationID)
	// Terminal states live in work_item_outcomes: update the row's other
	// columns while it is still live, then close it.
	terminal := continuationState == "failed" || continuationState == "succeeded"
	liveState := continuationState
	if terminal {
		liveState = "available"
	}
	if _, err := fixtureDB.ExecContext(ctx, `UPDATE work_items SET state=$3,fencing_token=1,idempotency_key=$4,attempt=$5,max_attempts=$6,input_snapshot=$7::jsonb,expected_owner_version=1 WHERE id=$1 AND owner_type='file_operation' AND owner_id=$2`, continuationID, operationID, liveState, effectKey, attempts, maxAttempts, snapshot); err != nil {
		t.Fatalf("seed placement continuation: %v", err)
	}
	if terminal {
		if _, err := fixtureDB.ExecContext(ctx, `SELECT public.work_item_close(id,$2) FROM work_items WHERE id=$1`, continuationID, continuationState); err != nil {
			t.Fatalf("close placement continuation: %v", err)
		}
	}
	return continuationID
}
