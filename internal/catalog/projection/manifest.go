// Package projection owns the shared immutable torrent manifest projection.
package projection

import (
	acquisitiondomain "bansoko/internal/acquisition/domain"
	torrentparser "bansoko/internal/acquisition/torrent"
	catalogdomain "bansoko/internal/catalog/domain"
	"errors"
	"strings"
)

func ManifestForItem(item acquisitiondomain.FeedItem, payloadDigest string) (*catalogdomain.TorrentManifest, error) {
	if len(item.ReleaseFiles) == 0 {
		return nil, errors.New("an available torrent seed has no parsed file tree")
	}
	files := make([]catalogdomain.TorrentFile, 0, len(item.ReleaseFiles))
	var total int64
	for ordinal, file := range item.ReleaseFiles {
		path := strings.TrimSpace(file.SourcePath)
		if path == "" {
			path = strings.TrimSpace(file.Filename)
		}
		if path == "" {
			return nil, errors.New("a projected torrent file has no path")
		}
		size := int64(0)
		if file.SizeBytes != nil {
			size = *file.SizeBytes
		}
		files = append(files, catalogdomain.TorrentFile{
			FileRef:    catalogdomain.TorrentFileRef(payloadDigest, path),
			Ordinal:    ordinal,
			SourcePath: path,
			Filename:   strings.TrimSpace(file.Filename),
			SizeBytes:  size,
			MediaKind:  file.MediaKind,
			Language:   releaseFileLanguage(file),
		})
		total += size
	}
	projectionDigest, err := catalogdomain.CanonicalTorrentProjectionDigest(payloadDigest, torrentparser.ImplementationVersion, files)
	if err != nil {
		return nil, err
	}
	manifest := &catalogdomain.TorrentManifest{
		PayloadDigest:    payloadDigest,
		ParserVersion:    torrentparser.ImplementationVersion,
		TorrentVersion:   strings.TrimSpace(item.TorrentVersion),
		TorrentName:      strings.TrimSpace(item.TorrentFilename),
		TotalSizeBytes:   total,
		ProjectionDigest: projectionDigest,
		Files:            files,
	}
	if hash := strings.ToLower(strings.TrimSpace(item.TorrentInfoHashV1)); hash != "" {
		manifest.InfoHashV1 = hash
	}
	if hash := strings.ToLower(strings.TrimSpace(item.TorrentInfoHashV2)); hash != "" {
		manifest.InfoHashV2 = hash
	}
	if manifest.TorrentName == "" {
		return nil, errors.New("a projected torrent manifest requires its torrent name")
	}
	return manifest, nil
}

// releaseFileLanguage reads the parser's normalized subtitle language off the
// file facts the shared torrent enricher recorded.
func releaseFileLanguage(file acquisitiondomain.ReleaseFile) string {
	if file.Facts == nil {
		return ""
	}
	value, ok := file.Facts["language"].(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(value)
}
