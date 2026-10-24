package application

import (
	"regexp"
	"strings"
	"testing"

	animedomain "bansoko/internal/anime/domain"
	animecatalogdomain "bansoko/internal/catalog/domain"
)

// Every RSS file row must carry the identity the contract requires:
// file_ref = TorrentFileRef(payload_digest, source_path). Preview mode has
// always derived it from the seed's own manifest; the owner mode rebuilds its
// projection from the release's frozen file facts, and before this test that
// rebuild carried no payload digest, so every owner row shipped an empty
// file_ref — a value the OpenAPI schema requires and that the browser keys rows
// by.

const projectionIdentityPayload = "sha256:" + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// fileRefPattern is the RSSListFile.file_ref pattern as the contract states it.
var fileRefPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

func projectionIdentityManifest(t *testing.T) *animecatalogdomain.TorrentManifest {
	t.Helper()
	files := []animecatalogdomain.TorrentFile{
		{Ordinal: 0, SourcePath: "Show S1/Show - 01.mkv", Filename: "Show - 01.mkv", SizeBytes: 1234, MediaKind: animecatalogdomain.TorrentFileMediaVideo},
		{Ordinal: 1, SourcePath: "Show S1/Show - 01.zh-Hans.ass", Filename: "Show - 01.zh-Hans.ass", SizeBytes: 20, MediaKind: animecatalogdomain.TorrentFileMediaSubtitle, Language: "zh-Hans"},
	}
	for index := range files {
		files[index].FileRef = animecatalogdomain.TorrentFileRef(projectionIdentityPayload, files[index].SourcePath)
	}
	return &animecatalogdomain.TorrentManifest{PayloadDigest: projectionIdentityPayload, Files: files}
}

// TestOwnerFileRowsCarryThePayloadBoundIdentity is the owner-versus-preview
// parity the row contract promises: the same file of the same payload gets the
// same file_ref whichever mode projected it.
func TestOwnerFileRowsCarryThePayloadBoundIdentity(t *testing.T) {
	preview := projectionIdentityManifest(t)
	size := int64(1234)
	subtitleSize := int64(20)
	language := "zh-Hans"
	// The owner projection is rebuilt from the release's frozen facts, exactly
	// as release_torrent_inputs payload digest + internal_facts release_files
	// would rebuild it.
	owner := rssListOwnerManifest(preview.PayloadDigest, &[]ReleaseFile{
		{Path: "Show S1/Show - 01.mkv", Filename: "Show - 01.mkv", SizeBytes: &size, MediaKind: animecatalogdomain.TorrentFileMediaVideo},
		{Path: "Show S1/Show - 01.zh-Hans.ass", Filename: "Show - 01.zh-Hans.ass", SizeBytes: &subtitleSize, MediaKind: animecatalogdomain.TorrentFileMediaSubtitle, Language: &language},
	})
	if owner == nil {
		t.Fatal("an owner release with a payload and file facts must project a manifest")
	}

	for index, sourcePath := range []string{"Show S1/Show - 01.mkv", "Show S1/Show - 01.zh-Hans.ass"} {
		file := animedomain.PreviewFileResult{SourcePath: sourcePath}
		previewRow, ok := rssListFileRow(file, preview)
		if !ok {
			t.Fatalf("preview dropped %q", sourcePath)
		}
		ownerRow, ok := rssListFileRow(file, owner)
		if !ok {
			t.Fatalf("owner dropped %q", sourcePath)
		}
		if previewRow.FileRef != ownerRow.FileRef {
			t.Fatalf("file %d file_ref diverged: preview=%q owner=%q", index, previewRow.FileRef, ownerRow.FileRef)
		}
		if want := animecatalogdomain.TorrentFileRef(projectionIdentityPayload, sourcePath); ownerRow.FileRef != want {
			t.Fatalf("file %d file_ref = %q, want the payload-bound identity %q", index, ownerRow.FileRef, want)
		}
		if !strings.HasPrefix(ownerRow.FileRef, "sha256:") || len(ownerRow.FileRef) != len("sha256:")+64 {
			t.Fatalf("file %d file_ref does not match the contract pattern: %q", index, ownerRow.FileRef)
		}
		// The schema's own pattern, not a hand-rolled stand-in for it.
		if !fileRefPattern.MatchString(ownerRow.FileRef) {
			t.Fatalf("file %d file_ref does not match the openapi pattern: %q", index, ownerRow.FileRef)
		}
		if ownerRow.Ordinal != previewRow.Ordinal || ownerRow.Filename != previewRow.Filename ||
			ownerRow.MediaKind == nil || *ownerRow.MediaKind != *previewRow.MediaKind {
			t.Fatalf("owner projection lost manifest facts: owner=%+v preview=%+v", ownerRow, previewRow)
		}
	}
}

// A release without a stored payload digest cannot bind any file identity. The
// projection is then absent, so no row is published with an empty required
// file_ref: the row would violate the schema and the browser would key it by an
// empty string.
func TestOwnerManifestRequiresADerivablePayloadIdentity(t *testing.T) {
	size := int64(1024)
	releaseFiles := &[]ReleaseFile{{Path: "01.mkv", Filename: "01.mkv", SizeBytes: &size, MediaKind: animecatalogdomain.TorrentFileMediaVideo}}
	if manifest := rssListOwnerManifest("", releaseFiles); manifest != nil {
		t.Fatalf("a release without a payload digest must not project file identities: %+v", manifest)
	}
	if manifest := rssListOwnerManifest(projectionIdentityPayload, nil); manifest != nil {
		t.Fatalf("a release without file facts must not project a manifest: %+v", manifest)
	}
	empty := &[]ReleaseFile{}
	if manifest := rssListOwnerManifest(projectionIdentityPayload, empty); manifest != nil {
		t.Fatalf("an empty file inventory must not project a manifest: %+v", manifest)
	}
	// A file that names no path at all cannot get an identity. The shared
	// torrent projection falls back to the filename before refusing; the rebuild
	// does the same, and a file with neither yields no projection rather than a
	// row keyed by an empty source path.
	if manifest := rssListOwnerManifest(projectionIdentityPayload, &[]ReleaseFile{{Filename: "01.mkv"}}); manifest == nil ||
		manifest.Files[0].SourcePath != "01.mkv" || manifest.Files[0].FileRef != animecatalogdomain.TorrentFileRef(projectionIdentityPayload, "01.mkv") {
		t.Fatalf("a pathless file must take its filename: %+v", manifest)
	}
	if manifest := rssListOwnerManifest(projectionIdentityPayload, &[]ReleaseFile{{Filename: ""}}); manifest != nil {
		t.Fatalf("a file with neither path nor filename must not project a manifest: %+v", manifest)
	}
}
