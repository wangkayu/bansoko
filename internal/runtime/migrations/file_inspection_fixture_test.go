package migrations

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"
)

// metadataFingerprintFixture is the path-level artifact file fingerprint
// inspection records for a file of size bytes without an identity.
func metadataFingerprintFixture(size any) map[string]any {
	n := fixtureSize(size)
	return map[string]any{"algorithm": "metadata", "value": fmt.Sprintf("meta:size=%d", n), "size_bytes": n}
}

// fixtureSize reads a JSON-decoded or literal size as an integer.
func fixtureSize(size any) int64 {
	switch v := size.(type) {
	case float64:
		return int64(v)
	case int:
		return int64(v)
	case int64:
		return v
	case json.Number:
		n, _ := v.Int64()
		return n
	}
	panic(fmt.Sprintf("fixture size %T", size))
}

// inspectionSnapshotFixture builds the v2 inspection summaries for files the
// way the inspect handler does: counts and a digest over every file, never
// the file list.
func inspectionSnapshotFixture(t *testing.T, files []map[string]any) (string, string) {
	t.Helper()
	counts := map[string]int{}
	var total int64
	for _, file := range files {
		counts[fmt.Sprint(file["media_kind"])]++
		total += fixtureSize(file["size_bytes"])
	}
	digest := inspectionFilesDigestFixture(t, files)
	primary := files[0]["file_ref"]
	inventory := map[string]any{
		"schema_version": "artifact-inspect/v2", "primary_file_ref": primary,
		"file_count": len(files), "video_count": counts["video"], "subtitle_count": counts["subtitle"], "other_count": len(files) - counts["video"] - counts["subtitle"],
		"total_size_bytes": total, "files_digest": digest,
	}
	fp := map[string]any{"schema_version": "artifact-fingerprint/v2", "primary_file_ref": primary, "file_count": len(files), "files_digest": digest}
	a, err := json.Marshal(inventory)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(fp)
	if err != nil {
		t.Fatal(err)
	}
	return string(a), string(b)
}

// inspectionFilesDigestFixture restates the artifact-files:v1 digest
// independently of the Go inspection code, so these commit tests also pin the
// SQL implementation.
func inspectionFilesDigestFixture(t *testing.T, files []map[string]any) string {
	t.Helper()
	sorted := append([]map[string]any(nil), files...)
	sort.SliceStable(sorted, func(i, j int) bool {
		return fmt.Sprint(sorted[i]["file_ref"]) < fmt.Sprint(sorted[j]["file_ref"])
	})
	var b strings.Builder
	b.WriteString("artifact-files:v1\x00")
	for _, file := range sorted {
		fingerprint, _ := file["fingerprint"].(map[string]any)
		for _, value := range []string{fmt.Sprint(file["file_ref"]), fmt.Sprint(file["relative_path"]), fmt.Sprint(file["media_kind"]), fmt.Sprint(fixtureSize(file["size_bytes"])), fmt.Sprint(fingerprint["value"])} {
			fmt.Fprintf(&b, "%d:%s", len(value), value)
		}
	}
	sum := sha256.Sum256([]byte(b.String()))
	return "sha256:" + hex.EncodeToString(sum[:])
}
