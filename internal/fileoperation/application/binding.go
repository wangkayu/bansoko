package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"bansoko/internal/filekinds"
	"bansoko/internal/postprocessing/naming"
)

// Subtitle binding states persisted on artifact_files (migration 013).
const (
	bindingStateNotApplicable = "not_applicable"
	bindingStateBound         = "bound"
	bindingStateMissing       = "missing"
	bindingStateAmbiguous     = "ambiguous"
)

// Binder decision methods (frozen into the binding snapshot and the API). The
// product binds by one rule only: the subtitle filename starts with the video
// filename without its extension plus a dot.
const bindingMethodBasenameMatch = "basename_match"

// The wire and database vocabulary still names two retired methods. The binder
// never emits them; the wire validators accept them only so a stored snapshot
// stays readable.
const (
	bindingMethodEpisodeMatch = "episode_match"
	bindingMethodUniqueVideo  = "unique_video"
)

// bindingSnapshotVersion identifies the binding snapshot JSON shape.
const bindingSnapshotVersion = "subtitle-binding-1"

// bindingSnapshot is the persisted binder decision plus the locked video
// target segments once the subtitle operation has been planned.
type bindingSnapshot struct {
	Version         string                    `json:"version"`
	Method          string                    `json:"method,omitempty"`
	Confidence      float64                   `json:"confidence,omitempty"`
	ExactConfidence json.RawMessage           `json:"-"`
	Diagnostics     []naming.NamingDiagnostic `json:"diagnostics"`
	Locked          *bindingLockedTarget      `json:"locked,omitempty"`
}

func (s bindingSnapshot) MarshalJSON() ([]byte, error) {
	type plain bindingSnapshot
	raw, err := json.Marshal(plain(s))
	if err != nil {
		return nil, err
	}
	if len(s.ExactConfidence) == 0 {
		return raw, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	fields["confidence"] = s.ExactConfidence
	return json.Marshal(fields)
}

// bindingLockedTarget freezes the bound video output the subtitle target was
// composed from. Any later drift against the video operation's frozen target
// is BOUND_TARGET_MISMATCH, never a silent rebind.
type bindingLockedTarget struct {
	VideoFileRef     string `json:"video_file_ref"`
	VideoOperationID string `json:"video_operation_id"`
	Directory        string `json:"directory"`
	Basename         string `json:"basename"`
	Extension        string `json:"extension"`
}

// bindingDecision is the pure binder output for one subtitle file.
type bindingDecision struct {
	state       string
	boundRef    string
	method      string
	confidence  float64
	diagnostics []naming.NamingDiagnostic
}

// bindingFileFacts are the file-local facts the binder consumes: the file
// name and, for a video, its name without the extension.
type bindingFileFacts struct {
	ref      string
	filename string
	basename string
	// directory is the file's own torrent directory (of its manifest relative
	// path): a subtitle binds only to a video of the same directory (all
	// first-level files share one).
	directory string
}

// fileBindingFacts extracts the binder facts for one artifact file.
func fileBindingFacts(file artifactFileContext) bindingFileFacts {
	filename := filepath.Base(file.sourcePath)
	return bindingFileFacts{
		ref:       file.fileRef,
		filename:  filename,
		basename:  strings.TrimSuffix(filename, filepath.Ext(filename)),
		directory: path.Dir(filepath.ToSlash(file.relativePath)),
	}
}

// decideSubtitleBinding binds one subtitle file to a video of the same
// artifact, or returns an explicit missing decision. The one rule: the
// subtitle filename starts with `<video filename without extension>.`, so
// `abcd.mp4` owns `abcd.ass`, `abcd.forced.ass` and `abcd.chs&cht.ass` but not
// `abcde.ass`. Several matching videos resolve to the longest video name, and
// a remaining tie (same stem, different extension) to the first video in
// `videos`; callers pass videos in manifest path order, so the result never
// depends on runtime arrival. A subtitle never binds across artifacts and
// never falls back to rendering the video template independently.
func decideSubtitleBinding(sub bindingFileFacts, videos []bindingFileFacts) bindingDecision {
	basenames := make([]string, len(videos))
	for index := range videos {
		if videos[index].directory == sub.directory {
			basenames[index] = videos[index].basename
		}
	}
	index := filekinds.BindSubtitle(sub.filename, basenames)
	var match *bindingFileFacts
	if index >= 0 {
		match = &videos[index]
	}
	if match == nil {
		return bindingDecision{
			state: bindingStateMissing,
			diagnostics: []naming.NamingDiagnostic{{
				Code: "subtitle_binding_missing", Message: "no video file name is a prefix of the subtitle file name", Path: "File.Ref"}},
		}
	}
	return bindingDecision{state: bindingStateBound, boundRef: match.ref,
		method: bindingMethodBasenameMatch, confidence: 1.0}
}

// marshalBindingSnapshot renders the persisted snapshot for one decision.
func marshalBindingSnapshot(decision bindingDecision, locked *bindingLockedTarget) (json.RawMessage, error) {
	snapshot := bindingSnapshot{
		Version:     bindingSnapshotVersion,
		Method:      decision.method,
		Confidence:  decision.confidence,
		Diagnostics: decision.diagnostics,
		Locked:      locked,
	}
	if snapshot.Diagnostics == nil {
		snapshot.Diagnostics = []naming.NamingDiagnostic{}
	}
	return json.Marshal(snapshot)
}

// decodeBindingSnapshot parses a persisted snapshot; NULL/empty snapshots mean
// the binder has not run for this row yet.
func decodeBindingSnapshot(raw []byte) (bindingSnapshot, error) {
	if len(raw) == 0 {
		return bindingSnapshot{}, nil
	}
	var snapshot bindingSnapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return bindingSnapshot{}, fmt.Errorf("decode subtitle binding snapshot: %w", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return bindingSnapshot{}, err
	}
	snapshot.ExactConfidence = fields["confidence"]
	return snapshot, nil
}

// persistBindingDecisionTx freezes one binder decision onto the artifact file
// row. Existing snapshots are never overwritten: the frozen decision boundary
// holds until artifact re-verification replaces the file rows.
func persistBindingDecisionTx(ctx context.Context, tx *sql.Tx, file artifactFileContext, decision bindingDecision) error {
	if err := requireFileArtifactQuiescenceTx(ctx, tx, file.id); err != nil {
		return err
	}
	snapshot, err := marshalBindingSnapshot(decision, nil)
	if err != nil {
		return fmt.Errorf("marshal subtitle binding snapshot: %w", err)
	}
	var boundRef any
	if decision.state == bindingStateBound {
		boundRef = decision.boundRef
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE artifact_files
SET subtitle_binding_state = $2,
    bound_video_file_ref = $3,
    subtitle_binding_snapshot = $4
WHERE id = $1 AND subtitle_binding_snapshot IS NULL`,
		file.id, decision.state, boundRef, snapshot); err != nil {
		return fmt.Errorf("persist subtitle binding decision: %w", err)
	}
	return nil
}

// lockBindingTargetTx records the frozen bound-video target segments onto the
// persisted binding snapshot after the subtitle operation has been planned.
func lockBindingTargetTx(ctx context.Context, tx *sql.Tx, file artifactFileContext, snapshot bindingSnapshot) error {
	if err := requireFileArtifactQuiescenceTx(ctx, tx, file.id); err != nil {
		return err
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("marshal locked subtitle binding snapshot: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE artifact_files
SET subtitle_binding_snapshot = $2
WHERE id = $1`, file.id, raw); err != nil {
		return fmt.Errorf("lock subtitle binding target: %w", err)
	}
	return nil
}
