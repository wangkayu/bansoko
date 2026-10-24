package application

import (
	"database/sql"
	"encoding/json"
	"strings"
	"testing"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	"bansoko/internal/redact"
)

func TestPublicStableItemKeyRedactsURLFallback(t *testing.T) {
	kind, value := publicStableItemKey(acquisitiondomain.FeedItem{
		Title:     "[Group] Show - 01",
		Enclosure: "https://feeds.example/rss.torrent?passkey=secret-token",
	})
	if kind != "enclosure" {
		t.Fatalf("kind = %q, want enclosure", kind)
	}
	if strings.Contains(value, "secret-token") || !strings.HasPrefix(value, "sha256:") {
		t.Fatalf("stable key leaked URL or was not digested: %q", value)
	}
}

func TestSafeReasonRedactsHTTPAndMagnetURLs(t *testing.T) {
	reason := safeReason("fetch https://user:pw@example.test/feed.torrent?passkey=secret failed magnet:?xt=urn:btih:abc&passkey=secret")
	for _, secret := range []string{"user:pw", "secret", "urn:btih:abc"} {
		if strings.Contains(reason, secret) {
			t.Fatalf("reason leaked %q: %s", secret, reason)
		}
	}
	if reason != redact.Redacted {
		t.Fatalf("reason = %s, want shared redaction marker %q", reason, redact.Redacted)
	}
}

func TestDecodeFeedItemUsesStoredRawFactsAndDoesNotTreatDigestAsURL(t *testing.T) {
	raw := []byte(`{"title":"[Group] Show - 01"}`)
	source := []byte(`{"identity_kind":"enclosure","identity_value":"sha256:abc","group_name":"Group"}`)
	item, err := decodeFeedItem(raw, source, nil, "content-hash")
	if err != nil {
		t.Fatal(err)
	}
	if item.Title == "" || item.DownloadURL() != "" {
		t.Fatalf("digest fallback must not become a downloadable URL: %+v", item)
	}
}

func TestInternalFactsRoundTripTargetProjection(t *testing.T) {
	targetPath := "/library/Show/Show - 01.mkv"
	item := acquisitiondomain.FeedItem{
		Title: "[Group] Show - 01",
		ReleaseFiles: []acquisitiondomain.ReleaseFile{{
			SourcePath: "Show/Show - 01.mkv",
			Filename:   "Show - 01.mkv",
			MediaKind:  "video",
			TargetPath: &targetPath,
		}},
	}
	encoded, err := encodeInternalFeedFacts(item)
	if err != nil {
		t.Fatal(err)
	}
	var stored storedFeedFacts
	if err := json.Unmarshal(encoded, &stored); err != nil {
		t.Fatal(err)
	}
	if len(stored.ReleaseFiles) != 1 || stored.ReleaseFiles[0].TargetPath == nil || *stored.ReleaseFiles[0].TargetPath != targetPath {
		t.Fatalf("target projection was not encoded: %+v", stored.ReleaseFiles)
	}

	item.ReleaseFiles[0].TargetPath = nil
	item.ReleaseFiles[0].TargetDiagnostic = &acquisitiondomain.TargetDiagnostic{
		Code: "target_render_failed", Message: "目标路径生成失败，未执行重命名", SourcePath: "Show/Show - 01.mkv",
	}
	encoded, err = encodeInternalFeedFacts(item)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), "target_render_failed") {
		t.Fatalf("target diagnostic was not encoded: %s", encoded)
	}
}

func TestFeedFactsKeepDownloadURLOnlyInInternalFacts(t *testing.T) {
	item := acquisitiondomain.FeedItem{
		Title:     "[Group] Show - 01",
		Enclosure: "https://downloads.example/show-01.torrent?passkey=secret",
	}
	publicFacts, err := encodeFeedFacts(item)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(publicFacts), "downloads.example") || strings.Contains(string(publicFacts), "secret") {
		t.Fatalf("public facts leaked download URL: %s", publicFacts)
	}
	internalFacts, err := encodeInternalFeedFacts(item)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(internalFacts), "show-01.torrent") || !strings.Contains(string(internalFacts), "secret") {
		t.Fatalf("internal facts did not preserve execution URL: %s", internalFacts)
	}
}

func TestBoundedRedactedTextKeepsUnicodeValid(t *testing.T) {
	value := boundedRedactedText("这是一个很长的标题", 5)
	if value != "这是一个很" {
		t.Fatalf("bounded value = %q, want five runes", value)
	}
	if !json.Valid([]byte(`"` + value + `"`)) {
		t.Fatalf("bounded value is not valid UTF-8 JSON: %q", value)
	}
}

func TestMetadataBindingsRejectPartialTMDB(t *testing.T) {
	_, _, err := metadataBindingsFromColumns(
		sql.NullString{},
		sql.NullString{String: "tmdb-1", Valid: true},
		sql.NullString{},
		sql.NullInt64{},
		"anime-1",
	)
	if err == nil || !strings.Contains(err.Error(), "TMDB binding") {
		t.Fatalf("partial TMDB error = %v", err)
	}
}

func TestRefreshFactsPreservesLastGoodFactsForEmptyFeed(t *testing.T) {
	cursorJSON, err := json.Marshal(rssCursor{Strategy: rssCursorStrategyGuid, Value: "guid-old"})
	if err != nil {
		t.Fatal(err)
	}
	lastGoodJSON, err := json.Marshal(rssLastGoodFacts{ItemCount: 7, NewestStableItemKey: "guid-old"})
	if err != nil {
		t.Fatal(err)
	}
	cursor, lastGood := refreshFacts(nil, sourceSnapshot{cursorJSON: cursorJSON, lastGoodJSON: lastGoodJSON})
	if cursor.Strategy != rssCursorStrategyGuid || cursor.Value != "guid-old" {
		t.Fatalf("empty feed changed cursor: %+v", cursor)
	}
	if lastGood.ItemCount != 7 || lastGood.NewestStableItemKey != "guid-old" {
		t.Fatalf("empty feed changed last-good facts: %+v", lastGood)
	}
}

func TestReuseVerifiedTorrentMetadataPreservesCurrentErrorAndRejectsConflicts(t *testing.T) {
	v1Hash := strings.Repeat("a", 40)
	size := int64(123)
	oldFacts, err := json.Marshal(storedFeedFacts{
		Title: "Show - 01", TorrentInfoHash: v1Hash, TorrentInfoHashV1: v1Hash,
		TorrentVersion: "v1", TorrentFilename: "show-01.torrent", TorrentSizeBytes: 123,
		ReleaseFiles: []storedReleaseFile{{SourcePath: "Show/show-01.mkv", Filename: "show-01.mkv", SizeBytes: &size}},
	})
	if err != nil {
		t.Fatal(err)
	}
	input := releaseTorrentInputRef{
		ID: "input-1", PayloadDigest: "sha256:" + strings.Repeat("b", 64), SourceURLDigest: stableDigest("https://example.test/show-01.torrent"),
		Filename: "show-01.torrent", ParserVersion: torrentInputParserVersion, TorrentVersion: "v1", InfoHashV1: v1Hash,
	}
	current := acquisitiondomain.FeedItem{
		Title: "Show - 01", TorrentURL: "https://example.test/show-01.torrent",
		TorrentErrorCode: "torrent_metadata_unavailable", TorrentErrorReason: "temporary failure",
	}
	reused, ok := reuseVerifiedTorrentMetadata(oldFacts, current, input)
	if !ok || reused.TorrentInfoHashV1 != v1Hash || len(reused.ReleaseFiles) != 1 || reused.TorrentErrorCode != "torrent_metadata_unavailable" || reused.TorrentErrorReason != "temporary failure" {
		t.Fatalf("reuse result=%+v ok=%v", reused, ok)
	}
	conflict := current
	conflict.TorrentInfoHashV1 = strings.Repeat("c", 40)
	if _, ok := reuseVerifiedTorrentMetadata(oldFacts, conflict, input); ok {
		t.Fatal("explicit torrent identity conflict reused old metadata")
	}
	missingSource := current
	missingSource.TorrentURL = ""
	missingSource.Enclosure = ""
	if _, ok := reuseVerifiedTorrentMetadata(oldFacts, missingSource, input); ok {
		t.Fatal("missing current torrent source reused old metadata")
	}
}

func TestReuseVerifiedTorrentMetadataRejectsConflictingPrimaryHash(t *testing.T) {
	v1Hash := strings.Repeat("a", 40)
	size := int64(123)
	oldFacts, err := json.Marshal(storedFeedFacts{
		TorrentInfoHash: strings.Repeat("b", 40), TorrentInfoHashV1: v1Hash, TorrentVersion: "v1", TorrentFilename: "show-01.torrent", TorrentSizeBytes: 123,
		ReleaseFiles: []storedReleaseFile{{SourcePath: "Show/show-01.mkv", Filename: "show-01.mkv", SizeBytes: &size}},
	})
	if err != nil {
		t.Fatal(err)
	}
	input := releaseTorrentInputRef{ID: "input-1", PayloadDigest: "sha256:" + strings.Repeat("c", 64), SourceURLDigest: stableDigest("https://example.test/show-01.torrent"), Filename: "show-01.torrent", ParserVersion: torrentInputParserVersion, TorrentVersion: "v1", InfoHashV1: v1Hash}
	current := acquisitiondomain.FeedItem{TorrentURL: "https://example.test/show-01.torrent", TorrentErrorCode: "torrent_metadata_unavailable"}
	if _, ok := reuseVerifiedTorrentMetadata(oldFacts, current, input); ok {
		t.Fatal("inconsistent persisted primary hash reused old metadata")
	}
}

func TestReuseVerifiedTorrentMetadataRequiresCompleteOldFacts(t *testing.T) {
	oldFacts, err := json.Marshal(storedFeedFacts{Title: "Show - 01", TorrentVersion: "v1", TorrentFilename: "show-01.torrent"})
	if err != nil {
		t.Fatal(err)
	}
	input := releaseTorrentInputRef{ID: "input-1", PayloadDigest: "sha256:" + strings.Repeat("a", 64), SourceURLDigest: stableDigest("https://example.test/show-01.torrent"), Filename: "show-01.torrent", ParserVersion: torrentInputParserVersion, TorrentVersion: "v1", InfoHashV1: strings.Repeat("b", 40)}
	current := acquisitiondomain.FeedItem{TorrentURL: "https://example.test/show-01.torrent", TorrentErrorCode: "torrent_metadata_unavailable"}
	if _, ok := reuseVerifiedTorrentMetadata(oldFacts, current, input); ok {
		t.Fatal("incomplete old metadata was reused")
	}
}

func TestReuseVerifiedTorrentMetadataReplacesWholeGroup(t *testing.T) {
	oldHash := strings.Repeat("a", 40)
	newHash := strings.Repeat("b", 40)
	oldSize := int64(123)
	oldFacts, err := json.Marshal(storedFeedFacts{
		TorrentInfoHash: oldHash, TorrentInfoHashV1: oldHash, TorrentVersion: "v1", TorrentFilename: "show-a.torrent", TorrentSizeBytes: 123,
		ReleaseFiles: []storedReleaseFile{{SourcePath: "Show/a.mkv", Filename: "a.mkv", SizeBytes: &oldSize}},
	})
	if err != nil {
		t.Fatal(err)
	}
	input := releaseTorrentInputRef{ID: "input-b", PayloadDigest: "sha256:" + strings.Repeat("c", 64), SourceURLDigest: stableDigest("https://example.test/show-b.torrent"), Filename: "show-b.torrent", ParserVersion: torrentInputParserVersion, TorrentVersion: "v1", InfoHashV1: newHash}
	current := acquisitiondomain.FeedItem{TorrentURL: "https://example.test/show-b.torrent", TorrentErrorCode: "torrent_metadata_unavailable"}
	if _, ok := reuseVerifiedTorrentMetadata(oldFacts, current, input); ok {
		t.Fatal("old metadata with a conflicting persisted input identity was reused")
	}
}

func TestVerifiedTorrentMetadataSupportsV1V2AndHybrid(t *testing.T) {
	tests := []struct {
		name      string
		version   string
		primary   string
		v1        string
		v2        string
		wantValid bool
	}{
		{name: "v1", version: "v1", primary: strings.Repeat("a", 40), v1: strings.Repeat("a", 40), wantValid: true},
		{name: "v2", version: "v2", primary: strings.Repeat("b", 64), v2: strings.Repeat("b", 64), wantValid: true},
		{name: "hybrid", version: "hybrid", primary: strings.Repeat("c", 40), v1: strings.Repeat("c", 40), v2: strings.Repeat("d", 64), wantValid: true},
		{name: "v1 with v2", version: "v1", primary: strings.Repeat("a", 40), v1: strings.Repeat("a", 40), v2: strings.Repeat("b", 64)},
		{name: "hybrid primary v2", version: "hybrid", primary: strings.Repeat("d", 64), v1: strings.Repeat("c", 40), v2: strings.Repeat("d", 64)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			size := int64(1)
			_, ok := (storedFeedFacts{
				TorrentInfoHash: test.primary, TorrentInfoHashV1: test.v1, TorrentInfoHashV2: test.v2,
				TorrentVersion: test.version, TorrentFilename: "show.torrent", TorrentSizeBytes: 1,
				ReleaseFiles: []storedReleaseFile{{SourcePath: "show.mkv", Filename: "show.mkv", SizeBytes: &size}},
			}).verifiedTorrentMetadata()
			if ok != test.wantValid {
				t.Fatalf("verified metadata valid=%v, want %v", ok, test.wantValid)
			}
		})
	}
}
