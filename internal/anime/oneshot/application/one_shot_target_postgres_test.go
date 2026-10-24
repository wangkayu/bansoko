package application

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	acquisitiontarget "bansoko/internal/acquisition/target"
	targetpersistence "bansoko/internal/acquisition/target/adapter/persistence"
	animeapplication "bansoko/internal/anime/application"
	animedomain "bansoko/internal/anime/domain"
	draftapplication "bansoko/internal/anime/draft/application"
	"bansoko/internal/apperrors"
	catalogapplication "bansoko/internal/catalog/application"
	"bansoko/internal/parser"
	provider "bansoko/internal/provider/domain"
	"bansoko/internal/testutil/animetest"
	"bansoko/internal/testutil/frozenpolicy"
	"bansoko/internal/workitems"
)

// PostgreSQL-backed coverage for the shared one-shot target admission boundary.
// It needs no qB connection: adoption and collision refusal both terminate
// before the download boundary, which is exactly the property these tests
// assert (zero download tasks, zero WorkItems, zero temporary graph rows).

func oneShotAgentSeedForPayload(t *testing.T, payload animeapplication.TorrentPayload) SelectedSeed {
	t.Helper()
	infoHash := payload.Metadata.InfoHashV1
	if infoHash == "" {
		t.Fatalf("test payload has no v1 info hash: %+v", payload.Metadata)
	}
	digest := animetest.SHA256Hex(payload.Bytes)
	seed := validOneShotSeed()
	// The agent form admits the seed against the provider it names, and a Mikan
	// group is identified by a numeric subgroup id: the AniBT slug the shared
	// seed carries is not a group Mikan can have. The observation the fixture
	// records is keyed by this same identity.
	seed.Provider = animedomain.ProviderKindMikan
	seed.ProviderGroupRef = "678"
	seed.Locator = "https://torrent.example.test/one-shot-adoption.torrent"
	seed.Title = "[Lilith-Raws] Example Anime - 01 [1080p]"
	seed.PublishedAt = time.Now().UTC()
	seed.InfoHash = &infoHash
	seed.ExpectedPayloadDigest = &digest
	return seed
}

// oneShotSeedGroupName is the display name the fixtures record for the provider
// group their seed names. The admission resolves that recorded name, so the
// fixture's own target probe states the same one: a probe that named a different
// group would learn a target the admission never plans.
const oneShotSeedGroupName = "Lilith-Raws"

// recordOneShotSeedGroup records the group observation a provider seed names:
// admission renders anime.group from the display name the newest observation of
// the seed's catalog entry published for that group, so a fixture that seeds a
// provider seed without one can only exercise the fail-closed refusal. The name
// is stated by the fixture, never read off the seed's title: the observed display
// name is what the provider published, and a parsed title is not that fact.
func recordOneShotSeedGroup(t *testing.T, db *sql.DB, seed SelectedSeed, displayName string) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Microsecond)
	active := true
	ref := provider.GroupRef{Provider: provider.Kind(seed.Provider), ExternalID: seed.ProviderGroupRef}
	result := provider.GroupReadResult{
		Provider: provider.Kind(seed.Provider), CatalogEntryRef: seed.ProviderEntryRef,
		ObservedAt: now, SchemaVersion: "mikan-detail-groups-v1", Complete: true,
		CompletenessEvidence: "mikan-detail-group-blocks-v1",
		Groups: []provider.GroupSnapshotEntry{{
			Ref: ref, DisplayName: displayName, ObservedAt: now, Active: &active,
			FeedLocator: provider.FeedLocator{Provider: ref.Provider, CatalogEntryRef: seed.ProviderEntryRef, Group: ref},
		}},
	}
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := catalogapplication.PersistGroupObservationTx(ctx, tx, result); err != nil {
		t.Fatalf("record seed group observation: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// oneShotAdmissionFixture wires the shared target boundary on a disposable
// database and returns the service, the agreed target path, and the command
// input for one deterministic admission.
func oneShotAdmissionFixture(t *testing.T) (*oneShotTestServices, string, int64, AcquireSeedInput) {
	t.Helper()
	db := animetest.OpenTestDB(t)
	payload := parseOneShotTestTorrent(t)
	service := oneShotNewTestService(t, db, &oneShotFakeTorrentFetcher{payload: payload}, nil)
	service.SetTargetAcquisition(acquisitiontarget.FilesystemProbe{}, acquisitiontarget.NewStore(targetpersistence.NewRepository(db)))
	// The rendered target, and therefore the adoption write below, lands under a
	// root this test owns.
	_, pinnedRules := pinOneShotNamingRoot(t, db)

	draft := agentDraftWithLabel(t)
	// The shipped default TV templates name ${anime.year} and
	// ${anime.season_folder}: the year reads the owner-edited value and the
	// season folder names the owner season, which no binding supplies
	// implicitly, so the fixture binds one TMDB tv work and states the owner
	// year and season (as a TMDB match prefill would).
	draft.Metadata = animedomain.AnimeMetadataDraft{
		Bgm:  &animedomain.BGMBinding{WorkId: "bgm-oneshot-adoption"},
		Tmdb: &animedomain.TMDBBinding{WorkId: "tmdb-oneshot-adoption", MediaKind: animedomain.TMDBMediaKindTv, Season: 1},
	}
	setOneShotOwnerYear(&draft)
	season := 1
	draft.Metadata.Season = &season
	business, businessErr := frozenpolicy.SeedBusiness(context.Background(), db, `vars.test = {marker: input.release_title};`)
	if businessErr != nil {
		t.Fatal(businessErr)
	}
	// The draft carries the business snapshot, so its frozen naming section — not
	// the Global row — is what the admission renders with.
	business = rootOneShotBusinessNaming(t, business, pinnedRules)
	draftCarriesBusinessSections(t, &draft, business)
	draft.Business = &business
	businessDigest, businessErr := business.Digest()
	if businessErr != nil {
		t.Fatal(businessErr)
	}
	draft.BusinessDigest = &businessDigest
	seed := oneShotAgentSeedForPayload(t, payload)
	recordOneShotSeedGroup(t, db, seed, oneShotSeedGroupName)
	candidate, err := service.Draft.SaveAnimeConfigCandidate(context.Background(), draftapplication.CandidateDraftInput{Draft: draft})
	if err != nil {
		t.Fatalf("save candidate: %v", err)
	}
	if candidate.CurrentRevision == nil {
		t.Fatal("saved candidate has no current revision")
	}
	input := AcquireSeedInput{
		CandidateID: candidate.Id,
		Revision:    *candidate.CurrentRevision,
		Seed:        seed,
		Caller: AcquireSeedCaller{
			Kind:  AcquireSeedCallerAgent,
			Agent: &AgentSeedAuthorization{GrantedBy: "pg-test", AuthorizationRef: "one-shot-target"},
		},
	}

	// Learn the canonical target from the same shared planner the admission
	// runs, with the same anime evidence: the probe writes the temporary owner
	// rows the admission writes and freezes Vars with them.
	effective, err := animedomain.ResolveDraftEffective(draft)
	if err != nil {
		t.Fatalf("resolve draft effective config: %v", err)
	}
	item := buildOneShotFeedItem(payload, seed)
	if len(item.ReleaseFiles) != 1 {
		t.Fatalf("test payload produced %d release files, want 1", len(item.ReleaseFiles))
	}
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	animeID := probeAnimeEvidenceTx(t, service, tx, draft)
	frozen, err := freezeBusinessVarsTx(context.Background(), tx, business, item, animeID, "", nil)
	tx.Rollback()
	if err != nil {
		t.Fatal(err)
	}
	plan := planOneShotTargets(context.Background(), acquisitiontarget.FilesystemProbe{}, oneShotTargetRequest(
		"release-adoption-probe",
		draft,
		oneShotSeedGroupName,
		0,
		string(parser.ParserRegex),
		effective,
		item,
		animedomain.PreviewReleaseResult{StableItemKey: "probe", Verdict: animedomain.ReleaseVerdict("accepted_pending_download"), FrozenVars: &frozen},
		nil,

		"skip",
	))
	if plan.Admission.Admission != acquisitiontarget.AdmissionDownload {
		t.Fatalf("probe admission = %+v, reason %q", plan.Admission, plan.PlanReason)
	}
	return service, plan.Set.Targets[0].TargetPath, plan.Set.Targets[0].ExpectedSizeBytes, input
}

func writeAdoptionTarget(t *testing.T, targetPath string, size int64) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(targetPath, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertNoOneShotOwnerRows(t *testing.T, service *oneShotTestServices) {
	t.Helper()
	for _, table := range []string{
		"download_tasks", "work_items", "one_shot_acquisitions",
		"releases", "managed_anime", "acquisition_targets",
	} {
		if count := animetest.CountRows(t, service.Service.db, table); count != 0 {
			t.Fatalf("%s rows = %d, want 0 after a fail-closed adoption", table, count)
		}
	}
}

// An already satisfied exact-size target is a successful terminal adoption:
// the temporary owner graph, the frozen TargetSet/observations and the marker
// are created atomically, the Release records adopted_existing, and no
// DownloadTask / qB work / Artifact / FileOperation is fabricated. Retention
// later reclaims the temporary database graph and never the target file.
func TestAcquireSeedOnceAdoptsExactTargetAsTerminalSuccess(t *testing.T) {
	service, targetPath, expectedSize, input := oneShotAdmissionFixture(t)
	writeAdoptionTarget(t, targetPath, expectedSize)
	targetBefore, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatal(err)
	}
	digestBefore := sha256.Sum256(targetBefore)

	result, err := service.AcquireSeedOnce(context.Background(), input, "one-shot-adoption-0001")
	if err != nil {
		t.Fatalf("adopted admission failed: %v", err)
	}
	if result.DownloadTaskID != nil {
		t.Fatalf("adopted admission fabricated download_task_id %q", *result.DownloadTaskID)
	}
	if result.State != string(OneShotAcquisitionStateRetentionPending) {
		t.Fatalf("adopted admission state = %q, want retention_pending", result.State)
	}
	if result.OneShotID == "" || result.ManagedAnimeID == "" || result.SubscriptionID == "" || result.RSSSourceID == "" || result.ReleaseID == "" {
		t.Fatalf("adopted admission returned an incomplete handle: %+v", result)
	}

	// The Release carries the shared adopted verdict, exactly like ordinary RSS.
	var releaseVerdict, releaseReasonCode string
	var releaseDownloadTask sql.NullString
	if err := service.db.QueryRowContext(context.Background(),
		`SELECT verdict, reason_code, download_task_id FROM releases WHERE id = $1`, result.ReleaseID).
		Scan(&releaseVerdict, &releaseReasonCode, &releaseDownloadTask); err != nil {
		t.Fatal(err)
	}
	if releaseVerdict != "adopted_existing" || releaseReasonCode != "all_targets_exact_size" {
		t.Fatalf("adopted release = (%s, %s), want (adopted_existing, all_targets_exact_size)", releaseVerdict, releaseReasonCode)
	}
	if releaseDownloadTask.Valid {
		t.Fatalf("adopted release has download_task_id %q, want NULL", releaseDownloadTask.String)
	}

	// The frozen TargetSet and its observation are durable owner facts.
	var targetRows, observationRows int
	if err := service.db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM acquisition_targets WHERE release_id = $1 AND expected_size_bytes = $2`, result.ReleaseID, expectedSize).
		Scan(&targetRows); err != nil {
		t.Fatal(err)
	}
	if err := service.db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM acquisition_targets t WHERE t.release_id = $1 AND t.observation_state = 'present_matched'`, result.ReleaseID).
		Scan(&observationRows); err != nil {
		t.Fatal(err)
	}
	if targetRows != 1 || observationRows != 1 {
		t.Fatalf("adopted target facts = (%d targets, %d present_matched observations), want (1, 1)", targetRows, observationRows)
	}

	// The marker is created already terminal with the retained success evidence.
	var markerState, markerOutcome, markerReasonCode string
	var markerDownloadTask sql.NullString
	if err := service.db.QueryRowContext(context.Background(),
		`SELECT state, terminal_outcome, terminal_reason_code, download_task_id FROM one_shot_acquisitions WHERE id = $1`, result.OneShotID).
		Scan(&markerState, &markerOutcome, &markerReasonCode, &markerDownloadTask); err != nil {
		t.Fatal(err)
	}
	if markerState != "retention_pending" || markerOutcome != "succeeded" {
		t.Fatalf("adopted marker = (%s, %s), want (retention_pending, succeeded)", markerState, markerOutcome)
	}
	if markerReasonCode == "" {
		t.Fatal("adopted marker carries no terminal reason code")
	}
	if markerDownloadTask.Valid {
		t.Fatalf("adopted marker has download_task_id %q, want NULL", markerDownloadTask.String)
	}

	// Zero download chain: no DownloadTask, no Artifact, no FileOperation, no
	// WorkItem (so no qB submission is possible). The fixture has no
	// qBittorrent connection at all, yet the command succeeds: the adopted path
	// never reads or contacts a downloader, which is the strongest form of qB
	// zero effect.
	var qbConnections int
	if err := service.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM connections WHERE kind = 'qbittorrent'`).Scan(&qbConnections); err != nil {
		t.Fatal(err)
	}
	if qbConnections != 0 {
		t.Fatalf("adoption fixture unexpectedly has %d qB connections", qbConnections)
	}
	for _, table := range []string{"download_tasks", "artifacts", "file_operations", "work_items"} {
		if count := animetest.CountRows(t, service.db, table); count != 0 {
			t.Fatalf("%s rows = %d, want 0 for an adopted acquisition", table, count)
		}
	}

	// Same-key replay returns the identical handle, still without a task.
	replayed, err := service.AcquireSeedOnce(context.Background(), input, "one-shot-adoption-0001")
	if err != nil {
		t.Fatalf("adopted replay failed: %v", err)
	}
	if replayed.OneShotID != result.OneShotID || replayed.ReleaseID != result.ReleaseID || replayed.DownloadTaskID != nil {
		t.Fatalf("adopted replay changed the handle: first=%+v replay=%+v", result, replayed)
	}

	// The read model must present the adopted terminal outcome without assuming
	// a download task exists, and must carry the adopted fact for the UI.
	view, err := service.ReadOneShotAcquisition(context.Background(), result.OneShotID)
	if err != nil {
		t.Fatalf("read adopted one-shot: %v", err)
	}
	if view.DownloadTaskID != nil {
		t.Fatalf("adopted view fabricated download_task_id %q", *view.DownloadTaskID)
	}
	if view.DownloadTask != nil || view.Artifact != nil || view.FileOperation != nil {
		t.Fatalf("adopted view exposed owner rows for a chain that does not exist: %+v", view)
	}
	if view.State != OneShotAcquisitionStateRetentionPending || view.TerminalOutcome == nil || *view.TerminalOutcome != OneShotTerminalOutcomeSucceeded {
		t.Fatalf("adopted view state = (%s, %v), want (retention_pending, succeeded)", view.State, view.TerminalOutcome)
	}
	if view.InputSnapshot == nil || len(view.InputSnapshot.TargetFacts) == 0 {
		t.Fatalf("adopted view lost the frozen target facts: %+v", view.InputSnapshot)
	}
	var targetFacts struct {
		Admission string `json:"admission"`
		Reason    string `json:"reason"`
	}
	if err := json.Unmarshal(view.InputSnapshot.TargetFacts, &targetFacts); err != nil {
		t.Fatalf("decode adopted target facts: %v", err)
	}
	if targetFacts.Admission != "adopted_existing" {
		t.Fatalf("adopted view target facts = %+v, want admission adopted_existing", targetFacts)
	}

	// A different selected payload under the same key stays a typed conflict.
	conflicting := input
	conflicting.Seed.Title = input.Seed.Title + " (other)"
	_, err = service.AcquireSeedOnce(context.Background(), conflicting, "one-shot-adoption-0001")
	var business *apperrors.Business
	if !errors.As(err, &business) || business.Code != "IDEMPOTENCY_KEY_REUSED" || business.Status != 409 {
		t.Fatalf("different payload under the same key = %v, want 409 IDEMPOTENCY_KEY_REUSED", err)
	}

	// Retention reclaims the temporary database graph only: the target bytes
	// and their hash must survive.
	if _, err := service.db.ExecContext(context.Background(),
		`UPDATE one_shot_acquisitions SET retention_until = NOW() - INTERVAL '1 day' WHERE id = $1`, result.OneShotID); err != nil {
		t.Fatal(err)
	}
	retention, err := workitems.NewOneShotRetentionService(service.db, nil)
	if err != nil {
		t.Fatal(err)
	}
	pass, err := retention.RunPass(context.Background())
	if err != nil {
		t.Fatalf("retention pass: %v", err)
	}
	if pass.Reclaimed != 1 {
		t.Fatalf("retention pass reclaimed %d markers, want 1", pass.Reclaimed)
	}
	for _, table := range []string{"managed_anime", "subscriptions", "rss_sources", "releases", "media_fact_snapshots", "media_fact_files", "acquisition_targets"} {
		if count := animetest.CountRows(t, service.db, table); count != 0 {
			t.Fatalf("%s rows = %d after reclamation, want 0", table, count)
		}
	}
	targetAfter, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("reclamation removed the adopted target file: %v", err)
	}
	if sha256.Sum256(targetAfter) != digestBefore {
		t.Fatal("reclamation changed the adopted target file bytes")
	}
}

// A mismatched regular target under the default block policy is refused before
// any qB work is admitted.
func TestAcquireSeedOnceRefusesBlockedCollisionWithoutQBWork(t *testing.T) {
	service, targetPath, expectedSize, input := oneShotAdmissionFixture(t)
	if expectedSize == 1 {
		t.Fatalf("fixture entry size %d cannot express a mismatch", expectedSize)
	}
	writeAdoptionTarget(t, targetPath, 1)

	_, err := service.AcquireSeedOnce(context.Background(), input, "one-shot-collision-0001")
	var business *apperrors.Business
	if !errors.As(err, &business) {
		t.Fatalf("blocked admission error = %v, want a typed apperrors.Business", err)
	}
	if business.Code != "TARGET_COLLISION_BLOCKED" || business.Status != 409 {
		t.Fatalf("blocked admission = %d %s, want 409 TARGET_COLLISION_BLOCKED", business.Status, business.Code)
	}
	assertNoOneShotOwnerRows(t, service)
}

// A missing target passes the shared admission and reaches the download
// boundary, which is where the absent qB connection is reported: the planner
// never blocks a legitimate acquisition.
func TestAcquireSeedOnceWrongRevisionCreatesNoOwners(t *testing.T) {
	service, _, _, input := oneShotAdmissionFixture(t)
	input.Revision++

	_, err := service.AcquireSeedOnce(context.Background(), input, "one-shot-wrong-revision-0001")
	var business *apperrors.Business
	if !errors.As(err, &business) || business.Code != "CANDIDATE_REVISION_MISMATCH" {
		t.Fatalf("wrong revision error = %v, want CANDIDATE_REVISION_MISMATCH", err)
	}
	assertNoOneShotOwnerRows(t, service)
}

func TestAcquireSeedOnceMissingTargetReachesDownloadBoundary(t *testing.T) {
	service, _, _, input := oneShotAdmissionFixture(t)
	_, err := service.AcquireSeedOnce(context.Background(), input, "one-shot-missing-0001")
	var business *apperrors.Business
	if !errors.As(err, &business) {
		t.Fatalf("missing-target admission error = %v, want a typed apperrors.Business", err)
	}
	if business.Code != "QB_CONNECTION_NOT_CONFIGURED" {
		t.Fatalf("missing-target admission = %d %s, want QB_CONNECTION_NOT_CONFIGURED", business.Status, business.Code)
	}
	// The admission transaction rolled back, so nothing durable remains.
	assertNoOneShotOwnerRows(t, service)
}
