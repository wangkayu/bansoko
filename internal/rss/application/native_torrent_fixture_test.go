package application

import (
	acquisitionapplication "bansoko/internal/acquisition/application"
	"bansoko/internal/acquisition/torrent"
	catalog "bansoko/internal/catalog/domain"
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
)

// Supply actual bytes and their parsed immutable identity to native capture.
func seedNativeTorrent(t *testing.T, db *sql.DB, id, release string) {
	seedNativeTorrentFile(t, db, id, release, "show-01.mkv", 1024, "show-01.torrent")
}

func nativeTorrentFile(path string, size int64) []byte {
	parts := strings.Split(path, "/")
	if len(parts) == 1 {
		return []byte(fmt.Sprintf("d4:infod6:lengthi%de4:name%d:%see", size, len(path), path))
	}
	var encoded strings.Builder
	for _, part := range parts {
		fmt.Fprintf(&encoded, "%d:%s", len(part), part)
	}
	return []byte(fmt.Sprintf("d4:infod5:filesld6:lengthi%de4:pathl%seee4:name7:Fixtureee", size, encoded.String()))
}

// seedNativeTorrentFile stores the release's torrent input and writes its
// manifest into the release's internal facts the way a feed fetch does.
func seedNativeTorrentFile(t *testing.T, db *sql.DB, id, release, path string, size int64, filename string) {
	t.Helper()
	payload, meta := seedTorrentManifest(t, db, release, path, size, filename)
	if _, err := db.ExecContext(context.Background(), `INSERT INTO release_torrent_inputs(id,release_id,payload,payload_digest,source_url_digest,filename,parser_version,torrent_version,info_hash_v1,payload_size_bytes) VALUES($1,$2,$3,$4,$5,$6,'torrent-bencode-v1',$7,$8,$9)`, id, release, payload, catalog.DigestBytes(payload), catalog.DigestBytes([]byte("fixture-locator")), filename, meta.TorrentVersion, meta.InfoHashV1, len(payload)); err != nil {
		t.Fatal(err)
	}
}

// seedTorrentManifest writes the torrent manifest (identity, total size and
// per-file facts) into the release's internal facts through the fetch writer
// ApplyTorrentMetadata, without storing the payload bytes: a metadata-only
// fetch leaves exactly this state.
func seedTorrentManifest(t *testing.T, db *sql.DB, release, path string, size int64, filename string) ([]byte, torrent.Metadata) {
	t.Helper()
	ctx := context.Background()
	payload := nativeTorrentFile(path, size)
	if _, err := db.ExecContext(ctx, `UPDATE releases SET raw_facts=jsonb_build_object('title',internal_facts->>'title','download_url',internal_facts->>'download_url') WHERE id=$1`, release); err != nil {
		t.Fatal(err)
	}
	meta, err := torrent.Parse(payload)
	if err != nil {
		t.Fatal(err)
	}
	if len(meta.Files) != 1 || meta.Files[0].Path != path || meta.Files[0].SizeBytes != size {
		t.Fatalf("fixture torrent differs from asserted file: %+v", meta.Files)
	}
	var rawFacts, internalFacts, sourceFacts []byte
	var stableKey string
	if err := db.QueryRowContext(ctx, `SELECT raw_facts, internal_facts, source_facts, stable_item_key FROM releases WHERE id=$1`, release).Scan(&rawFacts, &internalFacts, &sourceFacts, &stableKey); err != nil {
		t.Fatal(err)
	}
	item, err := decodeFeedItemWithInternal(rawFacts, internalFacts, sourceFacts, nil, stableKey)
	if err != nil {
		t.Fatal(err)
	}
	item.TorrentFilename = filename
	acquisitionapplication.ApplyTorrentMetadata(&item, meta)
	encoded, err := encodeInternalFeedFacts(item)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE releases SET internal_facts=$2::jsonb WHERE id=$1`, release, encoded); err != nil {
		t.Fatal(err)
	}
	return payload, meta
}
