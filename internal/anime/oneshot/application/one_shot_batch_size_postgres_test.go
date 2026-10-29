package application

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	acquisitiontarget "bansoko/internal/acquisition/target"
	terminal "bansoko/internal/acquisition/terminal/application"
	"bansoko/internal/acquisition/torrent"
	animeapplication "bansoko/internal/anime/application"
	librarydomain "bansoko/internal/library/domain"
	"bansoko/internal/namingvars"
	"bansoko/internal/testutil/animetest"
	"bansoko/internal/transport/httpapi/generated"
)

// PostgreSQL coverage of the frozen record's size for batch one-shots. The
// release-level naming vars input is frozen once (download_tasks.frozen_policy)
// and every target names it by digest; the target set is stored once, one
// acquisition_targets row per target, and the one-shot snapshot and the
// download task's frozen input name it by set digest and count. Nothing frozen
// grows with targets × files.

// batchTorrentFile is one entry of a synthetic v1 multi-file torrent.
type batchTorrentFile struct {
	path []string
	size int64
}

// batchTorrentPayload assembles a structurally valid v1 multi-file torrent
// under root and parses it the way the fetcher does.
func batchTorrentPayload(t *testing.T, root string, files []batchTorrentFile) animeapplication.TorrentPayload {
	t.Helper()
	var builder strings.Builder
	builder.WriteString("d4:infod5:filesl")
	for _, file := range files {
		builder.WriteString("d6:lengthi" + strconv.FormatInt(file.size, 10) + "e4:pathl")
		for _, part := range file.path {
			builder.WriteString(strconv.Itoa(len(part)) + ":" + part)
		}
		builder.WriteString("ee")
	}
	builder.WriteString("e4:name" + strconv.Itoa(len(root)) + ":" + root + "ee")
	data := []byte(builder.String())
	meta, err := torrent.Parse(data)
	if err != nil {
		t.Fatalf("parse batch torrent: %v", err)
	}
	if len(meta.Files) != len(files) {
		t.Fatalf("batch torrent projects %d files, want %d", len(meta.Files), len(files))
	}
	return animeapplication.TorrentPayload{Bytes: data, Metadata: meta}
}

// episodeBatchFiles is a first-level batch: one video per episode and a few
// archives, all directly under the torrent root.
func episodeBatchFiles(episodes, archives int) []batchTorrentFile {
	width := 2
	if episodes >= 100 {
		width = 3
	}
	files := make([]batchTorrentFile, 0, episodes+archives)
	for episode := 1; episode <= episodes; episode++ {
		files = append(files, batchTorrentFile{path: []string{fmt.Sprintf("Detective Conan - %0*d [1080p][HEVC].mkv", width, episode)}, size: int64(1<<20 + episode)})
	}
	for index := 1; index <= archives; index++ {
		files = append(files, batchTorrentFile{path: []string{fmt.Sprintf("Detective Conan Extras Vol.%d.7z", index)}, size: int64(4096 + index)})
	}
	return files
}

// boxSetFiles is the shape of a large Blu-ray box set: 619 files (131 videos,
// 96 subtitles, 392 others), the deepest seven directories below the root.
// It returns the files and the manifest paths of the videos.
func boxSetFiles(root string) ([]batchTorrentFile, []string) {
	const tag = "[Ma10p_1080p][x265_flac]"
	var files []batchTorrentFile
	var videos []string
	add := func(size int64, parts ...string) {
		files = append(files, batchTorrentFile{path: parts, size: size})
	}
	for episode := 1; episode <= 131; episode++ {
		season := fmt.Sprintf("Detective Conan Season %02d [Ma10p_1080p]", (episode-1)/33+1)
		disc := fmt.Sprintf("Disc %02d", (episode-1)/6+1)
		name := fmt.Sprintf("[Group] Detective Conan [%03d]%s", episode, tag)
		add(int64(1<<30+episode), season, disc, "Main", "Episodes", name+".mkv")
		videos = append(videos, strings.Join([]string{root, season, disc, "Main", "Episodes", name + ".mkv"}, "/"))
		add(int64(1<<25+episode), season, disc, "Main", "Episodes", "Audio", name+".flac")
		if episode <= 96 {
			add(int64(1<<16+episode), season, disc, "Main", "Episodes", name+".chs.ass")
		}
	}
	for page := 1; page <= 200; page++ {
		season := fmt.Sprintf("Detective Conan Season %02d [Ma10p_1080p]", (page-1)/50+1)
		add(int64(1<<21+page), season, "Disc 01", "Extras", "Menus", "Scans", "Booklet", "Pages", fmt.Sprintf("Detective Conan Booklet Page %03d.png", page))
	}
	for index := 1; index <= 61; index++ {
		season := fmt.Sprintf("Detective Conan Season %02d [Ma10p_1080p]", (index-1)/16+1)
		add(int64(1<<12+index), season, fmt.Sprintf("Disc %02d", index%5+1), "Extras", fmt.Sprintf("Detective Conan Disc Notes %02d.txt", index))
	}
	return files, videos
}

// admitBatchOneShot refreshes a snapshot that retains payload and admits it
// with the current-input draft and the given manual selection. The naming
// templates are rooted under a scratch directory before the draft copies
// Global, and the draft states the owner year and season the shipped TV
// templates name, so every selected video plans a target.
func admitBatchOneShot(t *testing.T, payload animeapplication.TorrentPayload, title string, selection []string, key string) (*oneShotTestServices, *sql.DB, AcquireSeedInput, OneShotAdmissionResult, error) {
	t.Helper()
	service, _, db, seed, refresh := humanOneShotFixtureWithPayload(t, payload)
	pinOneShotNamingRoot(t, db)
	seed.Title = title
	snapshot := refresh([]acquisitiondomain.FeedItem{seedFeedItem(seed)})
	_, input := currentInputForSeed(t, service, seed, func(draft generated.AnimeDraft) generated.AnimeDraft {
		year, season := oneShotObservedYear, 1
		draft.Metadata.Year = &year
		draft.Metadata.Season = &season
		return draft
	})
	input.Seed = bindSeedIdentity(seed, snapshot)
	input.FileSelection = selection
	result, err := service.AcquireSeedOnce(context.Background(), input, key)
	return service, db, input, result, err
}

// frozenBlobSize is one capped frozen document measured three ways: raw is its
// compact JSON (what the Go caps measure), datum is pg_column_size of a fresh
// jsonb datum of it (what the column CHECK measures when the row is written),
// stored is pg_column_size of the stored value (after TOAST compression).
type frozenBlobSize struct {
	name                string
	raw, datum, stored  int
	limit               int
	measuredByOctetText bool
}

// measureJSONB measures the largest document the query returns. The query
// selects (doc::text, pg_column_size(doc::text::jsonb), pg_column_size(doc)).
func measureJSONB(t *testing.T, db *sql.DB, name string, limit int, query string, args ...any) frozenBlobSize {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), query, args...)
	if err != nil {
		t.Fatalf("measure %s: %v", name, err)
	}
	defer rows.Close()
	size := frozenBlobSize{name: name, limit: limit}
	found := false
	for rows.Next() {
		var text []byte
		var datum, stored int
		if err := rows.Scan(&text, &datum, &stored); err != nil {
			t.Fatalf("scan %s: %v", name, err)
		}
		var compact bytes.Buffer
		if err := json.Compact(&compact, text); err != nil {
			t.Fatalf("compact %s: %v", name, err)
		}
		found = true
		if compact.Len() > size.raw {
			size.raw = compact.Len()
		}
		if datum > size.datum {
			size.datum = datum
		}
		if stored > size.stored {
			size.stored = stored
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows %s: %v", name, err)
	}
	if !found {
		t.Fatalf("measure %s: no row", name)
	}
	return size
}

// measureOneShotBlobs measures every capped document the admission froze. The
// sizes are logged; nothing of their content is.
func measureOneShotBlobs(t *testing.T, db *sql.DB, result OneShotAdmissionResult) map[string]frozenBlobSize {
	t.Helper()
	sizes := map[string]frozenBlobSize{}
	record := func(size frozenBlobSize) {
		sizes[size.name] = size
		t.Logf("%-44s raw=%7d datum=%7d stored=%7d limit=%7d", size.name, size.raw, size.datum, size.stored, size.limit)
		measured := size.datum
		if size.measuredByOctetText {
			measured = size.raw
		}
		if measured > size.limit {
			t.Fatalf("%s measures %d, over its bound %d", size.name, measured, size.limit)
		}
	}
	record(measureJSONB(t, db, "one_shot_acquisitions.input_snapshot", 262144, `
SELECT input_snapshot::text, pg_column_size(input_snapshot::text::jsonb), pg_column_size(input_snapshot)
FROM one_shot_acquisitions WHERE id = $1`, result.OneShotID))
	record(measureJSONB(t, db, "one_shot_execution_attempts.input_snapshot", 262144, `
SELECT input_snapshot::text, pg_column_size(input_snapshot::text::jsonb), pg_column_size(input_snapshot)
FROM one_shot_execution_attempts WHERE acquisition_id = $1`, result.OneShotID))
	record(measureJSONB(t, db, "download_tasks.frozen_input", 1048576, `
SELECT frozen_input::text, pg_column_size(frozen_input::text::jsonb), pg_column_size(frozen_input)
FROM download_tasks WHERE release_id = $1`, result.ReleaseID))
	record(measureJSONB(t, db, "download_tasks.frozen_policy", 1048576, `
SELECT frozen_policy::text, pg_column_size(frozen_policy::text::jsonb), pg_column_size(frozen_policy)
FROM download_tasks WHERE release_id = $1`, result.ReleaseID))
	record(measureJSONB(t, db, "download_tasks.frozen_policy.vars.input", 262144, `
SELECT (frozen_policy->'vars'->'input')::text, pg_column_size((frozen_policy->'vars'->'input')::text::jsonb), pg_column_size(frozen_policy->'vars'->'input')
FROM download_tasks WHERE release_id = $1`, result.ReleaseID))
	record(measureJSONB(t, db, "work_items.input_snapshot", 1048576, `
SELECT w.input_snapshot::text, pg_column_size(w.input_snapshot::text::jsonb), pg_column_size(w.input_snapshot)
FROM work_items w JOIN download_tasks d ON d.id = w.owner_id WHERE d.release_id = $1`, result.ReleaseID))
	record(measureJSONB(t, db, "acquisition_targets.render_snapshot", 1048576, `
SELECT render_snapshot::text, pg_column_size(render_snapshot::text::jsonb), pg_column_size(render_snapshot)
FROM acquisition_targets WHERE release_id = $1`, result.ReleaseID))
	record(measureJSONB(t, db, "acquisition_targets.config_snapshot", 1048576, `
SELECT config_snapshot::text, pg_column_size(config_snapshot::text::jsonb), pg_column_size(config_snapshot)
FROM acquisition_targets WHERE release_id = $1`, result.ReleaseID))
	record(measureJSONB(t, db, "media_fact_snapshots.release_facts", 2097152, `
SELECT release_facts::text, pg_column_size(release_facts::text::jsonb), pg_column_size(release_facts)
FROM media_fact_snapshots WHERE release_id = $1`, result.ReleaseID))
	record(measureJSONB(t, db, "releases.internal_facts", 2097152, `
SELECT internal_facts::text, pg_column_size(internal_facts::text::jsonb), pg_column_size(internal_facts)
FROM releases WHERE id = $1`, result.ReleaseID))
	canonical := measureJSONB(t, db, "acquisitions.canonical_request", 65536, `
SELECT a.canonical_request::text, pg_column_size(a.canonical_request::text::jsonb), pg_column_size(a.canonical_request)
FROM acquisitions a JOIN download_tasks d ON d.acquisition_id = a.id WHERE d.release_id = $1`, result.ReleaseID)
	canonical.measuredByOctetText = true
	record(canonical)
	var inputCanonical int
	if err := db.QueryRowContext(context.Background(), `
SELECT octet_length(a.input_canonical_utf8)
FROM acquisitions a JOIN download_tasks d ON d.acquisition_id = a.id WHERE d.release_id = $1`, result.ReleaseID).Scan(&inputCanonical); err != nil {
		t.Fatalf("measure acquisitions.input_canonical_utf8: %v", err)
	}
	record(frozenBlobSize{name: "acquisitions.input_canonical_utf8", raw: inputCanonical, datum: inputCanonical, stored: inputCanonical, limit: 1048576, measuredByOctetText: true})
	return sizes
}

// targetSetDigestFromRows recomputes the target set digest from the stored
// rows exactly as the planner computed it: the specs in target_ref order with
// their normalized render/config snapshots.
func targetSetDigestFromRows(t *testing.T, db *sql.DB, releaseID string) (string, int) {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), `
SELECT id, target_ref, source_path, media_kind, target_path, expected_size_bytes, intent, collision_policy,
       render_snapshot::text, config_snapshot::text, spec_digest
FROM acquisition_targets WHERE release_id = $1 ORDER BY target_ref COLLATE "C"`, releaseID)
	if err != nil {
		t.Fatalf("read target rows: %v", err)
	}
	defer rows.Close()
	normalize := func(raw string) json.RawMessage {
		var object map[string]any
		if err := json.Unmarshal([]byte(raw), &object); err != nil {
			t.Fatalf("decode target snapshot: %v", err)
		}
		encoded, err := json.Marshal(object)
		if err != nil {
			t.Fatal(err)
		}
		return encoded
	}
	set := acquisitiontarget.Set{ReleaseID: releaseID}
	for rows.Next() {
		var spec acquisitiontarget.Spec
		var render, config string
		var intent, collision string
		if err := rows.Scan(&spec.ID, &spec.TargetRef, &spec.SourcePath, &spec.MediaKind, &spec.TargetPath, &spec.ExpectedSizeBytes, &intent, &collision, &render, &config, &spec.SpecDigest); err != nil {
			t.Fatalf("scan target row: %v", err)
		}
		spec.ReleaseID = releaseID
		spec.Intent = acquisitiontarget.Intent(intent)
		spec.CollisionPolicy = librarydomain.CollisionPolicy(collision)
		spec.RenderSnapshot = normalize(render)
		spec.ConfigSnapshot = normalize(config)
		set.Targets = append(set.Targets, spec)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(set)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:]), len(set.Targets)
}

// assertTargetSetReference checks a carrier's target facts are the reference
// form and name exactly the stored set.
func assertTargetSetReference(t *testing.T, carrier string, raw json.RawMessage, wantDigest string, wantCount int) {
	t.Helper()
	var facts map[string]json.RawMessage
	if err := json.Unmarshal(raw, &facts); err != nil {
		t.Fatalf("%s target facts: %v", carrier, err)
	}
	for _, embedded := range []string{"targets", "observations"} {
		if _, ok := facts[embedded]; ok {
			t.Fatalf("%s target facts still embed %s", carrier, embedded)
		}
	}
	var reference acquisitiontarget.FrozenFacts
	if err := json.Unmarshal(raw, &reference); err != nil {
		t.Fatalf("%s target facts: %v", carrier, err)
	}
	if reference.Admission != string(acquisitiontarget.AdmissionDownload) || reference.TargetSetDigest != wantDigest || reference.TargetCount != wantCount {
		t.Fatalf("%s target facts = %+v, want download_required naming %s with %d targets", carrier, reference, wantDigest, wantCount)
	}
}

// assertBatchFrozenRecord checks the admitted record: the stored set, the
// reference form of both carriers and the vars reference of every target.
func assertBatchFrozenRecord(t *testing.T, service *oneShotTestServices, db *sql.DB, result OneShotAdmissionResult, wantTargets int) {
	t.Helper()
	ctx := context.Background()
	if result.State != string(OneShotAcquisitionStateRunning) || result.DownloadTaskID == nil {
		t.Fatalf("admission state = %q task = %v, want a running chain with a download task", result.State, result.DownloadTaskID)
	}
	digest, count := targetSetDigestFromRows(t, db, result.ReleaseID)
	if count != wantTargets {
		t.Fatalf("stored target set has %d targets, want %d", count, wantTargets)
	}
	view, err := service.ReadOneShotAcquisition(ctx, result.OneShotID)
	if err != nil {
		t.Fatalf("read one-shot view: %v", err)
	}
	if view.InputSnapshot == nil {
		t.Fatal("one-shot view has no input snapshot")
	}
	assertTargetSetReference(t, "one-shot snapshot", view.InputSnapshot.TargetFacts, digest, count)
	var frozenInput, varsDigestSQL string
	var frozenPolicy []byte
	if err := db.QueryRowContext(ctx, `
SELECT frozen_input->'target_facts', frozen_policy,
       'sha256:' || encode(digest(convert_to(anime_config_candidate_canonical_json(frozen_policy->'vars'), 'UTF8'), 'sha256'), 'hex')
FROM download_tasks WHERE release_id = $1`, result.ReleaseID).Scan(&frozenInput, &frozenPolicy, &varsDigestSQL); err != nil {
		t.Fatalf("read frozen task: %v", err)
	}
	assertTargetSetReference(t, "download task frozen input", json.RawMessage(frozenInput), digest, count)
	var copiesFacts bool
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(frozen_input->'selected_release' ? 'internal_facts', false) FROM download_tasks WHERE release_id = $1`, result.ReleaseID).Scan(&copiesFacts); err != nil {
		t.Fatal(err)
	}
	if copiesFacts {
		t.Fatal("the frozen input's selected release still copies the release's internal facts")
	}
	var policy struct {
		Vars *namingvars.FrozenEvaluation `json:"vars"`
	}
	if err := json.Unmarshal(frozenPolicy, &policy); err != nil || policy.Vars == nil {
		t.Fatalf("frozen policy vars: %v", err)
	}
	varsDigest, err := acquisitiontarget.VarsDigest(policy.Vars)
	if err != nil {
		t.Fatal(err)
	}
	if varsDigest != varsDigestSQL {
		t.Fatalf("vars digest %s differs from the SQL canonical digest %s", varsDigest, varsDigestSQL)
	}
	var withVars, withoutDigest int
	if err := db.QueryRowContext(ctx, `
SELECT COUNT(*) FILTER (WHERE render_snapshot ? 'vars'),
       COUNT(*) FILTER (WHERE render_snapshot->>'vars_digest' IS DISTINCT FROM $2)
FROM acquisition_targets WHERE release_id = $1`, result.ReleaseID, varsDigest).Scan(&withVars, &withoutDigest); err != nil {
		t.Fatal(err)
	}
	if withVars != 0 || withoutDigest != 0 {
		t.Fatalf("render snapshots: %d still embed vars, %d do not name the frozen vars digest", withVars, withoutDigest)
	}
}

// A 24-episode batch with two archives is admitted, and the record holds the
// release-level inventory once: the one-shot snapshot does not grow with the
// targets at all, and the per-file terms grow by a bounded amount per file.
func TestAcquireSeedOnceAdmitsEpisodeBatchesWithAFrozenRecordLinearInFiles(t *testing.T) {
	type admitted struct {
		files   int
		targets int
		sizes   map[string]frozenBlobSize
	}
	run := func(t *testing.T, episodes int) admitted {
		payload := batchTorrentPayload(t, fmt.Sprintf("Detective Conan [01-%02d][1080p]", episodes), episodeBatchFiles(episodes, 2))
		service, db, _, result, err := admitBatchOneShot(t, payload, fmt.Sprintf("[Group] Detective Conan [01-%02d][1080p]", episodes), nil, fmt.Sprintf("one-shot-batch-%03d-0001", episodes))
		if err != nil {
			t.Fatalf("admit a %d-episode batch: %v", episodes, err)
		}
		assertBatchFrozenRecord(t, service, db, result, episodes)
		return admitted{files: episodes + 2, targets: episodes, sizes: measureOneShotBlobs(t, db, result)}
	}
	var small, large admitted
	t.Run("24 episodes and 2 archives", func(t *testing.T) { small = run(t, 24) })
	t.Run("100 episodes and 2 archives", func(t *testing.T) { large = run(t, 100) })
	if small.sizes == nil || large.sizes == nil {
		t.Fatal("a batch was not admitted")
	}
	// The snapshot carries no per-target term: only the count's digits and the
	// seed title differ.
	snapshotGrowth := large.sizes["one_shot_acquisitions.input_snapshot"].raw - small.sizes["one_shot_acquisitions.input_snapshot"].raw
	if snapshotGrowth < -64 || snapshotGrowth > 64 {
		t.Fatalf("one-shot snapshot grew by %d bytes from 24 to 100 targets", snapshotGrowth)
	}
	// Every other frozen term is per file (release_files, the vars inventory),
	// never per target × file.
	addedFiles := large.files - small.files
	for _, name := range []string{"download_tasks.frozen_input", "download_tasks.frozen_policy", "work_items.input_snapshot"} {
		perFile := (large.sizes[name].raw - small.sizes[name].raw) / addedFiles
		t.Logf("%s grows by %d bytes per added file", name, perFile)
		if perFile > 2048 {
			t.Fatalf("%s grows by %d bytes per file, want a bounded per-file term", name, perFile)
		}
	}
	if render := large.sizes["acquisition_targets.render_snapshot"].raw; render > 4096 {
		t.Fatalf("a render snapshot is %d bytes, want a bounded per-target document", render)
	}
}

// A Blu-ray box set of 619 files (seven directories deep) with its 131 videos
// selected by hand is admitted. Its file inventory is frozen once, in the
// vars input of the frozen policy.
func TestAcquireSeedOnceAdmitsALargeBoxSetWithAManualSelection(t *testing.T) {
	const root = "[Group] Detective Conan [Ma10p_1080p]"
	files, videos := boxSetFiles(root)
	if len(files) != 619 || len(videos) != 131 {
		t.Fatalf("box set fixture has %d files and %d videos", len(files), len(videos))
	}
	payload := batchTorrentPayload(t, root, files)
	service, db, _, result, err := admitBatchOneShot(t, payload, root, videos, "one-shot-box-set-0001")
	if err != nil {
		t.Fatalf("admit the box set: %v", err)
	}
	assertBatchFrozenRecord(t, service, db, result, len(videos))
	sizes := measureOneShotBlobs(t, db, result)
	if inventory := sizes["download_tasks.frozen_policy.vars.input"].raw; inventory < 140000 {
		t.Fatalf("the fixture's vars input is %d bytes, want a box-set sized inventory", inventory)
	}
}

// A torrent listing 20,000 files (the manifest maximum) is refused with the
// typed size refusal before anything is written.
func TestAcquireSeedOnceRefusesATwentyThousandFileTorrentTyped(t *testing.T) {
	const root = "Detective Conan Archive"
	files := make([]batchTorrentFile, 0, 20000)
	for index := 1; index <= 20000; index++ {
		files = append(files, batchTorrentFile{path: []string{fmt.Sprintf("Part %03d", index/100), fmt.Sprintf("Detective Conan Frame %05d.png", index)}, size: int64(4096 + index)})
	}
	files[0] = batchTorrentFile{path: []string{"Detective Conan - 01 [1080p].mkv"}, size: 1 << 30}
	payload := batchTorrentPayload(t, root, files)
	if len(payload.Bytes) > 20<<20 {
		t.Fatalf("fixture payload is %d bytes, over the 20 MiB torrent input bound", len(payload.Bytes))
	}
	_, db, _, _, err := admitBatchOneShot(t, payload, "[Group] Detective Conan Archive", nil, "one-shot-huge-0001")
	if !isBusinessCode(err, 422, terminal.TorrentTooLargeCode) {
		t.Fatalf("admission of 20,000 files err = %v, want 422 %s", err, terminal.TorrentTooLargeCode)
	}
	assertNoOneShotRows(t, db)
}

// migration101SQL reads the forward migration under test.
func migration101SQL(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "db", "migrations", "101_frozen_target_set_reference.sql"))
	if err != nil {
		t.Fatalf("read migration 101: %v", err)
	}
	return string(raw)
}

// Migration 101 on a running one-shot: the embedded target copies the
// carriers held before the upgrade are rewritten to the reference form the
// current admission writes, byte for byte, the replay identity is untouched
// and the same idempotency key still answers with the original handle.
func TestMigration101RewritesARunningOneShotToTheReferenceForm(t *testing.T) {
	ctx := context.Background()
	payload := batchTorrentPayload(t, "Detective Conan [01-24][1080p]", episodeBatchFiles(24, 2))
	service, db, input, result, err := admitBatchOneShot(t, payload, "[Group] Detective Conan [01-24][1080p]", nil, "one-shot-m101-0001")
	if err != nil {
		t.Fatalf("admit the batch: %v", err)
	}
	owner := animetest.OwnerTestDB(t)
	type carrier struct {
		table, column, where string
		arg                  string
	}
	carriers := []carrier{
		{"one_shot_acquisitions", "input_snapshot", "id = $1", result.OneShotID},
		{"one_shot_execution_attempts", "input_snapshot", "acquisition_id = $1", result.OneShotID},
		{"download_tasks", "frozen_input", "release_id = $1", result.ReleaseID},
	}
	type identity struct {
		document, hash, digest string
	}
	read := func(c carrier) identity {
		t.Helper()
		var got identity
		hashColumn, digestColumn := "''", "''"
		switch c.table {
		case "one_shot_acquisitions":
			hashColumn = "request_hash"
		case "one_shot_execution_attempts":
			hashColumn, digestColumn = "request_hash", "input_digest"
		case "download_tasks":
			hashColumn = "idempotency_key"
		}
		if err := owner.QueryRowContext(ctx, `SELECT `+c.column+`::text, COALESCE(`+hashColumn+`, ''), COALESCE(`+digestColumn+`, '') FROM `+c.table+` WHERE `+c.where, c.arg).Scan(&got.document, &got.hash, &got.digest); err != nil {
			t.Fatalf("read %s: %v", c.table, err)
		}
		return got
	}
	before := make([]identity, len(carriers))
	for index, c := range carriers {
		before[index] = read(c)
	}

	// The pre-upgrade form: every target spec, its render snapshot holding its
	// own copy of the release's vars evaluation, and every observation.
	var embedded string
	if err := owner.QueryRowContext(ctx, `
SELECT jsonb_build_object(
    'targets', jsonb_agg(jsonb_build_object(
        'id', t.id, 'release_id', t.release_id, 'target_ref', t.target_ref, 'source_path', t.source_path,
        'media_kind', t.media_kind, 'target_path', t.target_path, 'expected_size_bytes', t.expected_size_bytes,
        'intent', t.intent, 'collision_policy', t.collision_policy,
        'render_snapshot', (t.render_snapshot - 'vars_digest') || jsonb_build_object('vars', d.frozen_policy->'vars'),
        'config_snapshot', t.config_snapshot, 'spec_digest', t.spec_digest) ORDER BY t.target_ref),
    'observations', jsonb_agg(jsonb_build_object('target_id', t.id, 'state', 'missing', 'observed_at', '2026-10-28T00:00:00Z') ORDER BY t.target_ref))::text
FROM acquisition_targets t JOIN download_tasks d ON d.release_id = t.release_id
WHERE t.release_id = $1`, result.ReleaseID).Scan(&embedded); err != nil {
		t.Fatalf("build the pre-upgrade target facts: %v", err)
	}
	for _, c := range carriers {
		if _, err := owner.ExecContext(ctx, `
UPDATE `+c.table+` SET `+c.column+` = jsonb_set(`+c.column+`, '{target_facts}', ((`+c.column+`->'target_facts') - 'target_count') || $2::jsonb)
WHERE `+c.where, c.arg, embedded); err != nil {
			t.Fatalf("write the pre-upgrade form into %s: %v", c.table, err)
		}
	}
	// The task's selected release also carried a copy of the release's internal
	// facts, its whole file list included.
	if _, err := owner.ExecContext(ctx, `
UPDATE download_tasks d
SET frozen_input = jsonb_set(d.frozen_input, '{selected_release,internal_facts}', r.internal_facts)
FROM releases r
WHERE r.id = d.release_id AND d.release_id = $1`, result.ReleaseID); err != nil {
		t.Fatalf("write the pre-upgrade selected release: %v", err)
	}

	if _, err := owner.ExecContext(ctx, migration101SQL(t)); err != nil {
		t.Fatalf("apply migration 101: %v", err)
	}

	for index, c := range carriers {
		after := read(c)
		var equal bool
		if err := owner.QueryRowContext(ctx, `SELECT $1::jsonb = $2::jsonb`, after.document, before[index].document).Scan(&equal); err != nil {
			t.Fatal(err)
		}
		if !equal {
			t.Fatalf("%s.%s after migration 101 differs from what the admission writes", c.table, c.column)
		}
		if after.hash != before[index].hash || after.digest != before[index].digest {
			t.Fatalf("%s replay identity changed: %+v -> %+v", c.table, before[index], after)
		}
	}
	replay, err := service.AcquireSeedOnce(ctx, input, "one-shot-m101-0001")
	if err != nil || replay.OneShotID != result.OneShotID {
		t.Fatalf("replay after migration 101 = %v, %v; want the original handle %s", replay.OneShotID, err, result.OneShotID)
	}
	assertBatchFrozenRecord(t, service, db, result, 24)
}

// The column CHECKs measure the jsonb datum the row is written with, before
// TOAST compression; pg_column_size of a stored value reports its compressed
// size. A compressible document over the bound is refused although its stored
// form would be small, and an accepted one reads back much smaller.
func TestFrozenSizeBoundsMeasureTheUncompressedDatum(t *testing.T) {
	ctx := context.Background()
	// A temporary table lives in one session: every statement runs on one
	// connection.
	conn, err := animetest.OwnerTestDB(t).Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `CREATE TEMP TABLE frozen_size_probe (doc JSONB NOT NULL CHECK (pg_column_size(doc) <= 262144))`); err != nil {
		t.Fatal(err)
	}
	defer conn.ExecContext(context.Background(), `DROP TABLE IF EXISTS frozen_size_probe`)
	if _, err := conn.ExecContext(ctx, `INSERT INTO frozen_size_probe VALUES (jsonb_build_object('pad', repeat('a', 300000)))`); err == nil {
		t.Fatal("a compressible 300 KB document passed a 256 KiB pg_column_size bound")
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO frozen_size_probe VALUES (jsonb_build_object('pad', repeat('a', 200000)))`); err != nil {
		t.Fatalf("a 200 KB document was refused: %v", err)
	}
	var stored, datum int
	if err := conn.QueryRowContext(ctx, `SELECT pg_column_size(doc), pg_column_size(doc::text::jsonb) FROM frozen_size_probe`).Scan(&stored, &datum); err != nil {
		t.Fatal(err)
	}
	t.Logf("compressible 200 KB document: stored=%d datum=%d", stored, datum)
	if stored >= datum || datum < 200000 {
		t.Fatalf("stored=%d datum=%d, want the stored size compressed below the datum", stored, datum)
	}
}
