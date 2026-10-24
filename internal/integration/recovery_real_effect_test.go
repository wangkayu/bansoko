package integration

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	artifactadapter "bansoko/internal/artifact/adapter"
	artifactapplication "bansoko/internal/artifact/application"
	configurationpersistence "bansoko/internal/configuration/adapter/persistence"
	configurationapplication "bansoko/internal/configuration/application"
	"bansoko/internal/deployment"
	downloadapplication "bansoko/internal/download/application"
	"bansoko/internal/download/qbittorrent"
	fileoperationapplication "bansoko/internal/fileoperation/application"
	"bansoko/internal/library/adapter/filesystem"
	observabilityapplication "bansoko/internal/observability/application"
	"bansoko/internal/testutil/acquisitionfixture"
	"bansoko/internal/testutil/disposabledb"
	"bansoko/internal/transport/httpapi/generated"
	"bansoko/internal/workitems"
)

// TestRecoveryRealEffectQBIsReadOnlyForCommittedSubmit is the real-effect
// scenario for the qB deployment-recovery handler: a torrent was committed to
// the controlled qB (tagged with the frozen operation key) while the owner
// never recorded the submit. Deployment recovery only
// classifies durable submission evidence: it must open the gate, never query
// or mutate qB, never re-submit and never settle the owner — the ticketed
// worker reconciliation owns that after the gate opens.
//
// Runs under the real-effect harness (BANSOKO_QB_* set); otherwise it skips — the
// authoritative entry is scripts/verify/real-effect-lane.sh.
func TestRecoveryRealEffectQBIsReadOnlyForCommittedSubmit(t *testing.T) {
	endpoint := strings.TrimSpace(os.Getenv("BANSOKO_QB_ENDPOINT"))
	username := strings.TrimSpace(os.Getenv("BANSOKO_QB_USERNAME"))
	password := strings.TrimSpace(os.Getenv("BANSOKO_QB_PASSWORD"))
	if endpoint == "" || username == "" || password == "" {
		t.Skip("BANSOKO_QB_ENDPOINT, BANSOKO_QB_USERNAME, BANSOKO_QB_PASSWORD are required for the recovery real-effect test")
	}

	db := disposabledb.ResetDisposableSchema(t)
	ctx := context.Background()

	// Real torrent fixture served by the M1 fixture helper.
	artifactName := fmt.Sprintf("Recovery Real Effect - %d.mkv", time.Now().UnixNano())
	artifactBytes := []byte("recovery real effect artifact")
	fixtureClient, fixtureURL, torrentBytes, closeFixture := startSingleEpisodeFixture(t, artifactName, artifactBytes)
	t.Cleanup(closeFixture)
	_ = fixtureURL
	_ = fixtureClient

	// Typed qB connection (same-path harness mapping as the M1 test).
	_, filename, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(filename), "..", ".."))
	hostDownloadRoot := strings.TrimSpace(os.Getenv("BANSOKO_HARNESS_DOWNLOAD_ROOT"))
	if hostDownloadRoot == "" {
		hostDownloadRoot = filepath.Join(root, ".local-artifacts", "real-chain-qb", "downloads")
	}
	if err := os.MkdirAll(hostDownloadRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	secrets, err := configurationapplication.NewDatabaseSecretStore(db, []byte("test-secret-encryption-key-0123456789abcdef"), configurationpersistence.NewRepository(db))
	if err != nil {
		t.Fatal(err)
	}
	configService := configurationapplication.NewHTTPService(configurationapplication.NewService(db, secrets))
	connectionID := "recovery-qb-" + time.Now().UTC().Format("150405")
	downloadRoot := hostDownloadRoot
	if strings.TrimSpace(os.Getenv("BANSOKO_HARNESS_DOWNLOAD_ROOT")) != "" {
		downloadRoot = "/downloads"
	}
	var connectionInput generated.ConnectionInput
	var connectionSecret generated.ConnectionSecret
	if err := connectionSecret.FromConnectionSecretReplace(generated.ConnectionSecretReplace{Mode: "replace", Value: password}); err != nil {
		t.Fatal(err)
	}
	if err := connectionInput.FromQBittorrentConnectionInput(generated.QBittorrentConnectionInput{
		BaseUrl:        endpoint,
		DownloadRoot:   &downloadRoot,
		DownloaderRoot: &hostDownloadRoot,
		Enabled:        true,
		Kind:           generated.QBittorrentConnectionInputKindQbittorrent,
		Name:           connectionID,
		Secret:         connectionSecret,
		Username:       username,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := putGeneratedConnection(t, configService, ctx, connectionID, 0, connectionInput); err != nil {
		t.Fatal(err)
	}
	var connVersion int64
	if err := db.QueryRowContext(ctx, `SELECT version FROM connections WHERE id = $1`, connectionID).Scan(&connVersion); err != nil {
		t.Fatal(err)
	}
	if _, err := putGeneratedConnection(t, configService, ctx, connectionID, connVersion, connectionInput); err != nil {
		t.Fatal(err)
	}

	// Commit the torrent to qB with the frozen operation tag, leaving the
	// owner unrecorded (the rollback window). The key is unique per run so
	// repeated executions never collide into an ambiguous tag.
	operationKey := fmt.Sprintf("qb:recovery-e2e-%d", time.Now().UnixNano())
	submitTaggedTorrent(t, endpoint, username, password, torrentBytes, "bansoko-"+operationKey)

	// Owner chain with a queued task that never recorded the submit.
	seedRecoveryQueuedTask(t, db, operationKey)

	// Recovery chain: same composition as cmd/bansoko-recovery.
	workStore, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	qbClient := qbittorrent.ConfiguredClient{Source: configService, Timeout: 20 * time.Second}
	downloadService, err := downloadapplication.NewService(db, workStore, qbClient, configService)
	if err != nil {
		t.Fatal(err)
	}
	artifactService, err := artifactapplication.NewService(db, artifactadapter.OSArtifactInspector{})
	if err != nil {
		t.Fatal(err)
	}
	fileService, err := fileoperationapplication.NewService(db, filesystem.OSFileExecutor{}, filesystem.OSFileObserver{})
	if err != nil {
		t.Fatal(err)
	}
	observability, err := observabilityapplication.NewService(db)
	if err != nil {
		t.Fatal(err)
	}
	fileService.SetActivityWriter(observability)
	registry, err := deployment.NewRegistry(deployment.DefaultRequiredKeys,
		deployment.Registration{Key: "qb_download_task", Effect: deployment.EffectIdentity{Owner: "download_task", Effect: "qb_unknown_effect"}, Version: 1, Handler: downloadService},
		deployment.Registration{Key: "artifact", Effect: deployment.EffectIdentity{Owner: "artifact", Effect: "inspect_unknown_effect"}, Version: 1, Handler: artifactService},
		deployment.Registration{Key: "file_operation", Effect: deployment.EffectIdentity{Owner: "file_operation", Effect: "filesystem_unknown_effect"}, Version: 1, Handler: fileService},
	)
	if err != nil {
		t.Fatal(err)
	}
	store := deployment.NewPGStore(db)
	plan := deployment.Plan{
		OperationID:          "op-recovery-e2e",
		WindowID:             "win-recovery-e2e",
		OldBundleIdentity:    "old",
		NewBundleIdentity:    "new",
		BackupIdentity:       "backup",
		SchemaIdentity:       "schema",
		BuildIdentity:        "test",
		OldBundleSmokePassed: true,
	}
	result, err := deployment.NewOrchestrator(registry, store).Run(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Gate.Passed {
		t.Fatalf("recovery gate must pass: %+v", result.Gate)
	}
	qbCall, ok := result.Run.Checkpoint.CallFor("qb_download_task")
	if !ok || qbCall.State != deployment.StateCompleted || qbCall.Result.Effect != deployment.EffectNone {
		t.Fatalf("qB handler must classify read-only (completed, no effect): %+v", qbCall)
	}

	// The owner stays exactly as seeded: recovery never settles it.
	var state string
	var correlation sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT state, provider_correlation::text FROM download_tasks WHERE id = 'task-recovery-e2e'`).Scan(&state, &correlation); err != nil {
		t.Fatal(err)
	}
	if state != "queued" || correlation.Valid {
		t.Fatalf("deployment recovery must not settle the owner: state=%q correlation=%v", state, correlation)
	}
	if got := countTaggedTorrents(t, endpoint, username, password, "bansoko-"+operationKey); got != 1 {
		t.Fatalf("expected exactly one tagged torrent after recovery, got %d", got)
	}

	// Replay idempotency: a second run over the same store must settle the
	// same way without re-invoking completed handlers or re-submitting.
	replay, err := deployment.NewOrchestrator(registry, store).Run(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	if !replay.Gate.Passed {
		t.Fatalf("replay gate must pass: %+v", replay.Gate)
	}
	if got := countTaggedTorrents(t, endpoint, username, password, "bansoko-"+operationKey); got != 1 {
		t.Fatalf("replay must not re-submit: tagged torrents=%d", got)
	}
}

// submitTaggedTorrent adds a torrent to qB via its HTTP API with the given
// tag, simulating an effect committed outside the owner.
func submitTaggedTorrent(t *testing.T, endpoint, username, password string, torrentBytes []byte, tag string) {
	t.Helper()
	cookie := qbLogin(t, endpoint, username, password)
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("torrents", "fixture.torrent")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(torrentBytes); err != nil {
		t.Fatal(err)
	}
	_ = writer.WriteField("tags", tag)
	_ = writer.WriteField("savepath", "/downloads")
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, endpoint+"/api/v2/torrents/add", &body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Cookie", cookie)
	req.Header.Set("Referer", endpoint)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("qB add returned %d", resp.StatusCode)
	}
}

func qbLogin(t *testing.T, endpoint, username, password string) string {
	t.Helper()
	form := url.Values{}
	form.Set("username", username)
	form.Set("password", password)
	resp, err := http.PostForm(endpoint+"/api/v2/auth/login", form)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("qB login returned %d", resp.StatusCode)
	}
	return strings.Join(resp.Header.Values("Set-Cookie"), "; ")
}

func countTaggedTorrents(t *testing.T, endpoint, username, password, tag string) int {
	t.Helper()
	cookie := qbLogin(t, endpoint, username, password)
	req, err := http.NewRequest(http.MethodGet, endpoint+"/api/v2/torrents/info?tag="+url.QueryEscape(tag), nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Cookie", cookie)
	req.Header.Set("Referer", endpoint)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var torrents []struct {
		Hash string `json:"hash"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&torrents); err != nil {
		t.Fatalf("decode torrent info: %v", err)
	}
	return len(torrents)
}

// seedRecoveryQueuedTask seeds the owner chain plus a queued download task
// whose submit outcome is unknown (the rollback window).
func seedRecoveryQueuedTask(t *testing.T, db *sql.DB, operationKey string) {
	t.Helper()
	for _, stmt := range []string{
		`INSERT INTO managed_anime(id, title) VALUES ('anime-rec-e2e', 'Recovery E2E')`,
		`INSERT INTO subscriptions(id, managed_anime_id, origin, instance_label, state) VALUES ('sub-rec-e2e', 'anime-rec-e2e', 'custom', 'E2E', 'active')`,
		`INSERT INTO rss_sources(id, subscription_id, source_kind, locator, locator_display, group_name, title) VALUES ('src-rec-e2e', 'sub-rec-e2e', 'custom_rss', 'https://e2e.test/rss', 'https://e2e.test/rss', 'G', 'Recovery E2E')`,
		`INSERT INTO rss_source_runtime(rss_source_id) VALUES ('src-rec-e2e')`,
		`INSERT INTO releases(id, rss_source_id, stable_item_key, raw_facts, source_facts, verdict, reason_code) VALUES ('rel-rec-e2e', 'src-rec-e2e', 'guid:e2e', '{}'::jsonb, '{}'::jsonb, 'accepted_pending_download', 'accepted_pending_download')`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("seed recovery owner: %v", err)
		}
	}
	tx, err := disposabledb.Owner(t).Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	acquisitionfixture.SeedObservedDownload(t, tx, "acq-recovery-e2e", "task-recovery-e2e", "rel-rec-e2e", operationKey, `{}`, `{}`, "queued", `null`)
	if _, err := tx.Exec(`UPDATE releases SET download_task_id = 'task-recovery-e2e', verdict = 'submitted' WHERE id = 'rel-rec-e2e'`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}
