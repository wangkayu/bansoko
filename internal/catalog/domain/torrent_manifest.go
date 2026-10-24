package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// TorrentManifestProjectionSchema is the canonical file-projection schema
// version. It is part of the projection digest so a future shape can never be
// mistaken for this one.
const TorrentManifestProjectionSchema = "torrent-manifest-v1"

// Torrent manifest bounds. They mirror the parser's own structural limits so a
// stored projection can never describe a payload the parser would refuse.
const (
	MaxTorrentManifestFiles       = 20000
	MaxTorrentManifestPathLength  = 2048
	MaxTorrentManifestNameLength  = 512
	MaxTorrentManifestLanguageLen = 64
	MaxTorrentParserVersionLength = 64
	MaxTorrentManifestSizeBytes   = int64(1125899906842624)
	MaxTorrentManifestTorrentName = 512
)

// Torrent acquisition states of one snapshot seed. A seed whose torrent could
// not be acquired stays visible in its group with an explicit reason instead of
// being dropped or silently rendered without structure.
const (
	TorrentAcquisitionAvailable     = "available"
	TorrentAcquisitionUnavailable   = "unavailable"
	TorrentAcquisitionNotApplicable = "not_applicable"
)

// Torrent file media kinds, matching the parser's classification.
const (
	TorrentFileMediaVideo    = "video"
	TorrentFileMediaSubtitle = "subtitle"
	TorrentFileMediaOther    = "other"
)

// TorrentFile is one projected file of an exact bound torrent payload.
//
// FileRef is the identity a per-file decision binds to. It is derived from the
// payload digest and the canonical source path, so it stays stable across
// re-parses of the same bytes and cannot be confused with a same-named file in
// another torrent. Ordinal is display order only and is never an identity.
type TorrentFile struct {
	FileRef    string
	Ordinal    int
	SourcePath string
	Filename   string
	SizeBytes  int64
	MediaKind  string
	Language   string
}

// TorrentManifest is the immutable projection of one exact torrent payload.
// It is derived, never authoritative on its own: the payload digest binds it to
// the stored bytes, and the projection digest covers the file set it declares.
type TorrentManifest struct {
	PayloadDigest    string
	ParserVersion    string
	TorrentVersion   string
	TorrentName      string
	InfoHashV1       string
	InfoHashV2       string
	TotalSizeBytes   int64
	ProjectionDigest string
	Files            []TorrentFile
}

// FileCount is the number of projected files.
func (m TorrentManifest) FileCount() int { return len(m.Files) }

// TorrentFileRef derives the stable identity of one file inside an exact
// payload. It deliberately includes a schema tag so a future derivation change
// cannot silently collide with this one.
func TorrentFileRef(payloadDigest, sourcePath string) string {
	sum := sha256.Sum256([]byte("torrent-file-v1\x00" + payloadDigest + "\x00" + sourcePath))
	return "sha256:" + hex.EncodeToString(sum[:])
}

type canonicalTorrentFile struct {
	FileRef    string `json:"file_ref"`
	SourcePath string `json:"source_path"`
	Filename   string `json:"filename"`
	SizeBytes  int64  `json:"size_bytes"`
	MediaKind  string `json:"media_kind"`
	Language   string `json:"language,omitempty"`
}

type canonicalTorrentProjection struct {
	Schema        string                 `json:"schema"`
	PayloadDigest string                 `json:"payload_digest"`
	ParserVersion string                 `json:"parser_version"`
	Files         []canonicalTorrentFile `json:"files"`
}

// CanonicalTorrentProjectionDigest computes the digest of one manifest's file
// projection. Files are ordered by ordinal, which is their canonical order
// inside the payload.
func CanonicalTorrentProjectionDigest(payloadDigest, parserVersion string, files []TorrentFile) (string, error) {
	ordered := make([]TorrentFile, len(files))
	copy(ordered, files)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Ordinal < ordered[j].Ordinal })
	canonical := make([]canonicalTorrentFile, 0, len(ordered))
	for index, file := range ordered {
		if file.Ordinal != index {
			return "", fmt.Errorf("torrent projection file %d has a non-canonical ordinal %d", index, file.Ordinal)
		}
		canonical = append(canonical, canonicalTorrentFile{
			FileRef: file.FileRef, SourcePath: file.SourcePath, Filename: file.Filename,
			SizeBytes: file.SizeBytes, MediaKind: file.MediaKind, Language: file.Language,
		})
	}
	encoded, err := json.Marshal(canonicalTorrentProjection{
		Schema: TorrentManifestProjectionSchema, PayloadDigest: payloadDigest,
		ParserVersion: parserVersion, Files: canonical,
	})
	if err != nil {
		return "", fmt.Errorf("marshal canonical torrent projection: %w", err)
	}
	return DigestBytes(encoded), nil
}

// Validate rejects a manifest that cannot be an exact derivation of a bound
// payload: bad digests, unsafe or duplicated paths, non-canonical ordinals, an
// inconsistent total size or a projection digest that does not cover the files.
func (m TorrentManifest) Validate() error {
	if err := validateSHA256Digest(m.PayloadDigest); err != nil {
		return fmt.Errorf("torrent manifest payload digest: %w", err)
	}
	version := strings.TrimSpace(m.ParserVersion)
	if version == "" || len(version) > MaxTorrentParserVersionLength {
		return errors.New("torrent manifest parser version is required and bounded")
	}
	switch m.TorrentVersion {
	case "v1", "v2", "hybrid":
	default:
		return fmt.Errorf("unsupported torrent version %q", m.TorrentVersion)
	}
	name := strings.TrimSpace(m.TorrentName)
	if name == "" || len(name) > MaxTorrentManifestTorrentName {
		return errors.New("torrent manifest name is required and bounded")
	}
	if m.InfoHashV1 == "" && m.InfoHashV2 == "" {
		return errors.New("torrent manifest requires at least one info hash")
	}
	if m.InfoHashV1 != "" {
		if len(m.InfoHashV1) != 40 {
			return errors.New("torrent manifest v1 info hash must be 40 hexadecimal characters")
		}
		if err := validateInfoHash(m.InfoHashV1); err != nil {
			return fmt.Errorf("torrent manifest v1 info hash: %w", err)
		}
	}
	if m.InfoHashV2 != "" {
		if len(m.InfoHashV2) != 64 {
			return errors.New("torrent manifest v2 info hash must be 64 hexadecimal characters")
		}
		if err := validateInfoHash(m.InfoHashV2); err != nil {
			return fmt.Errorf("torrent manifest v2 info hash: %w", err)
		}
	}
	if len(m.Files) == 0 {
		return errors.New("torrent manifest must project at least one file")
	}
	if len(m.Files) > MaxTorrentManifestFiles {
		return fmt.Errorf("torrent manifest projects too many files: %d", len(m.Files))
	}
	seenRef := make(map[string]struct{}, len(m.Files))
	seenPath := make(map[string]struct{}, len(m.Files))
	var total int64
	for index, file := range m.Files {
		if file.Ordinal != index {
			return fmt.Errorf("torrent manifest file %d has a non-canonical ordinal %d", index, file.Ordinal)
		}
		if err := validateSHA256Digest(file.FileRef); err != nil {
			return fmt.Errorf("torrent manifest file %d reference: %w", index, err)
		}
		if want := TorrentFileRef(m.PayloadDigest, file.SourcePath); want != file.FileRef {
			return fmt.Errorf("torrent manifest file %d reference does not match its payload and path", index)
		}
		if _, duplicate := seenRef[file.FileRef]; duplicate {
			return fmt.Errorf("torrent manifest file reference is duplicated: %q", file.FileRef)
		}
		seenRef[file.FileRef] = struct{}{}
		if !isSafeManifestPath(file.SourcePath) {
			return fmt.Errorf("torrent manifest file %d has an unsafe path", index)
		}
		if _, duplicate := seenPath[file.SourcePath]; duplicate {
			return fmt.Errorf("torrent manifest source path is duplicated: %q", file.SourcePath)
		}
		seenPath[file.SourcePath] = struct{}{}
		filename := strings.TrimSpace(file.Filename)
		if filename == "" || len(filename) > MaxTorrentManifestNameLength {
			return fmt.Errorf("torrent manifest file %d name is required and bounded", index)
		}
		if file.SizeBytes < 0 || file.SizeBytes > MaxTorrentManifestSizeBytes {
			return fmt.Errorf("torrent manifest file %d size is outside the allowed bound", index)
		}
		switch file.MediaKind {
		case TorrentFileMediaVideo, TorrentFileMediaSubtitle, TorrentFileMediaOther:
		default:
			return fmt.Errorf("torrent manifest file %d has an unsupported media kind %q", index, file.MediaKind)
		}
		if len(file.Language) > MaxTorrentManifestLanguageLen {
			return fmt.Errorf("torrent manifest file %d language is not bounded", index)
		}
		total += file.SizeBytes
	}
	if total != m.TotalSizeBytes {
		return fmt.Errorf("torrent manifest total size %d does not equal its file sizes %d", m.TotalSizeBytes, total)
	}
	if err := validateSHA256Digest(m.ProjectionDigest); err != nil {
		return fmt.Errorf("torrent manifest projection digest: %w", err)
	}
	recomputed, err := CanonicalTorrentProjectionDigest(m.PayloadDigest, m.ParserVersion, m.Files)
	if err != nil {
		return err
	}
	if recomputed != m.ProjectionDigest {
		return errors.New("torrent manifest projection digest does not cover its declared files")
	}
	return nil
}

// isSafeManifestPath accepts only a non-empty canonical relative path with no
// traversal, absolute prefix, backslash or control character.
func isSafeManifestPath(value string) bool {
	if value == "" || len(value) > MaxTorrentManifestPathLength {
		return false
	}
	if strings.TrimSpace(value) != value {
		return false
	}
	if strings.HasPrefix(value, "/") || strings.Contains(value, `\`) {
		return false
	}
	for _, part := range strings.Split(value, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return !strings.ContainsAny(value, "\x00\n\r")
}
