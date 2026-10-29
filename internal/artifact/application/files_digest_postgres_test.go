package application

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
)

// The inspection summaries bind the committed files through one digest that
// Go computes before the commit and artifact_commit_inspection recomputes from
// the committed rows: both must agree byte for byte, including the bytewise
// file_ref order (upper before lower case, ASCII before multi-byte) and
// length-framed fields that would otherwise run together.
func TestInspectionFilesDigestMatchesTheDatabase(t *testing.T) {
	openArtifactTestDB(t)
	owner := ownerTestDB(t)
	cases := map[string][]artifactFilePlan{
		"one file": {
			{fileRef: "primary", relativePath: "Show/Show - 01.mkv", mediaKind: "video", sizeBytes: 1 << 40, fingerprint: []byte(`{"algorithm":"metadata","value":"meta:size=1099511627776;dev=64768;ino=1234567","size_bytes":1099511627776}`)},
		},
		"bytewise order and framing": {
			{fileRef: "b", relativePath: "Show/第2話.mkv", mediaKind: "video", sizeBytes: 12, fingerprint: []byte(`{"algorithm":"metadata","value":"meta:size=12","size_bytes":12}`)},
			{fileRef: "B", relativePath: "Show/b", mediaKind: "subtitle", sizeBytes: 1, fingerprint: []byte(`{"algorithm":"metadata","value":"meta:size=1","size_bytes":1}`)},
			{fileRef: "é", relativePath: "Show/c1", mediaKind: "other", sizeBytes: 0, fingerprint: []byte(`{"algorithm":"metadata","value":"meta:size=0","size_bytes":0}`)},
			{fileRef: "a", relativePath: "Show/c", mediaKind: "other", sizeBytes: 10, fingerprint: []byte(`{"algorithm":"metadata","value":"meta:size=10","size_bytes":10}`)},
		},
	}
	var many []artifactFilePlan
	for i := 0; i < 619; i++ {
		size := int64(i*7919 + 1)
		many = append(many, artifactFilePlan{
			fileRef:      fmt.Sprintf("sha256:%064x", i*104729),
			relativePath: fmt.Sprintf("Show Box/Disc %02d/Show - %03d.mkv", i/12+1, i),
			mediaKind:    "video",
			sizeBytes:    size,
			fingerprint:  []byte(fmt.Sprintf(`{"algorithm":"metadata","value":"meta:size=%d;dev=7;ino=%d","size_bytes":%d}`, size, i+1, size)),
		})
	}
	cases["619 files"] = many

	for name, files := range cases {
		t.Run(name, func(t *testing.T) {
			want, err := inspectionFilesDigest(files)
			if err != nil {
				t.Fatal(err)
			}
			rows := make([]map[string]any, 0, len(files))
			for _, file := range files {
				rows = append(rows, map[string]any{"file_ref": file.fileRef, "relative_path": file.relativePath, "media_kind": file.mediaKind, "size_bytes": file.sizeBytes, "fingerprint": json.RawMessage(file.fingerprint)})
			}
			body, err := json.Marshal(rows)
			if err != nil {
				t.Fatal(err)
			}
			var got string
			if err := owner.QueryRowContext(context.Background(), `SELECT public.artifact_files_digest($1::jsonb)`, string(body)).Scan(&got); err != nil {
				t.Fatal(err)
			}
			if got != want {
				t.Fatalf("database digest %s, Go digest %s", got, want)
			}
		})
	}
}
