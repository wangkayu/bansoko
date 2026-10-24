package application

import (
	"context"
	"testing"
)

// A nil DB transaction is intentional: this invokes the shared production
// classifier/video/subtitle builder and catches accidental DB calls in build.
func TestPreparedBatchBuildHasNoDatabaseTransaction(t *testing.T) {
	s := &Service{}
	b := &artifactPreparedBatch{artifact: artifactContext{artifactID: "artifact", artifactVersion: 1, files: []artifactFileContext{{id: "video", fileRef: "video", mediaKind: "video", relativePath: "episode.mkv", sourcePath: "/source/episode.mkv", fingerprint: []byte(`{}`)}, {id: "subtitle", fileRef: "subtitle", mediaKind: "subtitle", relativePath: "episode.ass", sourcePath: "/source/episode.ass", fingerprint: []byte(`{}`)}}}, targets: map[string]frozenTargetRecord{}, existing: map[string]operationRecord{}}
	// Invalid frozen policy becomes a fully computed blocked plan; it must not
	// read current globals, evaluate active Vars, or enter a transaction.
	b.prep.configErr = ErrInvalidTarget
	clone := *s
	clone.preparedBatch = b
	if err := clone.prepareForArtifactTxWithOverrides(context.Background(), nil, "artifact", nil, nil); err != nil {
		t.Fatal(err)
	}
	if len(b.operations) != 2 {
		t.Fatalf("blocked video/subtitle plans=%d", len(b.operations))
	}
	for _, op := range b.operations {
		if op.plan.state != ownerStateFailed || op.plan.targetPath == "" {
			t.Fatal("builder did not freeze blocked payload")
		}
	}
}
