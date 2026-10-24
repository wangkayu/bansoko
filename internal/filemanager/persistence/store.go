// Package persistence is the PostgreSQL store of the file manager: the
// owner-fact reader (store.go, plain SELECTs that lock and write nothing) and
// the P1 command writes (commands.go), each one transaction that re-checks
// the facts it was admitted on.
package persistence

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"bansoko/internal/download/qbittorrent"
	"bansoko/internal/episodeidentity"
	"bansoko/internal/filemanager"
)

// Store reads the mounts and the owner facts for the file manager.
type Store struct {
	db *sql.DB
}

// NewStore builds the reader.
func NewStore(db *sql.DB) (*Store, error) {
	if db == nil {
		return nil, errors.New("file manager store database is nil")
	}
	return &Store{db: db}, nil
}

var _ filemanager.Store = (*Store)(nil)

// Mounts returns the application-side root of every qBittorrent connection
// that has one. The filesystem-root mount and the works' tree-top mounts are
// composed by the service, so they are not read here.
func (s *Store) Mounts(ctx context.Context) ([]filemanager.MountConfig, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, config FROM connections WHERE kind = 'qbittorrent' ORDER BY name, id`)
	if err != nil {
		return nil, fmt.Errorf("list download mounts: %w", err)
	}
	defer rows.Close()
	var mounts []filemanager.MountConfig
	for rows.Next() {
		var id, name string
		var config []byte
		if err := rows.Scan(&id, &name, &config); err != nil {
			return nil, fmt.Errorf("scan download mount: %w", err)
		}
		var typed struct {
			SaveRoot       string `json:"save_root"`
			DownloaderRoot string `json:"downloader_root"`
		}
		if err := json.Unmarshal(config, &typed); err != nil {
			continue
		}
		paths, err := qbittorrent.DerivePathSnapshot(qbittorrent.RuntimeConfig{SaveRoot: typed.SaveRoot, ApplicationRoot: typed.DownloaderRoot})
		if err != nil || paths.ApplicationRoot == "" {
			continue
		}
		mounts = append(mounts, filemanager.MountConfig{
			Key: filemanager.DownloadMountPrefix + id, Kind: filemanager.MountDownload,
			ConnectionID: id, Label: name, Path: paths.ApplicationRoot,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list download mounts: %w", err)
	}
	return mounts, nil
}

// AnimePlacementIndex lists every work that owns at least one active placement,
// with the paths those placements put on disk. The service derives each work's
// tree top from them.
func (s *Store) AnimePlacementIndex(ctx context.Context) ([]filemanager.AnimePlacements, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT p.managed_anime_id, COALESCE(ma.title_override, ma.title, ''), p.physical_path
FROM placements p JOIN managed_anime ma ON ma.id = p.managed_anime_id
WHERE p.state = 'active' AND p.managed_anime_id IS NOT NULL AND p.managed_anime_id <> ''
ORDER BY p.managed_anime_id, p.physical_path LIMIT 20000`)
	if err != nil {
		return nil, fmt.Errorf("read anime placement index: %w", err)
	}
	defer rows.Close()
	var index []filemanager.AnimePlacements
	for rows.Next() {
		var id, title, path string
		if err := rows.Scan(&id, &title, &path); err != nil {
			return nil, fmt.Errorf("scan anime placement index: %w", err)
		}
		if len(index) > 0 && index[len(index)-1].ManagedAnimeID == id {
			last := &index[len(index)-1]
			last.Paths = append(last.Paths, path)
			continue
		}
		index = append(index, filemanager.AnimePlacements{ManagedAnimeID: id, Title: title, Paths: []string{path}})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read anime placement index: %w", err)
	}
	return index, nil
}

// placementColumns projects one placement row with the owner facts its state
// needs: the anime row, its lifecycle (retired = unsubscribed), the
// subscription state, the one-shot marker of a temporary anime, and the
// execution record of the file operation that established it.
const placementColumns = `
 p.id, p.physical_path, p.state, p.managed_anime_id,
 ma.id IS NOT NULL, COALESCE(ma.title_override, ma.title, ''),
 COALESCE(ma.lifecycle_state = 'retired', false), COALESCE(s.state, ''),
 COALESCE((SELECT o.id FROM one_shot_acquisitions o WHERE o.managed_anime_id = p.managed_anime_id ORDER BY o.id LIMIT 1), ''),
 COALESCE((SELECT fo.acquisition_id FROM file_operations fo JOIN acquisitions a ON a.id = fo.acquisition_id
   WHERE p.source_evidence_type = 'file_operation' AND fo.id = p.source_owner_ref), ''),
 p.version, COALESCE(p.displaced_cause, '')`

const placementJoins = `
 LEFT JOIN managed_anime ma ON ma.id = p.managed_anime_id
 LEFT JOIN subscriptions s ON s.managed_anime_id = p.managed_anime_id`

func scanPlacement(scan func(...any) error, extra ...any) (filemanager.PlacementFact, error) {
	var fact filemanager.PlacementFact
	targets := append(extra, &fact.ID, &fact.Path, &fact.State, &fact.ManagedAnimeID,
		&fact.AnimeExists, &fact.AnimeTitle, &fact.Retired, &fact.SubscriptionState, &fact.OneShotID, &fact.AcquisitionID, &fact.Version, &fact.DisplacedCause)
	return fact, scan(targets...)
}

// inProgressOwners is every non-terminal owner path:
// unfinished file operations (source and target), pending or retrying
// placement handoffs, admitted alignments, admitted cleanups (source and
// quarantine), and admitted or queued replacement decisions (candidate and
// frozen losers), unfinished offset migrations (old and new target), and
// unfinished user file operations (source, requested target, resolved result
// and delete capture).
const automaticOwners = `
SELECT fo.source_path AS path, 'file_operation' AS kind, COALESCE(fo.acquisition_id, '') AS acquisition_id
  FROM file_operations fo WHERE fo.state IN ('pending', 'running', 'waiting_retry')
UNION ALL
SELECT fo.target_path, 'file_operation', COALESCE(fo.acquisition_id, '')
  FROM file_operations fo WHERE fo.state IN ('pending', 'running', 'waiting_retry')
UNION ALL
SELECT fo.target_path, 'placement_handoff', COALESCE(fo.acquisition_id, '')
  FROM placement_obligations po JOIN file_operations fo ON fo.id = po.file_operation_id
  WHERE po.state IN ('pending', 'waiting_retry')
UNION ALL
SELECT a.source_path, 'alignment', '' FROM alignment_operations a WHERE a.state IN ('planned', 'ready', 'running')
UNION ALL
SELECT a.target_path, 'alignment', '' FROM alignment_operations a WHERE a.state IN ('planned', 'ready', 'running')
UNION ALL
SELECT c.source_path, 'cleanup', '' FROM cleanup_operations c WHERE c.state IN ('admitted', 'quarantined', 'grace_waiting')
UNION ALL
SELECT c.quarantine_path, 'cleanup', '' FROM cleanup_operations c
  WHERE c.state IN ('admitted', 'quarantined', 'grace_waiting') AND c.quarantine_path IS NOT NULL
UNION ALL
SELECT p.physical_path, 'replacement', '' FROM replacement_decisions d JOIN placements p ON p.id = d.candidate_placement_id
  WHERE d.state IN ('admitted', 'queued')
UNION ALL
SELECT p.physical_path, 'replacement', '' FROM replacement_decisions d
  CROSS JOIN LATERAL jsonb_array_elements(d.pools) pool
  CROSS JOIN LATERAL jsonb_array_elements(CASE WHEN jsonb_typeof(pool->'losers') = 'array' THEN pool->'losers' ELSE '[]'::jsonb END) loser
  JOIN placements p ON p.id = loser->>'placement_id'
  WHERE d.state IN ('admitted', 'queued')
UNION ALL
SELECT m.old_target_path, 'offset_migration', '' FROM offset_migration_plans m
  WHERE m.state IN ('planned', 'ready', 'running', 'waiting_retry', 'effect_unknown')
UNION ALL
SELECT m.new_target_path, 'offset_migration', '' FROM offset_migration_plans m
  WHERE m.state IN ('planned', 'ready', 'running', 'waiting_retry', 'effect_unknown')`

// inProgressOwners adds the unfinished user file operations to the automatic
// owners.
const inProgressOwners = automaticOwners + `
UNION ALL
SELECT u.source_path, 'user_operation', '' FROM file_manager_operations u WHERE u.state IN ('ready', 'running')
UNION ALL
SELECT u.target_path, 'user_operation', '' FROM file_manager_operations u
  WHERE u.state IN ('ready', 'running') AND u.target_path IS NOT NULL
UNION ALL
SELECT u.result_path, 'user_operation', '' FROM file_manager_operations u
  WHERE u.state IN ('ready', 'running') AND u.result_path IS NOT NULL
UNION ALL
SELECT u.attempt->>'quarantine_path', 'user_operation', '' FROM file_manager_operations u
  WHERE u.state IN ('ready', 'running') AND u.attempt ? 'quarantine_path'`

// PathFacts reads the placements, companion relations and non-terminal owners
// at exactly the given paths, in one read-only snapshot.
func (s *Store) PathFacts(ctx context.Context, paths []string) (filemanager.PathFacts, error) {
	facts := filemanager.PathFacts{
		Placements: map[string][]filemanager.PlacementFact{},
		Companions: map[string][]filemanager.CompanionFact{},
		InProgress: map[string][]filemanager.InProgressFact{},
	}
	if len(paths) == 0 {
		return facts, nil
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return facts, fmt.Errorf("begin file manager read: %w", err)
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx, `SELECT`+placementColumns+` FROM placements p`+placementJoins+`
WHERE p.physical_path COLLATE "C" = ANY($1::text[])`, paths)
	if err != nil {
		return facts, fmt.Errorf("read placement facts: %w", err)
	}
	for rows.Next() {
		fact, err := scanPlacement(rows.Scan)
		if err != nil {
			rows.Close()
			return facts, fmt.Errorf("scan placement fact: %w", err)
		}
		facts.Placements[fact.Path] = append(facts.Placements[fact.Path], fact)
	}
	if err := closeRows(rows); err != nil {
		return facts, fmt.Errorf("read placement facts: %w", err)
	}

	companions, err := queryCompanions(ctx, tx, `c.current_path COLLATE "C" = ANY($1::text[])`, paths)
	if err != nil {
		return facts, err
	}
	for _, companion := range companions {
		facts.Companions[companion.Path] = append(facts.Companions[companion.Path], companion)
	}

	rows, err = tx.QueryContext(ctx, `SELECT o.path, o.kind, o.acquisition_id FROM (`+inProgressOwners+`) o
WHERE o.path = ANY($1::text[])`, paths)
	if err != nil {
		return facts, fmt.Errorf("read in-progress owners: %w", err)
	}
	for rows.Next() {
		var fact filemanager.InProgressFact
		var kind string
		if err := rows.Scan(&fact.Path, &kind, &fact.AcquisitionID); err != nil {
			rows.Close()
			return facts, fmt.Errorf("scan in-progress owner: %w", err)
		}
		fact.Kind = filemanager.InProgressKind(kind)
		facts.InProgress[fact.Path] = append(facts.InProgress[fact.Path], fact)
	}
	if err := closeRows(rows); err != nil {
		return facts, fmt.Errorf("read in-progress owners: %w", err)
	}
	return facts, tx.Commit()
}

type queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func queryCompanions(ctx context.Context, db queryer, where string, args ...any) ([]filemanager.CompanionFact, error) {
	rows, err := db.QueryContext(ctx, `SELECT c.id, c.current_path, c.state, c.media_kind, c.suffix, c.original_extension,
 c.alignment_failure_code IS NOT NULL, c.version,`+placementColumns+`
FROM placement_companions c JOIN placements p ON p.id = c.placement_id`+placementJoins+`
WHERE `+where+` ORDER BY c.id`, args...)
	if err != nil {
		return nil, fmt.Errorf("read companion facts: %w", err)
	}
	var result []filemanager.CompanionFact
	for rows.Next() {
		var companion filemanager.CompanionFact
		placement, err := scanPlacement(rows.Scan, &companion.ID, &companion.Path, &companion.State, &companion.MediaKind,
			&companion.Suffix, &companion.Extension, &companion.AlignmentFailed, &companion.Version)
		if err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan companion fact: %w", err)
		}
		companion.Placement = placement
		result = append(result, companion)
	}
	if err := closeRows(rows); err != nil {
		return nil, fmt.Errorf("read companion facts: %w", err)
	}
	return result, nil
}

// SubtreeFacts summarizes, for each directory, whether any non-terminal owner
// path or managed file lies strictly below it. Managed applies the same rules
// as a single file: an active placement of an existing, not retired,
// non-one-shot anime (or an active companion of one).
//
// The read is set-based over the whole page: the non-terminal owners are
// evaluated once (MATERIALIZED, they are few), and each directory becomes one
// byte-order range [prefix, prefix+1) probed through the C-collation indexes of
// placements.physical_path and placement_companions.current_path. The prefix
// always ends in '/', so the first byte after the range is '0'.
func (s *Store) SubtreeFacts(ctx context.Context, dirs []string) (map[string]filemanager.SubtreeFact, error) {
	result := map[string]filemanager.SubtreeFact{}
	if len(dirs) == 0 {
		return result, nil
	}
	rows, err := s.db.QueryContext(ctx, `
WITH dirs AS (SELECT DISTINCT d AS dir, rtrim(d, '/') || '/' AS prefix FROM unnest($1::text[]) AS d),
bounds AS (SELECT dir, prefix, left(prefix, -1) || '0' AS upper_bound FROM dirs),
owners AS MATERIALIZED (SELECT o.path FROM (`+inProgressOwners+`) o WHERE o.path IS NOT NULL)
SELECT b.dir,
  EXISTS (SELECT 1 FROM owners o WHERE o.path COLLATE "C" >= b.prefix AND o.path COLLATE "C" < b.upper_bound),
  EXISTS (
    SELECT 1
    FROM placements p
    JOIN managed_anime ma ON ma.id = p.managed_anime_id
    WHERE p.physical_path COLLATE "C" >= b.prefix AND p.physical_path COLLATE "C" < b.upper_bound
      AND p.state = 'active' AND COALESCE(ma.lifecycle_state, 'active') <> 'retired'
      AND NOT EXISTS (SELECT 1 FROM one_shot_acquisitions os WHERE os.managed_anime_id = p.managed_anime_id)
  ) OR EXISTS (
    SELECT 1
    FROM placement_companions c
    JOIN placements p ON p.id = c.placement_id
    JOIN managed_anime ma ON ma.id = p.managed_anime_id
    WHERE c.current_path COLLATE "C" >= b.prefix AND c.current_path COLLATE "C" < b.upper_bound
      AND c.state = 'active' AND p.state = 'active' AND COALESCE(ma.lifecycle_state, 'active') <> 'retired'
      AND NOT EXISTS (SELECT 1 FROM one_shot_acquisitions os WHERE os.managed_anime_id = p.managed_anime_id)
  )
FROM bounds b`, dirs)
	if err != nil {
		return nil, fmt.Errorf("read subtree facts: %w", err)
	}
	for rows.Next() {
		var dir string
		var fact filemanager.SubtreeFact
		if err := rows.Scan(&dir, &fact.InProgress, &fact.Managed); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan subtree fact: %w", err)
		}
		result[dir] = fact
	}
	if err := closeRows(rows); err != nil {
		return nil, fmt.Errorf("read subtree facts: %w", err)
	}
	return result, nil
}

// SubtreeLockedPaths lists the managed or in-progress paths strictly below one
// directory, up to limit, in byte order. It answers the same question
// SubtreeFacts does for a listing, with the paths a refusal names instead of a
// boolean.
func (s *Store) SubtreeLockedPaths(ctx context.Context, dir string, limit int) ([]string, error) {
	prefix := strings.TrimSuffix(dir, "/") + "/"
	rows, err := s.db.QueryContext(ctx, `
WITH bounds AS (SELECT $1::text AS prefix, left($1::text, -1) || '0' AS upper_bound),
owners AS MATERIALIZED (SELECT o.path FROM (`+inProgressOwners+`) o WHERE o.path IS NOT NULL),
managed AS (
  SELECT p.physical_path AS path
  FROM placements p
  JOIN managed_anime ma ON ma.id = p.managed_anime_id
  WHERE p.state = 'active' AND COALESCE(ma.lifecycle_state, 'active') <> 'retired'
    AND NOT EXISTS (SELECT 1 FROM one_shot_acquisitions os WHERE os.managed_anime_id = p.managed_anime_id)
  UNION
  SELECT c.current_path FROM placement_companions c
  JOIN placements p ON p.id = c.placement_id
  JOIN managed_anime ma ON ma.id = p.managed_anime_id
  WHERE c.state = 'active' AND p.state = 'active' AND COALESCE(ma.lifecycle_state, 'active') <> 'retired'
    AND NOT EXISTS (SELECT 1 FROM one_shot_acquisitions os WHERE os.managed_anime_id = p.managed_anime_id)
)
SELECT path FROM (SELECT path FROM owners UNION SELECT path FROM managed) locked, bounds b
WHERE locked.path COLLATE "C" >= b.prefix AND locked.path COLLATE "C" < b.upper_bound
ORDER BY locked.path COLLATE "C" LIMIT $2`, prefix, limit)
	if err != nil {
		return nil, fmt.Errorf("read subtree locked paths: %w", err)
	}
	var paths []string
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan subtree locked path: %w", err)
		}
		paths = append(paths, path)
	}
	if err := closeRows(rows); err != nil {
		return nil, fmt.Errorf("read subtree locked paths: %w", err)
	}
	return paths, nil
}

// AnimePlacementPaths lists the current paths of one anime's active
// placements.
func (s *Store) AnimePlacementPaths(ctx context.Context, managedAnimeID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT physical_path FROM placements
WHERE managed_anime_id = $1 AND state = 'active' ORDER BY physical_path LIMIT 1000`, managedAnimeID)
	if err != nil {
		return nil, fmt.Errorf("read anime placements: %w", err)
	}
	var paths []string
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan anime placement: %w", err)
		}
		paths = append(paths, path)
	}
	if err := closeRows(rows); err != nil {
		return nil, fmt.Errorf("read anime placements: %w", err)
	}
	return paths, nil
}

// PlacementCompanions lists one video placement's companion relations.
func (s *Store) PlacementCompanions(ctx context.Context, placementID string) ([]filemanager.CompanionFact, error) {
	return queryCompanions(ctx, s.db, `c.placement_id = $1`, placementID)
}

func closeRows(rows *sql.Rows) error {
	err := rows.Err()
	if closeErr := rows.Close(); err == nil {
		err = closeErr
	}
	return err
}

// EpisodeEvidence reads the episode identity frozen on the file's acquisition.
// The placement's lineage is authoritative when the file has one; a file with
// no placement is read at its own path, through the completed operation that
// published it or its artifact file record at that source path. The identity
// is read from media_fact_files.effective_facts and never re-derived from the
// filename.
func (s *Store) EpisodeEvidence(ctx context.Context, placementID, path string) (*filemanager.EpisodeEvidence, error) {
	placementID, path = strings.TrimSpace(placementID), strings.TrimSpace(path)
	if placementID == "" && path == "" {
		return nil, nil
	}
	var raw []byte
	err := s.db.QueryRowContext(ctx, `WITH lineage AS (
 SELECT af.file_ref, rel.id AS release_id, p.created_at, p.id AS lineage_id
 FROM placements p
 JOIN file_operations fo ON fo.id=p.source_owner_ref AND p.source_evidence_type='file_operation'
 JOIN artifact_files af ON af.id=fo.artifact_file_id
 LEFT JOIN artifacts art ON art.id=af.artifact_id
 LEFT JOIN download_tasks dt ON dt.id=art.download_task_id
 LEFT JOIN releases rel ON rel.id=dt.release_id
 WHERE p.id=$1 AND $1<>''
 UNION ALL
 SELECT af.file_ref, rel.id, c.created_at, c.lineage_id
 FROM (
   SELECT fo.artifact_file_id, fo.created_at, fo.id AS lineage_id
   FROM file_operations fo
   WHERE $1='' AND $2<>'' AND fo.state='completed' AND fo.target_path=$2
   UNION ALL
   SELECT src.id, src.created_at, src.id
   FROM artifact_files src
   WHERE $1='' AND $2<>'' AND src.source_path=$2
 ) c
 JOIN artifact_files af ON af.id=c.artifact_file_id
 LEFT JOIN artifacts art ON art.id=af.artifact_id
 LEFT JOIN download_tasks dt ON dt.id=art.download_task_id
 LEFT JOIN releases rel ON rel.id=dt.release_id
)
SELECT COALESCE(mf.effective_facts->'identity','{}'::jsonb)
FROM lineage l
LEFT JOIN LATERAL (SELECT id FROM media_fact_snapshots WHERE release_id=l.release_id ORDER BY created_at DESC,id DESC LIMIT 1) snap ON true
LEFT JOIN media_fact_files mf ON mf.snapshot_id=snap.id AND mf.file_ref=l.file_ref
ORDER BY l.created_at DESC, l.lineage_id DESC
LIMIT 1`, placementID, path).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read episode evidence: %w", err)
	}
	var identity episodeidentity.EpisodeIdentity
	if json.Unmarshal(raw, &identity) != nil || !episodeStateKnown(identity.State) {
		return nil, nil
	}
	evidence := &filemanager.EpisodeEvidence{State: identity.State, Shape: identity.Shape}
	if identity.FailureCode != nil {
		evidence.FailureCode = *identity.FailureCode
	}
	if identity.RawEpisode != nil {
		evidence.RawValue = string(*identity.RawEpisode)
	}
	if !episodeShapeKnown(evidence.Shape) {
		// The contract's shape enum has no value for an identity that carries
		// none (a movie, or an identity frozen before a shape was recorded).
		evidence.Shape = ""
	}
	return evidence, nil
}

func episodeStateKnown(state string) bool {
	switch state {
	case episodeidentity.StateResolved, episodeidentity.StateUnresolved, episodeidentity.StateInvalid,
		episodeidentity.StateUnsupported, episodeidentity.StateConflict:
		return true
	}
	return false
}

func episodeShapeKnown(shape string) bool {
	switch shape {
	case episodeidentity.ShapeSingle, episodeidentity.ShapeRange, episodeidentity.ShapeList,
		episodeidentity.ShapeSeasonOnly, episodeidentity.ShapeSpecial, episodeidentity.ShapeUnknown:
		return true
	}
	return false
}

var _ filemanager.MatchAnimeReader = (*Store)(nil)

// MatchAnime reads what the file manager needs of an anime to match files into
// it: its title, whether it is retired or a one-shot's temporary anime, its
// subscription state and its TMDB binding. It returns nil for an anime that
// does not exist.
func (s *Store) MatchAnime(ctx context.Context, managedAnimeID string) (*filemanager.MatchAnimeFact, error) {
	var fact filemanager.MatchAnimeFact
	err := s.db.QueryRowContext(ctx, `SELECT ma.id, COALESCE(ma.title_override, ma.title, ''),
 ma.lifecycle_state IS DISTINCT FROM 'active', COALESCE(sub.state, ''),
 EXISTS (SELECT 1 FROM one_shot_acquisitions o WHERE o.managed_anime_id = ma.id),
 COALESCE(m.tmdb_work_id, ''), COALESCE(m.tmdb_media_kind, ''), COALESCE(m.media_kind_override, '')
FROM managed_anime ma
LEFT JOIN subscriptions sub ON sub.managed_anime_id = ma.id
LEFT JOIN anime_metadata m ON m.managed_anime_id = ma.id
WHERE ma.id = $1`, managedAnimeID).Scan(&fact.ID, &fact.Title, &fact.Retired, &fact.SubscriptionState, &fact.OneShot, &fact.TMDBWorkID, &fact.TMDBMediaKind, &fact.MediaKindOverride)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read match anime: %w", err)
	}
	return &fact, nil
}
