package filemaintenance

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"bansoko/internal/library/domain"
)

// Companion files ("subtitle follows video"): a prefix-bound subtitle is
// a companion of the video placement. Its expected path always derives from the
// video's current or expected path plus the frozen suffix and the sidecar's
// original extension; it never has coverage of its own.

// CompanionStateActive and CompanionStateEnded are the relation states.
const (
	CompanionStateActive = "active"
	CompanionStateEnded  = "ended"
)

// Companion is one registered companion relation.
type Companion struct {
	ID                   string
	PlacementID          string
	ManagedAnimeID       string
	SourceRef            string
	Registration         string
	MediaKind            string
	Suffix               string
	OriginalExtension    string
	CurrentPath          string
	SizeBytes            int64
	Fingerprint          string
	State                string
	AlignmentFailureCode string
	Version              int64
}

// CompanionPath composes a companion path beside a video path:
// <video dir>/<video basename>[.<suffix>].<extension>.
func CompanionPath(videoPath, suffix, extension string) string {
	directory := filepath.Dir(videoPath)
	base := filepath.Base(videoPath)
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	name := stem
	if suffix != "" {
		name += "." + suffix
	}
	return filepath.Join(directory, name+"."+extension)
}

// splitCompanionName derives the frozen suffix and original extension of a
// sidecar path registered beside a video path. ok=false means the sidecar does
// not sit beside the video under the video's basename.
func splitCompanionName(videoPath, sidecarPath string) (suffix, extension string, ok bool) {
	if filepath.Dir(filepath.Clean(videoPath)) != filepath.Dir(filepath.Clean(sidecarPath)) {
		return "", "", false
	}
	videoBase := filepath.Base(videoPath)
	stem := strings.TrimSuffix(videoBase, filepath.Ext(videoBase))
	base := filepath.Base(sidecarPath)
	if !strings.HasPrefix(base, stem+".") {
		return "", "", false
	}
	rest := strings.TrimPrefix(base, stem+".")
	extension = strings.TrimPrefix(filepath.Ext(base), ".")
	if extension == "" || strings.ContainsAny(extension, `/\`) {
		return "", "", false
	}
	if rest == extension {
		return "", extension, true
	}
	suffix = strings.TrimSuffix(rest, "."+extension)
	if suffix == "" || suffix == rest {
		return "", "", false
	}
	return suffix, extension, true
}

func companionID(sourceRef string) string {
	sum := sha256.Sum256([]byte("companion|" + sourceRef))
	return "cmp-" + hex.EncodeToString(sum[:16])
}

// companionFingerprint normalizes a stored fingerprint to the path-level
// metadata value ("meta:size=<n>[;dev=<d>;ino=<i>]") a companion records.
// Fingerprints are stored either as the bare value or as the FileOperation
// fingerprint object {"algorithm":"metadata","value":"meta:...",...}. The
// result is re-rendered in canonical form; any other form yields "" (no
// valid companion fingerprint).
func companionFingerprint(value string) string {
	trimmed := strings.TrimSpace(value)
	if strings.HasPrefix(trimmed, "{") {
		var parsed struct {
			Algorithm string `json:"algorithm"`
			Value     string `json:"value"`
		}
		if json.Unmarshal([]byte(trimmed), &parsed) != nil || parsed.Algorithm != "metadata" {
			return ""
		}
		trimmed = strings.TrimSpace(parsed.Value)
	}
	facts, ok := domain.ParseMetadataFingerprint(trimmed)
	if !ok {
		return ""
	}
	return domain.MetadataFingerprint(facts.SizeBytes, domain.FileIdentity{Device: facts.Device, Inode: facts.Inode})
}

// companionCandidate is one sidecar that may become a companion of a video
// placement.
type companionCandidate struct {
	sourceRef    string
	registration string
	mediaKind    string
	path         string
	sizeBytes    int64
	fingerprint  string
	// frozenSuffix is the suffix the sidecar operation froze (subtitle
	// render snapshot); nil derives it from the name beside the video.
	frozenSuffix *string
}

// sidecarOperation is the frozen evidence of one completed sidecar operation.
type sidecarOperation struct {
	id, mediaKind, artifactID, targetPath, boundVideoOperationID, fingerprint, suffix string
	sizeBytes                                                                         int64
	completed                                                                         bool
}

func loadSidecarOperationTx(ctx context.Context, tx *sql.Tx, fileOperationID string) (sidecarOperation, bool, error) {
	var op sidecarOperation
	var state string
	var bound, fingerprint sql.NullString
	var size sql.NullInt64
	err := tx.QueryRowContext(ctx, `
SELECT fo.id, af.media_kind, fo.artifact_id, fo.target_path, fo.bound_video_operation_id,
       fo.target_fingerprint::text, COALESCE(fo.observed_target_size_bytes, fo.expected_size_bytes), fo.state,
       COALESCE(fo.frozen_render_snapshot->>'suffix', '')
FROM file_operations fo
JOIN artifact_files af ON af.id = fo.artifact_file_id
WHERE fo.id = $1`, fileOperationID).Scan(&op.id, &op.mediaKind, &op.artifactID, &op.targetPath, &bound, &fingerprint, &size, &state, &op.suffix)
	if errors.Is(err, sql.ErrNoRows) {
		return op, false, nil
	}
	if err != nil {
		return op, false, err
	}
	op.boundVideoOperationID = bound.String
	op.fingerprint = companionFingerprint(fingerprint.String)
	op.sizeBytes = size.Int64
	op.completed = state == "completed"
	return op, true, nil
}

// RegisterCompanions registers every companion relation reachable from one
// completed file operation, in one transaction, idempotently:
//
//   - a video operation whose placement exists registers its completed bound
//     subtitles and the subtitles the same typed plan adopted in place beside
//     it;
//   - a subtitle operation registers itself once its video's placement exists.
//
// Either order converges: whichever handoff runs second registers the pair. A
// subtitle of a released placement (an unknown file, released at admission)
// is recorded as released with it.
func (s *PlacementService) RegisterCompanions(ctx context.Context, fileOperationID string) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	registered, err := registerCompanionsTx(ctx, tx, fileOperationID)
	if err != nil {
		return 0, err
	}
	return registered, tx.Commit()
}

func registerCompanionsTx(ctx context.Context, tx *sql.Tx, fileOperationID string) (int, error) {
	op, found, err := loadSidecarOperationTx(ctx, tx, fileOperationID)
	if err != nil || !found || !op.completed {
		return 0, err
	}
	registered := 0
	switch op.mediaKind {
	case "video":
		placement, ok, err := placementForOperationTx(ctx, tx, op.id)
		if err != nil || !ok {
			return 0, err
		}
		candidates, err := companionCandidatesForVideoTx(ctx, tx, op)
		if err != nil {
			return 0, err
		}
		for _, candidate := range candidates {
			done, err := registerCompanionTx(ctx, tx, placement, candidate)
			if err != nil {
				return 0, err
			}
			if done {
				registered++
			}
		}
	case "subtitle":
		if op.boundVideoOperationID == "" {
			return 0, nil
		}
		placement, ok, err := placementForOperationTx(ctx, tx, op.boundVideoOperationID)
		if err != nil || !ok {
			return 0, err
		}
		if op.fingerprint == "" {
			return 0, fmt.Errorf("sidecar operation %s has no verified target fingerprint", op.id)
		}
		suffix := op.suffix
		done, err := registerCompanionTx(ctx, tx, placement, companionCandidate{
			sourceRef: op.id, registration: "intake", mediaKind: op.mediaKind, path: op.targetPath,
			sizeBytes: op.sizeBytes, fingerprint: op.fingerprint, frozenSuffix: &suffix,
		})
		if err != nil {
			return 0, err
		}
		if done {
			registered++
		}
	}
	return registered, nil
}

// companionPlacement is the video placement a companion registers against.
type companionPlacement struct {
	id, path, animeID string
	released          bool
}

func placementForOperationTx(ctx context.Context, tx *sql.Tx, fileOperationID string) (companionPlacement, bool, error) {
	var placement companionPlacement
	var state string
	err := tx.QueryRowContext(ctx, `
SELECT id, physical_path, managed_anime_id, state FROM placements
WHERE source_evidence_type = 'file_operation' AND source_owner_ref = $1 AND state IN ('active', 'released')
FOR UPDATE`, fileOperationID).Scan(&placement.id, &placement.path, &placement.animeID, &state)
	if errors.Is(err, sql.ErrNoRows) {
		return placement, false, nil
	}
	placement.released = state == "released"
	return placement, err == nil, err
}

func companionCandidatesForVideoTx(ctx context.Context, tx *sql.Tx, video sidecarOperation) ([]companionCandidate, error) {
	candidates := []companionCandidate{}
	rows, err := tx.QueryContext(ctx, `
SELECT fo.id, af.media_kind, fo.target_path, fo.target_fingerprint::text,
       COALESCE(fo.observed_target_size_bytes, fo.expected_size_bytes),
       COALESCE(fo.frozen_render_snapshot->>'suffix', '')
FROM file_operations fo
JOIN artifact_files af ON af.id = fo.artifact_file_id
WHERE fo.artifact_id = $1 AND fo.state = 'completed' AND af.media_kind = 'subtitle'
  AND fo.bound_video_operation_id = $2
ORDER BY fo.id`, video.artifactID, video.id)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var c companionCandidate
		var fingerprint sql.NullString
		var suffix string
		if err := rows.Scan(&c.sourceRef, &c.mediaKind, &c.path, &fingerprint, &c.sizeBytes, &suffix); err != nil {
			rows.Close()
			return nil, err
		}
		c.registration = "intake"
		c.fingerprint = companionFingerprint(fingerprint.String)
		c.frozenSuffix = &suffix
		if c.fingerprint == "" {
			continue
		}
		candidates = append(candidates, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Subtitles the same typed plan adopted in place (an existing regular file
	// of the expected size at its target) have no FileOperation; their declared
	// target and adoption evidence are the frozen facts. Adoption is path +
	// size: the companion records the metadata fingerprint of the adopted
	// target object (its observed size and device/inode), never the source's.
	adopted, err := tx.QueryContext(ctx, `
SELECT fo.postprocessing_plan_id, i.file_ref, af.media_kind, i.canonical_path,
       COALESCE(i.plan_evidence->'final_object'->>'size', '-1'),
       COALESCE(i.plan_evidence->'final_object'->'identity'->>'device', '0'),
       COALESCE(i.plan_evidence->'final_object'->'identity'->>'inode', '0')
FROM file_operations fo
JOIN acquisition_files i ON i.acquisition_id = fo.acquisition_id AND i.plan_kind = 'adopt'
JOIN artifact_files af ON af.artifact_id = fo.artifact_id AND af.file_ref = i.file_ref
WHERE fo.id = $1 AND af.media_kind = 'subtitle'
ORDER BY i.file_ref`, video.id)
	if err != nil {
		return nil, err
	}
	defer adopted.Close()
	for adopted.Next() {
		var planID, fileRef, size, device, inode string
		var c companionCandidate
		if err := adopted.Scan(&planID, &fileRef, &c.mediaKind, &c.path, &size, &device, &inode); err != nil {
			return nil, err
		}
		if _, _, ok := splitCompanionName(video.targetPath, c.path); !ok {
			continue
		}
		if _, err := fmt.Sscan(size, &c.sizeBytes); err != nil || c.sizeBytes < 0 {
			continue
		}
		c.sourceRef = "adopt:" + planID + ":" + fileRef
		c.registration = "adopted"
		var identity domain.FileIdentity
		if _, err := fmt.Sscan(device, &identity.Device); err != nil {
			continue
		}
		if _, err := fmt.Sscan(inode, &identity.Inode); err != nil {
			continue
		}
		c.fingerprint = companionFingerprint(domain.MetadataFingerprint(c.sizeBytes, identity))
		if c.fingerprint == "" {
			continue
		}
		candidates = append(candidates, c)
	}
	return candidates, adopted.Err()
}

// registerCompanionTx inserts one companion of the placement. A path held by
// an active companion of another relation now belongs to this newer
// registration: the older relation ends (its file was replaced or adopted at
// that path by this intake) and is never cleaned up. A companion of a
// released placement is recorded already ended as released with its video.
func registerCompanionTx(ctx context.Context, tx *sql.Tx, placement companionPlacement, c companionCandidate) (bool, error) {
	suffix, extension, ok := splitCompanionName(placement.path, c.path)
	if c.frozenSuffix != nil {
		suffix, extension, ok = *c.frozenSuffix, strings.TrimPrefix(filepath.Ext(c.path), "."), true
	}
	if !ok {
		// The sidecar landed beside the video's planned target, but the
		// placement now records a different current path: derive the frozen
		// suffix from the sidecar's own name, with the extension last.
		base := filepath.Base(c.path)
		extension = strings.TrimPrefix(filepath.Ext(base), ".")
		suffix = ""
		if extension == "" {
			return false, fmt.Errorf("sidecar %s has no extension", c.sourceRef)
		}
	}
	var existing string
	err := tx.QueryRowContext(ctx, `SELECT id FROM placement_companions WHERE source_ref = $1`, c.sourceRef).Scan(&existing)
	if err == nil {
		return false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE placement_companions
SET state = 'ended', ended_reason = 'path_taken_by_newer_registration', version = version + 1, updated_at = CURRENT_TIMESTAMP
WHERE current_path = $1 AND state = 'active'`, c.path); err != nil {
		return false, err
	}
	state, endedReason := CompanionStateActive, sql.NullString{}
	if placement.released {
		state, endedReason = CompanionStateEnded, sql.NullString{String: "released", Valid: true}
	}
	_, err = tx.ExecContext(ctx, `
INSERT INTO placement_companions
(id, placement_id, managed_anime_id, source_ref, registration, media_kind, suffix, original_extension,
 current_path, size_bytes, fingerprint, state, ended_reason)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
ON CONFLICT (source_ref) DO NOTHING`,
		companionID(c.sourceRef), placement.id, placement.animeID, c.sourceRef, c.registration, c.mediaKind, suffix, extension,
		c.path, c.sizeBytes, c.fingerprint, state, endedReason)
	return err == nil, err
}

// ListCompanions returns the companions of one placement (all states).
func (s *PlacementService) ListCompanions(ctx context.Context, placementID string) ([]Companion, error) {
	return listCompanions(ctx, s.db, `placement_id = $1`, placementID)
}

func listCompanions(ctx context.Context, db *sql.DB, where string, args ...any) ([]Companion, error) {
	rows, err := db.QueryContext(ctx, `
SELECT id, placement_id, managed_anime_id, source_ref, registration, media_kind, suffix, original_extension,
       current_path, size_bytes, fingerprint, state, COALESCE(alignment_failure_code, ''), version
FROM placement_companions WHERE `+where+` ORDER BY current_path, id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	companions := []Companion{}
	for rows.Next() {
		var c Companion
		if err := rows.Scan(&c.ID, &c.PlacementID, &c.ManagedAnimeID, &c.SourceRef, &c.Registration, &c.MediaKind, &c.Suffix,
			&c.OriginalExtension, &c.CurrentPath, &c.SizeBytes, &c.Fingerprint, &c.State, &c.AlignmentFailureCode, &c.Version); err != nil {
			return nil, err
		}
		companions = append(companions, c)
	}
	return companions, rows.Err()
}

// activeCompanionsByPlacement returns the active companions of the given
// placements keyed by placement id.
func activeCompanionsByPlacement(ctx context.Context, db *sql.DB, placementIDs []string) (map[string][]Companion, error) {
	result := map[string][]Companion{}
	if len(placementIDs) == 0 {
		return result, nil
	}
	companions, err := listCompanions(ctx, db, `state = 'active' AND placement_id = ANY($1)`, placementIDs)
	if err != nil {
		return nil, err
	}
	for _, c := range companions {
		result[c.PlacementID] = append(result[c.PlacementID], c)
	}
	return result, nil
}
