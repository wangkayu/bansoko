package domain

import (
	"strings"
	"testing"
	"time"
)

func manifestFixture(t *testing.T, payloadDigest string) TorrentManifest {
	t.Helper()
	files := []TorrentFile{
		{SourcePath: "Show S1/ep1.mkv", Filename: "ep1.mkv", SizeBytes: 1234, MediaKind: TorrentFileMediaVideo},
		{SourcePath: "Show S1/Subs/ep1.zh-Hans.ass", Filename: "ep1.zh-Hans.ass", SizeBytes: 20, MediaKind: TorrentFileMediaSubtitle, Language: "zh-Hans"},
	}
	for index := range files {
		files[index].Ordinal = index
		files[index].FileRef = TorrentFileRef(payloadDigest, files[index].SourcePath)
	}
	digest, err := CanonicalTorrentProjectionDigest(payloadDigest, "torrent-parser-v1", files)
	if err != nil {
		t.Fatal(err)
	}
	return TorrentManifest{
		PayloadDigest:    payloadDigest,
		ParserVersion:    "torrent-parser-v1",
		TorrentVersion:   "v1",
		TorrentName:      "Show S1",
		InfoHashV1:       strings.Repeat("a", 40),
		TotalSizeBytes:   1254,
		ProjectionDigest: digest,
		Files:            files,
	}
}

func TestTorrentFileRefBindsPayloadAndPath(t *testing.T) {
	payloadA := DigestBytes([]byte("torrent-a"))
	payloadB := DigestBytes([]byte("torrent-b"))
	// Same path in two different payloads must never collide, and a different
	// path in the same payload must never collide either.
	refs := map[string]string{
		"a/ep1.mkv": TorrentFileRef(payloadA, "Show S1/ep1.mkv"),
		"b/ep1.mkv": TorrentFileRef(payloadB, "Show S1/ep1.mkv"),
		"a/other":   TorrentFileRef(payloadA, "Show S1/other.mkv"),
	}
	seen := map[string]string{}
	for label, ref := range refs {
		if !strings.HasPrefix(ref, "sha256:") || len(ref) != len("sha256:")+64 {
			t.Fatalf("%s produced a malformed file reference %q", label, ref)
		}
		if previous, duplicate := seen[ref]; duplicate {
			t.Fatalf("file reference collision between %s and %s", previous, label)
		}
		seen[ref] = label
	}
	// Derivation is deterministic for identical inputs.
	if TorrentFileRef(payloadA, "Show S1/ep1.mkv") != refs["a/ep1.mkv"] {
		t.Fatal("file reference derivation is not deterministic")
	}
}

func TestTorrentManifestProjectionDigestCoversFiles(t *testing.T) {
	payload := DigestBytes([]byte("torrent"))
	manifest := manifestFixture(t, payload)
	original := manifest.ProjectionDigest

	tampered := manifestFixture(t, payload)
	tampered.Files[0].SizeBytes++
	tampered.TotalSizeBytes++
	recomputed, err := CanonicalTorrentProjectionDigest(tampered.PayloadDigest, tampered.ParserVersion, tampered.Files)
	if err != nil {
		t.Fatal(err)
	}
	if recomputed == original {
		t.Fatal("projection digest did not change when a projected file changed")
	}
	// The altered manifest no longer validates against its stored digest.
	tampered.ProjectionDigest = original
	if err := tampered.Validate(); err == nil {
		t.Fatal("manifest with a stale projection digest was accepted")
	}
}

func TestTorrentManifestValidateRejectsInconsistentProjections(t *testing.T) {
	payload := DigestBytes([]byte("torrent"))
	cases := map[string]func(m *TorrentManifest){
		"non-canonical ordinal": func(m *TorrentManifest) { m.Files[1].Ordinal = 7 },
		"duplicated path":       func(m *TorrentManifest) { m.Files[1].SourcePath = m.Files[0].SourcePath },
		"unsafe path":           func(m *TorrentManifest) { m.Files[1].SourcePath = "../escape.ass" },
		"absolute path":         func(m *TorrentManifest) { m.Files[1].SourcePath = "/etc/passwd" },
		"size total mismatch":   func(m *TorrentManifest) { m.TotalSizeBytes++ },
		"missing info hash":     func(m *TorrentManifest) { m.InfoHashV1 = "" },
		"unknown media kind":    func(m *TorrentManifest) { m.Files[1].MediaKind = "executable" },
		"empty file set":        func(m *TorrentManifest) { m.Files = nil; m.TotalSizeBytes = 0 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			manifest := manifestFixture(t, payload)
			mutate(&manifest)
			if err := manifest.Validate(); err == nil {
				t.Fatalf("manifest with %s was accepted", name)
			}
		})
	}
	// The untouched fixture is valid, so the cases above fail for their reason.
	if err := manifestFixture(t, payload).Validate(); err != nil {
		t.Fatalf("baseline manifest must validate: %v", err)
	}
}

func TestTorrentManifestRequiresProjectionDigestBoundToPayload(t *testing.T) {
	payload := DigestBytes([]byte("torrent"))
	manifest := manifestFixture(t, payload)
	// A file reference derived from a different payload is not this manifest's file.
	manifest.Files[1].FileRef = TorrentFileRef(DigestBytes([]byte("other")), manifest.Files[1].SourcePath)
	if err := manifest.Validate(); err == nil {
		t.Fatal("manifest accepted a file reference bound to a different payload")
	}
}

func snapshotSeedFixture(t *testing.T, payloadDigest string, manifest *TorrentManifest) ProviderFeedSnapshotSeed {
	t.Helper()
	seed := ProviderFeedSnapshotSeed{
		StableSeedKey:  "seed-1",
		Locator:        "https://provider.example.test/a.torrent",
		LocatorDisplay: "https://provider.example.test/a.torrent",
		Title:          "Show - 01",
		Payload:        []byte("torrent"),
		PayloadDigest:  &payloadDigest,
		TorrentState:   TorrentAcquisitionAvailable,
		Manifest:       manifest,
	}
	return seed
}

func TestSnapshotSeedTorrentStateMustMatchItsManifest(t *testing.T) {
	payload := DigestBytes([]byte("torrent"))
	manifest := manifestFixture(t, payload)

	available := snapshotSeedFixture(t, payload, &manifest)
	if err := available.Validate(); err != nil {
		t.Fatalf("available seed with its manifest must validate: %v", err)
	}

	// available without a manifest is unreplayable.
	missing := available
	missing.Manifest = nil
	if err := missing.Validate(); err == nil {
		t.Fatal("available seed without a manifest was accepted")
	}

	// A manifest for a different payload is not this seed's projection.
	foreign := available
	otherManifest := manifestFixture(t, DigestBytes([]byte("other")))
	foreign.Manifest = &otherManifest
	if err := foreign.Validate(); err == nil {
		t.Fatal("seed accepted a manifest derived from a different payload")
	}

	// unavailable must explain itself and must not carry a manifest.
	code := "torrent_metadata_unavailable"
	unavailable := available
	unavailable.TorrentState = TorrentAcquisitionUnavailable
	unavailable.Manifest = nil
	unavailable.TorrentErrorCode = &code
	if err := unavailable.Validate(); err != nil {
		t.Fatalf("unavailable seed with a reason must validate: %v", err)
	}
	unavailable.TorrentErrorCode = nil
	if err := unavailable.Validate(); err == nil {
		t.Fatal("unavailable seed without a reason was accepted")
	}

	// not_applicable must carry neither.
	notApplicable := available
	notApplicable.TorrentState = TorrentAcquisitionNotApplicable
	notApplicable.Manifest = nil
	if err := notApplicable.Validate(); err != nil {
		t.Fatalf("seed without torrent acquisition must validate: %v", err)
	}
	notApplicable.Manifest = &manifest
	if err := notApplicable.Validate(); err == nil {
		t.Fatal("seed classified not_applicable accepted a manifest")
	}
}

func TestSnapshotContentHashCoversManifestProjection(t *testing.T) {
	payload := DigestBytes([]byte("torrent"))
	manifest := manifestFixture(t, payload)
	seed := snapshotSeedFixture(t, payload, &manifest)

	first, err := CanonicalFeedSnapshotContentHash([]ProviderFeedSnapshotSeed{seed})
	if err != nil {
		t.Fatal(err)
	}

	// The same seed facts re-hash to the same identity.
	repeat, err := CanonicalFeedSnapshotContentHash([]ProviderFeedSnapshotSeed{seed})
	if err != nil {
		t.Fatal(err)
	}
	if repeat != first {
		t.Fatal("canonical content hash is not deterministic")
	}

	// Re-projecting the file tree changes the snapshot's content identity, so a
	// stored manifest can never silently disagree with the snapshot it belongs to.
	reprojected := seed
	otherManifest := manifestFixture(t, payload)
	otherManifest.Files[0].SizeBytes++
	otherManifest.TotalSizeBytes++
	digest, err := CanonicalTorrentProjectionDigest(payload, otherManifest.ParserVersion, otherManifest.Files)
	if err != nil {
		t.Fatal(err)
	}
	otherManifest.ProjectionDigest = digest
	reprojected.Manifest = &otherManifest
	second, err := CanonicalFeedSnapshotContentHash([]ProviderFeedSnapshotSeed{reprojected})
	if err != nil {
		t.Fatal(err)
	}
	if second == first {
		t.Fatal("content hash ignored the manifest projection")
	}

	// A seed that becomes unavailable is a different snapshot identity too.
	unavailable := seed
	unavailable.Manifest = nil
	unavailable.TorrentState = TorrentAcquisitionUnavailable
	code := "torrent_metadata_unavailable"
	unavailable.TorrentErrorCode = &code
	third, err := CanonicalFeedSnapshotContentHash([]ProviderFeedSnapshotSeed{unavailable})
	if err != nil {
		t.Fatal(err)
	}
	if third == first {
		t.Fatal("content hash ignored the torrent acquisition state")
	}
}

func TestSnapshotWriteRequiresCurrentSchemas(t *testing.T) {
	payload := DigestBytes([]byte("torrent"))
	manifest := manifestFixture(t, payload)
	seed := snapshotSeedFixture(t, payload, &manifest)
	contentHash, err := CanonicalFeedSnapshotContentHash([]ProviderFeedSnapshotSeed{seed})
	if err != nil {
		t.Fatal(err)
	}
	fetchedAt := time.Date(2026, 9, 22, 10, 0, 0, 0, time.UTC)
	write := ProviderFeedSnapshotWrite{
		Provider: "anibt", ProviderEntryRef: "1001", ProviderGroupRef: "12",
		FetchedAt: fetchedAt, FreshUntil: fetchedAt.Add(15 * time.Minute),
		ContentHash: contentHash, HashSchema: ProviderFeedSnapshotContentHashSchema,
		ProjectionSchema: TorrentManifestProjectionSchema,
		Seeds:            []ProviderFeedSnapshotSeed{seed},
	}
	if err := write.Validate(); err != nil {
		t.Fatalf("current-schema snapshot write must validate: %v", err)
	}
	stale := write
	stale.HashSchema = "provider-feed-snapshot-v0"
	if err := stale.Validate(); err == nil {
		t.Fatal("an unknown hash schema was accepted for a new write")
	}
	noProjection := write
	noProjection.ProjectionSchema = ""
	if err := noProjection.Validate(); err == nil {
		t.Fatal("a write without its projection schema was accepted")
	}
}
