package filemaintenance

import (
	"context"
	"database/sql"
	"errors"
)

// FileOperationEvidenceReader implements the verified core-chain evidence
// port: Artifact verified -> FileOperation completed -> target fingerprint
// reread matched.
type FileOperationEvidenceReader struct {
	db *sql.DB
}

// NewFileOperationEvidenceReader wires the SQL reader.
func NewFileOperationEvidenceReader(db *sql.DB) *FileOperationEvidenceReader {
	return &FileOperationEvidenceReader{db: db}
}

// ReadVerifiedEvidence resolves the frozen evidence for one file operation.
func (r *FileOperationEvidenceReader) ReadVerifiedEvidence(ctx context.Context, fileOperationID string) (*FileOperationEvidence, bool, error) {
	var (
		evidence               FileOperationEvidence
		artifactState, opState string
		targetFingerprint      sql.NullString
		correlationID          sql.NullString
	)
	err := r.db.QueryRowContext(ctx, `
SELECT fo.id, fo.target_path, fo.source_fingerprint, fo.target_fingerprint, fo.state,
       a.state, am.id, rel.id, af.media_kind, af.file_ref, COALESCE(dt.correlation_id, fo.correlation_id),
       COALESCE(fo.frozen_render_snapshot->'unknown_file' = 'true'::jsonb, false)
FROM file_operations fo
JOIN artifacts a ON a.id = fo.artifact_id
JOIN artifact_files af ON af.id = fo.artifact_file_id
JOIN download_tasks dt ON dt.id = a.download_task_id
JOIN releases rel ON rel.id = dt.release_id
JOIN rss_sources src ON src.id = rel.rss_source_id
JOIN subscriptions sub ON sub.id = src.subscription_id
JOIN managed_anime am ON am.id = sub.managed_anime_id
WHERE fo.id = $1`, fileOperationID).
		Scan(&evidence.OperationID, &evidence.TargetPath, &evidence.SourceFingerprint,
			&targetFingerprint, &opState, &artifactState, &evidence.ManagedAnimeID,
			&evidence.ReleaseID, &evidence.FileKind, &evidence.FileRef, &correlationID, &evidence.UnknownFile)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	// The Business Log lineage is the Release admission correlation carried by
	// the DownloadTask and Artifact. A typed (S4) FileOperation row records its
	// acquisition root in correlation_id (plan commit 153/154), so the
	// placement inherits the task's lineage, not the root id.
	evidence.CorrelationID = correlationID.String
	evidence.ArtifactVerified = artifactState == "verified"
	if targetFingerprint.Valid {
		evidence.TargetFingerprint = targetFingerprint.String
	}
	if opState != "completed" || !evidence.ArtifactVerified || evidence.TargetFingerprint == "" {
		return &evidence, true, nil // present but not verified evidence
	}
	return &evidence, true, nil
}
