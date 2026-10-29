package integration

import (
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	acquisitiontorrent "bansoko/internal/acquisition/torrent"
	animedomain "bansoko/internal/anime/domain"
	draftapplication "bansoko/internal/anime/draft/application"
	oneshotapplication "bansoko/internal/anime/oneshot/application"
	animetransport "bansoko/internal/anime/transport"
	"bansoko/internal/batchplan"
	"bansoko/internal/transport/httpapi/generated"
)

const fileSelectionTorrentName = "OSPick Real Show"

// nestedFileTorrent is a multi-file torrent whose file names may hold folders
// ("Season 1/x.mkv"), web-seeded from webSeedRoot/<name>/<path>.
func nestedFileTorrent(name string, files []torrentFile, webSeedRoot string) []byte {
	var stream []byte
	for _, file := range files {
		stream = append(stream, file.Content...)
	}
	const pieceLength = 16384
	var pieces []byte
	for off := 0; off < len(stream); off += pieceLength {
		end := min(off+pieceLength, len(stream))
		hash := sha1.Sum(stream[off:end])
		pieces = append(pieces, hash[:]...)
	}
	var b bytes.Buffer
	b.WriteString("d4:info")
	b.WriteString("d4:name" + strconv.Itoa(len(name)) + ":" + name)
	b.WriteString("12:piece lengthi" + strconv.Itoa(pieceLength) + "e")
	b.WriteString("6:pieces" + strconv.Itoa(len(pieces)) + ":")
	b.Write(pieces)
	b.WriteString("5:filesl")
	for _, file := range files {
		b.WriteString("d6:lengthi" + strconv.Itoa(len(file.Content)) + "e4:pathl")
		for _, segment := range strings.Split(file.Name, "/") {
			b.WriteString(strconv.Itoa(len(segment)) + ":" + segment)
		}
		b.WriteString("ee")
	}
	b.WriteString("ee")
	b.WriteString("8:url-list" + strconv.Itoa(len(webSeedRoot)) + ":" + webSeedRoot)
	b.WriteString("e")
	return b.Bytes()
}

// startNestedFileFixture serves the nested torrent named name and its
// web-seed files.
func startNestedFileFixture(t *testing.T, name string, files []torrentFile) (*http.Client, fixtureEndpoint, []byte, func()) {
	t.Helper()
	listener, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	torrent := nestedFileTorrent(name, files, fmt.Sprintf("http://%s:%d", realChainFixtureHost(), port))
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/artifact.torrent" {
			w.Header().Set("Content-Type", "application/x-bittorrent")
			_, _ = w.Write(torrent)
			return
		}
		for _, file := range files {
			if r.URL.Path == "/"+name+"/"+file.Name {
				http.ServeContent(w, r, path.Base(file.Name), time.Unix(0, 0), bytes.NewReader(file.Content))
				return
			}
		}
		http.NotFound(w, r)
	})}
	go func() { _ = server.Serve(listener) }()
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, portValue, splitErr := net.SplitHostPort(address)
		if splitErr == nil && strings.EqualFold(host, realChainFixtureHost()) && portValue == strconv.Itoa(port) {
			return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, network, net.JoinHostPort("127.0.0.1", portValue))
		}
		return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, network, address)
	}
	client := &http.Client{Timeout: 20 * time.Second, Transport: transport}
	return client, fixtureEndpoint{Port: port}, append([]byte(nil), torrent...), func() {
		_ = server.Shutdown(context.Background())
	}
}

// TestOneShotManualFileSelectionRealChain admits a one-shot with a manual
// file_selection through the real qB and file-operation chain: a selected
// video in a season folder and its same-folder subtitle are placed under the
// naming template like a first-level file, two selected copies of one episode
// in different folders both land under the versioned collision policy (the
// later one by source path with a version number), and an unselected
// first-level video is not placed.
func TestOneShotManualFileSelectionRealChain(t *testing.T) {
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	content := func(label string) []byte { return []byte("ospick " + label + " " + stamp) }
	files := []torrentFile{
		{Name: "OSPick Real Show - 03.mkv", Content: content("first-level unselected 03")},
		{Name: "Season 1/OSPick Real Show - 01.chs.ass", Content: []byte("Dialogue: 0,0:00:01.00,0:00:02.00,Default,,0,0,0,,ospick " + stamp)},
		{Name: "Season 1/OSPick Real Show - 01.mkv", Content: content("season 1 episode 01")},
		{Name: "Season 1/OSPick Real Show - 02.mkv", Content: content("season 1 episode 02")},
		{Name: "Season 2/OSPick Real Show - 02.mkv", Content: content("season 2 copy of episode 02")},
	}
	byManifest := map[string][]byte{}
	for _, file := range files {
		byManifest[fileSelectionTorrentName+"/"+file.Name] = file.Content
	}
	fixtureClient, fixtureURL, torrentBytes, closeFixture := startNestedFileFixture(t, fileSelectionTorrentName, files)
	t.Cleanup(closeFixture)

	svc := newRealChainServices(t, fixtureClient)
	defer svc.db.Close()
	ctx := context.Background()

	metadata, err := acquisitiontorrent.Parse(torrentBytes)
	if err != nil {
		t.Fatal(err)
	}
	infoHash := metadata.InfoHashV1
	payloadDigest := sha256.Sum256(torrentBytes)
	digest := hex.EncodeToString(payloadDigest[:])
	label := "one-shot-file-selection"
	draft, err := animetransport.Draft(singleEpisodeDraft(realChainFixtureURL(fixtureURL.Port, "/feed.xml")))
	if err != nil {
		t.Fatal(err)
	}
	draft.Origin = animedomain.SubscriptionOriginOneshot
	draft.OriginEntryRef = nil
	draft.InstanceLabel = &label
	bgm := animedomain.BGMBinding{WorkId: "ospick-bgm"}
	tmdb := animedomain.TMDBBinding{WorkId: "ospick-tmdb", MediaKind: animedomain.TMDBMediaKindTv, Season: 1}
	draft.Metadata.Bgm = &bgm
	draft.Metadata.Tmdb = &tmdb
	ownerYear := realChainObservedYear
	draft.Metadata.Year = &ownerYear
	ownerSeason := 1
	draft.Metadata.Season = &ownerSeason
	seed := oneshotapplication.SelectedSeed{
		Provider: animedomain.ProviderKindMikan, ProviderEntryRef: "70032",
		ProviderGroupRef: "679", Locator: realChainFixtureURL(fixtureURL.Port, "/artifact.torrent"),
		Title: "[RealGroup] OSPick Real Show [01-03] [1080p]", PublishedAt: time.Now().UTC(),
		InfoHash: &infoHash, ExpectedPayloadDigest: &digest,
	}
	recordOneShotSeedGroup(t, svc.db, seed, "RealGroup")
	// The draft's file effect is versioned; the frozen business carries it.
	if draft.Rules.Sections.FileEffect, err = json.Marshal(generated.FileEffectRulesReplace{
		Mode:  generated.FileEffectRulesReplaceModeReplace,
		Value: generated.FileEffectRules{Operation: generated.FileEffectRulesOperationCopy, CollisionPolicy: generated.CollisionPolicyVersioned},
	}); err != nil {
		t.Fatal(err)
	}
	draft = withDomainDraftBusiness(t, svc.db, draft)
	candidate, err := svc.draft.SaveAnimeConfigCandidate(ctx, draftapplication.CandidateDraftInput{Draft: draft})
	if err != nil {
		t.Fatal(err)
	}
	if candidate.CurrentRevision == nil {
		t.Fatal("saved candidate has no current revision")
	}
	deepVideo := fileSelectionTorrentName + "/Season 1/OSPick Real Show - 01.mkv"
	deepSubtitle := fileSelectionTorrentName + "/Season 1/OSPick Real Show - 01.chs.ass"
	firstCopy := fileSelectionTorrentName + "/Season 1/OSPick Real Show - 02.mkv"
	secondCopy := fileSelectionTorrentName + "/Season 2/OSPick Real Show - 02.mkv"
	unselected := fileSelectionTorrentName + "/OSPick Real Show - 03.mkv"
	input := oneshotapplication.AcquireSeedInput{
		CandidateID:   candidate.Id,
		Revision:      *candidate.CurrentRevision,
		Seed:          seed,
		FileSelection: []string{deepVideo, deepSubtitle, firstCopy, secondCopy},
		Caller: oneshotapplication.AcquireSeedCaller{Kind: oneshotapplication.AcquireSeedCallerAgent,
			Agent: &oneshotapplication.AgentSeedAuthorization{GrantedBy: "integration", AuthorizationRef: "one-shot-file-selection"}},
	}
	key := "one-shot-file-selection-" + strings.ReplaceAll(time.Now().UTC().Format("150405.000000000"), ".", "-")
	result, err := svc.oneShot.AcquireSeedOnce(ctx, input, key)
	if err != nil {
		t.Fatal(err)
	}
	if result.DownloadTaskID == nil || *result.DownloadTaskID == "" {
		t.Fatalf("one-shot admission returned no download task: %+v", result)
	}
	downloadTaskID := *result.DownloadTaskID
	if err := runWorkerUntilIdle(ctx, svc.runtime, svc.db, 32); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(90 * time.Second)
	var taskState string
	for time.Now().Before(deadline) {
		if _, err := svc.download.DispatchQBSync(ctx); err != nil {
			t.Fatal(err)
		}
		if err := runWorkerUntilIdle(ctx, svc.runtime, svc.db, 32); err != nil {
			t.Fatal(err)
		}
		if err := svc.db.QueryRowContext(ctx, `SELECT state FROM download_tasks WHERE id = $1`, downloadTaskID).Scan(&taskState); err != nil {
			t.Fatal(err)
		}
		if taskState == string(generated.DownloadTaskStateCompleted) || taskState == string(generated.DownloadTaskStateFailed) {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if taskState != string(generated.DownloadTaskStateCompleted) {
		t.Fatalf("one-shot task did not complete: state=%q", taskState)
	}
	var artifactID string
	if err := svc.db.QueryRowContext(ctx, `SELECT id FROM artifacts WHERE download_task_id = $1`, downloadTaskID).Scan(&artifactID); err != nil {
		t.Fatal(err)
	}
	if err := runWorkerUntilIdle(ctx, svc.runtime, svc.db, 32); err != nil {
		t.Fatal(err)
	}

	rows, err := svc.db.QueryContext(ctx, `SELECT source_path, target_path, state FROM file_operations WHERE artifact_id = $1`, artifactID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	targets := map[string]string{}
	for rows.Next() {
		var source, target, state string
		if err := rows.Scan(&source, &target, &state); err != nil {
			t.Fatal(err)
		}
		if state != string(generated.FileOperationStateCompleted) {
			t.Fatalf("file operation %s -> %s state=%q, want completed", source, target, state)
		}
		manifest := ""
		for candidate := range byManifest {
			if strings.HasSuffix(filepath.ToSlash(source), "/"+strings.TrimPrefix(candidate, fileSelectionTorrentName+"/")) {
				if manifest == "" || len(candidate) > len(manifest) {
					manifest = candidate
				}
			}
		}
		if manifest == "" {
			t.Fatalf("file operation source %s is no torrent file", source)
		}
		targets[manifest] = target
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	placed := make([]string, 0, len(targets))
	for manifest := range targets {
		placed = append(placed, manifest)
	}
	sort.Strings(placed)
	if want := []string{deepSubtitle, deepVideo, firstCopy, secondCopy}; strings.Join(placed, "|") != strings.Join(want, "|") {
		var plan []byte
		_ = svc.db.QueryRowContext(ctx, `SELECT release_facts->'batch_plan' FROM media_fact_snapshots WHERE release_id = $1`, result.ReleaseID).Scan(&plan)
		var targetRows []byte
		_ = svc.db.QueryRowContext(ctx, `SELECT COALESCE(jsonb_agg(jsonb_build_object('source', source_path, 'target', target_path)), '[]') FROM acquisition_targets WHERE release_id = $1`, result.ReleaseID).Scan(&targetRows)
		t.Logf("frozen batch plan: %s", plan)
		t.Logf("acquisition targets: %s", targetRows)
		t.Logf("artifact effects: %s", describeArtifactEffects(ctx, svc.db, artifactID))
		t.Fatalf("placed files = %v, want %v (the unselected %s stays in the download)", placed, want, unselected)
	}
	for manifest, target := range targets {
		got, err := os.ReadFile(target)
		if err != nil {
			t.Fatalf("read %s target %s: %v", manifest, target, err)
		}
		if !bytes.Equal(got, byManifest[manifest]) {
			t.Fatalf("%s target %s holds other bytes", manifest, target)
		}
	}
	// The deep video is named by the template (episode 1 of season 1), not by
	// its folder, and its subtitle lands beside it under its name.
	video := filepath.Base(targets[deepVideo])
	if !strings.Contains(video, "S01E01") {
		t.Fatalf("deep video target %s is not the named episode", targets[deepVideo])
	}
	subtitle := targets[deepSubtitle]
	if filepath.Dir(subtitle) != filepath.Dir(targets[deepVideo]) || !strings.HasPrefix(filepath.Base(subtitle), strings.TrimSuffix(video, filepath.Ext(video))+".") || filepath.Ext(subtitle) != ".ass" {
		t.Fatalf("subtitle target %s is not beside its video %s", subtitle, targets[deepVideo])
	}
	// Both copies of episode 2 landed: the first by source path keeps the
	// rendered name, the later one carries the version number.
	if !strings.Contains(filepath.Base(targets[firstCopy]), "S01E02") || targets[secondCopy] != batchplan.VersionedTargetPath(targets[firstCopy], 2) {
		t.Fatalf("episode 2 copies = %s and %s, want the second versioned", targets[firstCopy], targets[secondCopy])
	}
	var activePlacements int
	if err := svc.db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM file_operations fo
JOIN placements p ON p.source_evidence_type = 'file_operation' AND p.source_owner_ref = fo.id
WHERE fo.artifact_id = $1 AND p.state = 'active'`, artifactID).Scan(&activePlacements); err != nil {
		t.Fatal(err)
	}
	if activePlacements < 3 {
		t.Fatalf("active placements = %d, want both episode 2 copies and episode 1 active", activePlacements)
	}
	assertNoDuplicateLiveWorkItems(t, ctx, svc.db)
}
