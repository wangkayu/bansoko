package application

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
)

const (
	// inventorySchemaVersion and fingerprintSchemaVersion name the summary
	// snapshots an inspection commits. They hold counts and a digest of the
	// inspected file set, never the file list: per-file data lives in
	// artifact_files rows, so the snapshots stay small for any file count.
	inventorySchemaVersion   = "artifact-inspect/v2"
	fingerprintSchemaVersion = "artifact-fingerprint/v2"

	// fingerprintAlgorithmMetadata is the only artifact file fingerprint
	// form: {"algorithm":"metadata","value":"meta:size=<n>[;dev=<d>;ino=<i>]",
	// "size_bytes":<n>}.
	fingerprintAlgorithmMetadata = "metadata"

	// filesDigestDomain separates the file-set digest from every other
	// digest. public.artifact_files_digest (SQL) computes the same value.
	filesDigestDomain = "artifact-files:v1"
)

// inventoryListKeys are file-list keys a pending provider snapshot (or an
// older summary) may carry; the committed summary never keeps them.
var inventoryListKeys = []string{"files", "inspected_files", "inspected_primary_file"}

// inspectionFilesDigest digests the bound file set: files ordered by file_ref
// (bytewise), each contributing its file_ref, relative_path, media_kind,
// size_bytes and fingerprint value, every field written as
// "<byte length>:<value>". The SQL commit recomputes it from the submitted
// rows (public.artifact_files_digest).
func inspectionFilesDigest(files []artifactFilePlan) (string, error) {
	sorted := append([]artifactFilePlan(nil), files...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].fileRef < sorted[j].fileRef })
	sum := sha256.New()
	sum.Write([]byte(filesDigestDomain))
	sum.Write([]byte{0})
	for _, file := range sorted {
		var fingerprint struct {
			Value string `json:"value"`
		}
		if err := json.Unmarshal(file.fingerprint, &fingerprint); err != nil || fingerprint.Value == "" {
			return "", errors.New("inspected file fingerprint has no value")
		}
		for _, field := range []string{file.fileRef, file.relativePath, file.mediaKind, strconv.FormatInt(file.sizeBytes, 10), fingerprint.Value} {
			fmt.Fprintf(sum, "%d:%s", len(field), field)
		}
	}
	return "sha256:" + hex.EncodeToString(sum.Sum(nil)), nil
}

// inspectionSnapshots completes the inventory and fingerprint summaries for
// the bound file set. primaryRef is the primary file's bound reference.
func inspectionSnapshots(base map[string]any, files []artifactFilePlan, primaryRef string) ([]byte, []byte, error) {
	if len(files) == 0 || primaryRef == "" {
		return nil, nil, errors.New("inspection summary needs files and a primary reference")
	}
	digest, err := inspectionFilesDigest(files)
	if err != nil {
		return nil, nil, err
	}
	var videos, subtitles, others int
	var total int64
	for _, file := range files {
		switch file.mediaKind {
		case "video":
			videos++
		case "subtitle":
			subtitles++
		default:
			others++
		}
		total += file.sizeBytes
	}
	inventory := make(map[string]any, len(base)+9)
	for key, value := range base {
		inventory[key] = value
	}
	for _, key := range inventoryListKeys {
		delete(inventory, key)
	}
	inventory["schema_version"] = inventorySchemaVersion
	inventory["primary_file_ref"] = primaryRef
	inventory["file_count"] = len(files)
	inventory["video_count"] = videos
	inventory["subtitle_count"] = subtitles
	inventory["other_count"] = others
	inventory["total_size_bytes"] = total
	inventory["files_digest"] = digest
	inventoryRaw, err := marshalObject(inventory)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal artifact inventory: %w", err)
	}
	fingerprintsRaw, err := marshalObject(map[string]any{
		"schema_version":   fingerprintSchemaVersion,
		"primary_file_ref": primaryRef,
		"file_count":       len(files),
		"files_digest":     digest,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("marshal artifact fingerprint snapshot: %w", err)
	}
	return inventoryRaw, fingerprintsRaw, nil
}
