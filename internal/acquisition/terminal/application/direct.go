package application

import (
	acq "bansoko/internal/acquisition/domain"
	"bansoko/internal/acquisition/torrent"
	catalog "bansoko/internal/catalog/domain"
	"bansoko/internal/catalog/projection"
	"context"
	"database/sql"
	"encoding/json"
)

type directMaterial struct {
	seed           catalog.ProviderFeedSnapshotSeed
	releaseVersion int64
}

func captureDirectTx(ctx context.Context, tx *sql.Tx, releaseID string) (*directMaterial, error) {
	var payload []byte
	var digest, parserVersion, version, filename, rawTitle, stable string
	var h1, h2 sql.NullString
	var raw []byte
	var releaseVersion int64
	err := tx.QueryRowContext(ctx, `SELECT ti.payload,ti.payload_digest,ti.parser_version,ti.torrent_version,ti.filename,ti.info_hash_v1,ti.info_hash_v2,r.raw_facts,r.stable_item_key,r.version FROM release_torrent_inputs ti JOIN releases r ON r.id=ti.release_id WHERE ti.release_id=$1`, releaseID).Scan(&payload, &digest, &parserVersion, &version, &filename, &h1, &h2, &raw, &stable, &releaseVersion)
	if err != nil {
		return nil, materialError("payload", err)
	}
	if catalog.DigestBytes(payload) != digest {
		return nil, materialError("payload_digest", nil)
	}
	meta, err := torrent.Parse(payload)
	if err != nil {
		return nil, materialError("inventory", err)
	}
	if parserVersion != "torrent-bencode-v1" || version != meta.TorrentVersion || h1.String != meta.InfoHashV1 || h2.String != meta.InfoHashV2 {
		return nil, materialError("torrent_identity", nil)
	}
	var facts struct {
		Title       string `json:"title"`
		DownloadURL string `json:"download_url"`
	}
	if err := json.Unmarshal(raw, &facts); err != nil {
		return nil, materialError("release_facts", err)
	}
	rawTitle = facts.Title
	if rawTitle == "" {
		return nil, materialError("release_facts", nil)
	}
	item := acq.FeedItem{Title: rawTitle, TorrentFilename: filename, TorrentVersion: meta.TorrentVersion, TorrentInfoHashV1: meta.InfoHashV1, TorrentInfoHashV2: meta.InfoHashV2}
	for _, f := range meta.Files {
		size := f.SizeBytes
		item.ReleaseFiles = append(item.ReleaseFiles, acq.ReleaseFile{SourcePath: f.Path, Filename: f.Name, SizeBytes: &size, MediaKind: f.MediaKind, Facts: map[string]any{"language": f.Language}})
	}
	manifest, err := projection.ManifestForItem(item, digest)
	if err != nil {
		return nil, materialError("inventory", err)
	}
	return &directMaterial{releaseVersion: releaseVersion, seed: catalog.ProviderFeedSnapshotSeed{StableSeedKey: stable, Title: rawTitle, Locator: facts.DownloadURL, LocatorDisplay: filename, Payload: append([]byte(nil), payload...), PayloadDigest: &digest, Manifest: manifest, TorrentState: "available"}}, nil
}
func admissionSeed(c admissionCapture) (*catalog.ProviderFeedSnapshotSeed, error) {
	if c.material.direct != nil {
		return &c.material.direct.seed, nil
	}
	if c.material.catalog != nil {
		for i := range c.material.catalog.feed.Seeds {
			s := &c.material.catalog.feed.Seeds[i]
			if s.StableSeedKey == c.material.catalog.seedKey {
				return s, nil
			}
		}
	}
	return nil, materialError("inventory", nil)
}
