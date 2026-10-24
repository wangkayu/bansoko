package facts

import (
	"context"
	"database/sql"
	"errors"

	"bansoko/internal/persistence/txscope"
	"bansoko/internal/release/facts/adapter/persistence/media"
)

// MediaRepository is the application-facing media facts port. It describes the
// complete contract used by release evaluation, artifact inspection, file
// maintenance, file operation, and anime read models. Persistence details and
// SQL stay behind this boundary; transactional operations receive the
// caller-owned opaque scope.
type MediaRepository interface {
	CreateReleaseSnapshotTx(context.Context, txscope.Tx, Snapshot) error
	UpsertFileFactsTx(context.Context, txscope.Tx, FileRow) error
	ListFileRows(context.Context, string) ([]FileRow, error)
	ListFileRowsTx(context.Context, txscope.Tx, string) ([]FileRow, error)
	// ListFileRowsBySnapshots reads the file rows of several snapshots at once,
	// keyed by snapshot id.
	ListFileRowsBySnapshots(context.Context, []string) (map[string][]FileRow, error)
}

type Store struct {
	repository *persistence.Repository
}

var _ MediaRepository = (*Store)(nil)

// NewStoreWithRepository wires the application facade to its PostgreSQL
// persistence adapter. The adapter owns the database handle and all SQL.
func NewStoreWithRepository(repository *persistence.Repository) *Store {
	return &Store{repository: repository}
}

// NewStore is the PostgreSQL media facts store over db. Its transactional
// writes use only the caller's transaction.
func NewStore(db *sql.DB) *Store {
	return NewStoreWithRepository(persistence.NewRepository(db))
}

// CreateReleaseSnapshotTx persists one append-only Release-level snapshot in
// the caller-owned transaction.
func (s *Store) CreateReleaseSnapshotTx(ctx context.Context, scope txscope.Tx, snapshot Snapshot) error {
	if s == nil || s.repository == nil {
		return errors.New("media facts store is not configured")
	}
	return s.repository.CreateReleaseSnapshotTx(ctx, scope, persistence.Snapshot{
		ID:                      snapshot.ID,
		ReleaseID:               snapshot.ReleaseID,
		SchemaVersion:           snapshot.SchemaVersion,
		ResolverVersion:         snapshot.ResolverVersion,
		ParserKind:              snapshot.ParserKind,
		ParserConfigFingerprint: snapshot.ParserConfigFingerprint,
		ParserConfig:            snapshot.ParserConfig,
		InputDigest:             snapshot.InputDigest,
		ReleaseIdentity:         snapshot.ReleaseIdentity,
		ReleaseFacts: persistence.ReleaseFacts{
			Title:            snapshot.ReleaseFacts.Title,
			GroupName:        snapshot.ReleaseFacts.GroupName,
			Resolution:       snapshot.ReleaseFacts.Resolution,
			Quality:          snapshot.ReleaseFacts.Quality,
			Languages:        snapshot.ReleaseFacts.Languages,
			Special:          snapshot.ReleaseFacts.Special,
			Warnings:         snapshot.ReleaseFacts.Warnings,
			EpisodeEvidence:  snapshot.ReleaseFacts.EpisodeEvidence,
			MediaKind:        snapshot.ReleaseFacts.MediaKind,
			Season:           snapshot.ReleaseFacts.Season,
			SeasonBound:      snapshot.ReleaseFacts.SeasonBound,
			BGMWorkID:        snapshot.ReleaseFacts.BGMWorkID,
			TMDBWorkID:       snapshot.ReleaseFacts.TMDBWorkID,
			ParserProvenance: snapshot.ReleaseFacts.ParserProvenance,
			FileKindsVersion: snapshot.ReleaseFacts.FileKindsVersion,
			BatchPlan:        snapshot.ReleaseFacts.BatchPlan,
		},
		CreatedAt: snapshot.CreatedAt,
	})
}

// UpsertFileFactsTx persists one per-file facts row in the caller-owned
// transaction.
func (s *Store) UpsertFileFactsTx(ctx context.Context, scope txscope.Tx, row FileRow) error {
	if s == nil || s.repository == nil {
		return errors.New("media facts store is not configured")
	}
	return s.repository.UpsertFileFactsTx(ctx, scope, persistence.FileRow{
		SnapshotID:        row.SnapshotID,
		FileRef:           row.FileRef,
		MediaKind:         row.MediaKind,
		SizeBytes:         row.SizeBytes,
		SourceFingerprint: row.SourceFingerprint,
		FileEvidence: persistence.FileEvidence{
			Name:       row.FileEvidence.Name,
			MediaKind:  row.FileEvidence.MediaKind,
			SizeBytes:  row.FileEvidence.SizeBytes,
			GroupName:  row.FileEvidence.GroupName,
			Resolution: row.FileEvidence.Resolution,
			Languages:  row.FileEvidence.Languages,
			Revision:   row.FileEvidence.Revision,
			Special:    row.FileEvidence.Special,
		},
		EffectiveFacts: persistence.EffectiveFileFacts{
			Identity: row.EffectiveFacts.Identity,
			Quality: persistence.QualityFacts{
				Group:      row.EffectiveFacts.Quality.Group,
				Languages:  row.EffectiveFacts.Quality.Languages,
				Resolution: row.EffectiveFacts.Quality.Resolution,
				Source:     row.EffectiveFacts.Quality.Source,
				Revision:   row.EffectiveFacts.Quality.Revision,
				SizeBytes:  row.EffectiveFacts.Quality.SizeBytes,
				States:     row.EffectiveFacts.Quality.States,
			},
			MappingVersion: row.EffectiveFacts.MappingVersion,
		},
		CreatedAt: row.CreatedAt,
	})
}

// ReadSnapshot returns the latest Release-level snapshot for a release.
func (s *Store) ReadSnapshot(ctx context.Context, releaseID string) (*Snapshot, error) {
	if s == nil || s.repository == nil {
		return nil, errors.New("media facts store is not configured")
	}
	value, err := s.repository.ReadSnapshot(ctx, releaseID)
	if err != nil || value == nil {
		return nil, err
	}
	result := Snapshot{
		ID:                      value.ID,
		ReleaseID:               value.ReleaseID,
		SchemaVersion:           value.SchemaVersion,
		ResolverVersion:         value.ResolverVersion,
		ParserKind:              value.ParserKind,
		ParserConfigFingerprint: value.ParserConfigFingerprint,
		ParserConfig:            value.ParserConfig,
		InputDigest:             value.InputDigest,
		ReleaseIdentity:         value.ReleaseIdentity,
		ReleaseFacts: ReleaseFacts{
			Title:            value.ReleaseFacts.Title,
			GroupName:        value.ReleaseFacts.GroupName,
			Resolution:       value.ReleaseFacts.Resolution,
			Quality:          value.ReleaseFacts.Quality,
			Languages:        value.ReleaseFacts.Languages,
			Special:          value.ReleaseFacts.Special,
			Warnings:         value.ReleaseFacts.Warnings,
			EpisodeEvidence:  value.ReleaseFacts.EpisodeEvidence,
			MediaKind:        value.ReleaseFacts.MediaKind,
			Season:           value.ReleaseFacts.Season,
			SeasonBound:      value.ReleaseFacts.SeasonBound,
			BGMWorkID:        value.ReleaseFacts.BGMWorkID,
			TMDBWorkID:       value.ReleaseFacts.TMDBWorkID,
			ParserProvenance: value.ReleaseFacts.ParserProvenance,
			FileKindsVersion: value.ReleaseFacts.FileKindsVersion,
			BatchPlan:        value.ReleaseFacts.BatchPlan,
		},
		CreatedAt: value.CreatedAt,
	}
	return &result, nil
}

// ListFileRows returns every per-file facts row of a snapshot.
func (s *Store) ListFileRows(ctx context.Context, snapshotID string) ([]FileRow, error) {
	if s == nil || s.repository == nil {
		return nil, errors.New("media facts store is not configured")
	}
	values, err := s.repository.ListFileRows(ctx, snapshotID)
	if err != nil {
		return nil, err
	}
	return mapFileRows(values), nil
}

// ListFileRowsTx returns every per-file facts row through the caller-owned
// transaction.
func (s *Store) ListFileRowsTx(ctx context.Context, scope txscope.Tx, snapshotID string) ([]FileRow, error) {
	if s == nil || s.repository == nil {
		return nil, errors.New("media facts store is not configured")
	}
	values, err := s.repository.ListFileRowsTx(ctx, scope, snapshotID)
	if err != nil {
		return nil, err
	}
	return mapFileRows(values), nil
}

// ListFileRowsBySnapshots returns the per-file facts rows of several snapshots in
// one read, keyed by snapshot id.
func (s *Store) ListFileRowsBySnapshots(ctx context.Context, snapshotIDs []string) (map[string][]FileRow, error) {
	if s == nil || s.repository == nil {
		return nil, errors.New("media facts store is not configured")
	}
	values, err := s.repository.ListFileRowsBySnapshots(ctx, snapshotIDs)
	if err != nil {
		return nil, err
	}
	result := make(map[string][]FileRow, len(values))
	for id, rows := range values {
		result[id] = mapFileRows(rows)
	}
	return result, nil
}

func mapFileRows(values []persistence.FileRow) []FileRow {
	result := make([]FileRow, 0, len(values))
	for _, value := range values {
		result = append(result, FileRow{
			SnapshotID:        value.SnapshotID,
			FileRef:           value.FileRef,
			MediaKind:         value.MediaKind,
			SizeBytes:         value.SizeBytes,
			SourceFingerprint: value.SourceFingerprint,
			FileEvidence: FileEvidence{
				Name:       value.FileEvidence.Name,
				MediaKind:  value.FileEvidence.MediaKind,
				SizeBytes:  value.FileEvidence.SizeBytes,
				GroupName:  value.FileEvidence.GroupName,
				Resolution: value.FileEvidence.Resolution,
				Languages:  value.FileEvidence.Languages,
				Revision:   value.FileEvidence.Revision,
				Special:    value.FileEvidence.Special,
			},
			EffectiveFacts: EffectiveFileFacts{
				Identity: value.EffectiveFacts.Identity,
				Quality: QualityFacts{
					Group:      value.EffectiveFacts.Quality.Group,
					Languages:  value.EffectiveFacts.Quality.Languages,
					Resolution: value.EffectiveFacts.Quality.Resolution,
					Source:     value.EffectiveFacts.Quality.Source,
					Revision:   value.EffectiveFacts.Quality.Revision,
					SizeBytes:  value.EffectiveFacts.Quality.SizeBytes,
					States:     value.EffectiveFacts.Quality.States,
				},
				MappingVersion: value.EffectiveFacts.MappingVersion,
			},
			CreatedAt: value.CreatedAt,
		})
	}
	return result
}
