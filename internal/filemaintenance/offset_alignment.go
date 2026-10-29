package filemaintenance

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"bansoko/internal/episodeidentity"
	"bansoko/internal/persistence/txscope"
	facts "bansoko/internal/release/facts"
)

// Offset alignment: an offset change of an RSS link is an identity change.
// The transaction that changes the offset rewrites the coverage key of the
// link's single-episode placements and touches no file; each re-keyed
// placement gets an alignment request carrying the change. The request
// dispatcher then puts the keys through retention (the losers are deleted by
// the existing retention path) and submits a scan, whose alignment consumer
// routes the keepers like any other move: retention first, then the
// collision policy. One activity record per change counts what happened.
//
// A multi-episode file, a movie and a file whose frozen identity does not
// resolve keep their keys and do not move with an offset.

// OffsetAlignmentService is the offset change's in-transaction re-key (the
// RSS link owner's SourceOffsetAlignment seam).
type OffsetAlignmentService struct{}

// NewOffsetAlignmentService wires the transaction-only re-key.
func NewOffsetAlignmentService() *OffsetAlignmentService {
	return &OffsetAlignmentService{}
}

// Offset change states: comparing waits for the retention recheck of the
// re-keyed keys, aligning for the scan that routes the keepers, recorded
// once its activity record is written.
const (
	offsetChangeComparing = "comparing"
	offsetChangeAligning  = "aligning"
	offsetChangeRecorded  = "recorded"
)

// AdmitRSSSourceTx re-keys the link's placements inside the link owner's
// offset CAS transaction and records the change. It returns the change id
// and whether this call created it (a replay of the same link version
// returns the existing change).
func (s *OffsetAlignmentService) AdmitRSSSourceTx(ctx context.Context, scope txscope.Tx, animeID, rssSourceID string, sourceVersion int64, oldOffset, newOffset int) (string, bool, error) {
	if oldOffset == newOffset {
		return "", false, nil
	}
	tx := scope.SQLTx()
	if tx == nil {
		return "", false, errors.New("offset alignment transaction is nil")
	}
	changeID := offsetAlignmentChangeID(rssSourceID, sourceVersion)
	result, err := tx.ExecContext(ctx, `
INSERT INTO offset_alignment_changes (id, rss_source_id, source_version, old_offset, new_offset)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (rss_source_id, source_version) DO NOTHING`, changeID, rssSourceID, sourceVersion, oldOffset, newOffset)
	if err != nil {
		return "", false, fmt.Errorf("record offset change: %w", err)
	}
	if inserted, err := result.RowsAffected(); err != nil || inserted == 0 {
		return changeID, false, err
	}
	candidates, err := loadOffsetRekeyCandidates(ctx, tx, animeID, rssSourceID)
	if err != nil {
		return "", false, err
	}
	rekeyed := 0
	for _, candidate := range candidates {
		key, ok := offsetRekeyedKey(candidate, newOffset)
		if !ok || key == candidate.currentKey {
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE placement_coverage SET observation_key = $2 WHERE placement_id = $1`, candidate.placementID, key); err != nil {
			return "", false, fmt.Errorf("re-key placement %s: %w", candidate.placementID, err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE placements SET version = version + 1, updated_at = CURRENT_TIMESTAMP WHERE id = $1`, candidate.placementID); err != nil {
			return "", false, fmt.Errorf("re-key placement %s: %w", candidate.placementID, err)
		}
		if err := requestAlignmentTx(ctx, tx, candidate.placementID, changeID); err != nil {
			return "", false, err
		}
		rekeyed++
	}
	state := offsetChangeComparing
	if rekeyed == 0 {
		// Nothing imported moved to another episode: no record to write.
		state = offsetChangeRecorded
	}
	if _, err := tx.ExecContext(ctx, `UPDATE offset_alignment_changes SET rekeyed_count = $2, state = $3 WHERE id = $1`, changeID, rekeyed, state); err != nil {
		return "", false, fmt.Errorf("record offset change: %w", err)
	}
	return changeID, true, nil
}

func offsetAlignmentChangeID(rssSourceID string, sourceVersion int64) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d", rssSourceID, sourceVersion)))
	return "offset:" + hex.EncodeToString(digest[:16])
}

// offsetRekeyCandidate is one active single-key placement of the link with
// its frozen media facts.
type offsetRekeyCandidate struct {
	placementID string
	currentKey  string
	keyCount    int
	// ownerMediaKind is the owner media kind (tv | movie; empty is tv),
	// read only when the frozen identity carries no kind.
	ownerMediaKind string
	fileRef        string
	effectiveFacts []byte
}

// loadOffsetRekeyCandidates reads the link's active, verified, replacement-
// eligible placements (an excluded placement keeps its key) with the frozen
// facts of the file each one placed, locking the placement rows.
func loadOffsetRekeyCandidates(ctx context.Context, tx *sql.Tx, animeID, rssSourceID string) ([]offsetRekeyCandidate, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT p.id,
       COALESCE((SELECT min(c.observation_key) FROM placement_coverage c WHERE c.placement_id = p.id), ''),
       (SELECT count(*) FROM placement_coverage c WHERE c.placement_id = p.id),
       COALESCE(meta.media_kind, ''),
       COALESCE(mf.file_ref, ''),
       COALESCE(mf.effective_facts, '{}'::jsonb)
FROM placements p
JOIN file_operations fo ON fo.id = p.source_owner_ref AND p.source_evidence_type = 'file_operation'
JOIN artifact_files af ON af.id = fo.artifact_file_id
JOIN artifacts art ON art.id = fo.artifact_id
JOIN download_tasks dt ON dt.id = art.download_task_id
JOIN releases rel ON rel.id = dt.release_id
LEFT JOIN anime_metadata meta ON meta.managed_anime_id = p.managed_anime_id
LEFT JOIN LATERAL (
    SELECT snap.id FROM media_fact_snapshots snap
    WHERE snap.release_id = rel.id
    ORDER BY snap.created_at DESC, snap.id DESC
    LIMIT 1
) snap ON true
LEFT JOIN media_fact_files mf ON mf.snapshot_id = snap.id AND mf.file_ref = af.file_ref
WHERE p.managed_anime_id = $1 AND rel.rss_source_id = $2
  AND p.state = 'active' AND p.verification_state = 'verified' AND p.replacement_eligible
ORDER BY p.created_at, p.id
FOR UPDATE OF p`, animeID, rssSourceID)
	if err != nil {
		return nil, fmt.Errorf("load offset re-key candidates: %w", err)
	}
	defer rows.Close()
	var out []offsetRekeyCandidate
	for rows.Next() {
		var candidate offsetRekeyCandidate
		if err := rows.Scan(&candidate.placementID, &candidate.currentKey, &candidate.keyCount,
			&candidate.ownerMediaKind, &candidate.fileRef, &candidate.effectiveFacts); err != nil {
			return nil, fmt.Errorf("scan offset re-key candidate: %w", err)
		}
		out = append(out, candidate)
	}
	return out, rows.Err()
}

// offsetRekeyedKey is the candidate's coverage key under newOffset: its
// frozen raw episode re-resolved with the new offset. ok is false for a file
// an offset does not move (several keys, a movie, an unresolved identity).
func offsetRekeyedKey(candidate offsetRekeyCandidate, newOffset int) (string, bool) {
	if candidate.keyCount != 1 || candidate.fileRef == "" {
		return "", false
	}
	var effective facts.EffectiveFileFacts
	if err := json.Unmarshal(candidate.effectiveFacts, &effective); err != nil {
		return "", false
	}
	frozen := effective.Identity
	if frozen.State != episodeidentity.StateResolved || frozen.RawEpisode == nil || frozen.SelectedSource == nil || frozen.MultiEpisode() {
		return "", false
	}
	// The frozen identity kind is the media-kind authority; the owner kind is
	// read only for an identity frozen before the kind existed.
	mediaKind := frozenIdentityMediaKind(frozen.Kind)
	if mediaKind == "" {
		mediaKind = ownerMediaKind(candidate.ownerMediaKind)
	}
	raw := *frozen.RawEpisode
	identity := episodeidentity.NewResolver().Resolve(episodeidentity.EpisodeIdentityInput{
		MediaKind: mediaKind,
		FileRef:   candidate.fileRef,
		FileEvidence: episodeidentity.EpisodeEvidence{
			Source:     *frozen.SelectedSource,
			InputKind:  "frozen_identity",
			Status:     episodeidentity.StatusValue,
			Shape:      episodeidentity.ShapeSingle,
			RawCapture: string(raw),
			Episode:    &raw,
		},
		Offset: newOffset,
	})
	if identity.Kind == episodeidentity.KindMovie || identity.State != episodeidentity.StateResolved || identity.CoverageKey == nil {
		return "", false
	}
	return episodeidentity.CanonicalCoverageKey(*identity.CoverageKey), true
}

// frozenIdentityMediaKind maps a frozen EpisodeIdentity kind to the metadata
// media kind the resolver consumes; an identity frozen before the kind
// existed returns "".
func frozenIdentityMediaKind(kind string) string {
	switch kind {
	case episodeidentity.KindMovie:
		return "movie"
	case episodeidentity.KindTVEpisode:
		return "tv"
	default:
		return ""
	}
}
