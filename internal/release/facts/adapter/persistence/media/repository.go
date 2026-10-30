// Package persistence implements the PostgreSQL adapter for release media facts.
// It owns SQL and database handles while exposing adapter-private values to the
// application-facing facade.
package persistence

import (
	"bansoko/internal/batchplan"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/sqlc-dev/pqtype"

	"bansoko/internal/episodeidentity"
	"bansoko/internal/parser"
	"bansoko/internal/persistence/txscope"
	generated "bansoko/internal/release/facts/adapter/persistence/media/generated"
)

// ReleaseFacts is the adapter-private representation of release facts.
type ReleaseFacts struct {
	Title           string                           `json:"title"`
	GroupName       string                           `json:"group_name,omitempty"`
	Resolution      string                           `json:"resolution,omitempty"`
	Quality         string                           `json:"quality,omitempty"`
	Languages       []string                         `json:"languages,omitempty"`
	Special         bool                             `json:"special,omitempty"`
	Warnings        []string                         `json:"warnings,omitempty"`
	EpisodeEvidence *episodeidentity.EpisodeEvidence `json:"episode_evidence,omitempty"`
	// Metadata bindings (media kind/season and work IDs) survive adapter
	// mapping as frozen facts. Dropping them would re-open post-submission drift.
	MediaKind   string `json:"media_kind,omitempty"`
	Season      int    `json:"season,omitempty"`
	SeasonBound bool   `json:"season_bound,omitempty"`
	BGMWorkID   string `json:"bgm_work_id,omitempty"`
	TMDBWorkID  string `json:"tmdb_work_id,omitempty"`
	// ParserProvenance is the frozen selected-parser evidence. It must survive
	// the adapter mapping: dropping it would silently degrade every downstream
	// owner (Artifact, FileOperation, retry) to the identity offset and erase the
	// typed parse result.
	ParserProvenance *parser.Provenance `json:"parser_provenance,omitempty"`
	// FileKindsVersion is the product file kind list version frozen with the
	// snapshot; retry, recovery and preview classify files with it.
	FileKindsVersion string `json:"file_kinds_version,omitempty"`
	// BatchPlan is the per-file plan conclusion accepted with the Release,
	// including the frozen unknown-file route.
	BatchPlan *batchplan.Plan `json:"batch_plan,omitempty"`
}

// FileEvidence is the adapter-private representation of file evidence.
type FileEvidence struct {
	Name       string   `json:"name"`
	MediaKind  string   `json:"media_kind"`
	SizeBytes  int64    `json:"size_bytes"`
	GroupName  string   `json:"group_name,omitempty"`
	Resolution string   `json:"resolution,omitempty"`
	Languages  []string `json:"languages,omitempty"`
	Revision   string   `json:"revision,omitempty"`
	Special    bool     `json:"special,omitempty"`
}

// QualityFacts is the adapter-private representation of quality facts.
type QualityFacts struct {
	Group      string            `json:"group,omitempty"`
	Languages  []string          `json:"languages,omitempty"`
	Resolution string            `json:"resolution,omitempty"`
	Source     string            `json:"source,omitempty"`
	Revision   string            `json:"revision,omitempty"`
	SizeBytes  int64             `json:"size_bytes"`
	States     map[string]string `json:"states,omitempty"`
}

// EffectiveFileFacts is the adapter-private representation of effective facts.
type EffectiveFileFacts struct {
	Identity       episodeidentity.EpisodeIdentity `json:"identity"`
	Quality        QualityFacts                    `json:"quality"`
	MappingVersion string                          `json:"mapping_version"`
}

// Snapshot is the adapter-private persistence value.
type Snapshot struct {
	ID                      string
	ReleaseID               string
	SchemaVersion           string
	ResolverVersion         string
	ParserKind              string
	ParserConfigFingerprint string
	ParserConfig            json.RawMessage
	InputDigest             string
	ReleaseIdentity         episodeidentity.EpisodeIdentity
	ReleaseFacts            ReleaseFacts
	CreatedAt               time.Time
}

// FileRow is the adapter-private persistence value.
type FileRow struct {
	SnapshotID        string
	FileRef           string
	MediaKind         string
	SizeBytes         int64
	SourceFingerprint string
	FileEvidence      FileEvidence
	EffectiveFacts    EffectiveFileFacts
	CreatedAt         time.Time
}

// Repository owns release media facts PostgreSQL reads and writes. Write methods use
// only the caller-owned transaction scope; this adapter never begins,
// commits, or rolls back a transaction.
type Repository struct {
	db *sql.DB
}

// NewRepository wires the PostgreSQL release media facts adapter.
func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db}
}

// CreateReleaseSnapshotTx reuses the latest snapshot when every frozen fact is
// unchanged. A changed snapshot remains append-only; only id and creation time
// are excluded from equality. The per-release lock also covers concurrent writers.
func (r *Repository) CreateReleaseSnapshotTx(ctx context.Context, scope txscope.Tx, snapshot Snapshot) error {
	tx := scope.SQLTx()
	if tx == nil {
		return errors.New("media facts snapshot transaction is nil")
	}
	identityRaw, err := json.Marshal(snapshot.ReleaseIdentity)
	if err != nil {
		return fmt.Errorf("encode release identity: %w", err)
	}
	factsRaw, err := json.Marshal(snapshot.ReleaseFacts)
	if err != nil {
		return fmt.Errorf("encode release facts: %w", err)
	}
	now := snapshot.CreatedAt
	if now.IsZero() {
		now = time.Now().UTC()
	}
	queries := generated.New(tx)
	if err := queries.LockReleaseSnapshot(ctx, snapshot.ReleaseID); err != nil {
		return err
	}
	if err := queries.CreateReleaseSnapshot(ctx, generated.CreateReleaseSnapshotParams{
		ID:                      snapshot.ID,
		ReleaseID:               snapshot.ReleaseID,
		SchemaVersion:           firstNonEmpty(snapshot.SchemaVersion, "media-facts-v1"),
		ResolverVersion:         snapshot.ResolverVersion,
		ParserKind:              snapshot.ParserKind,
		ParserConfigFingerprint: nullableString(snapshot.ParserConfigFingerprint),
		ParserConfig:            nullableRawMessage(snapshot.ParserConfig),
		InputDigest:             snapshot.InputDigest,
		ReleaseIdentity:         identityRaw,
		ReleaseFacts:            factsRaw,
		CreatedAt:               now,
	}); err != nil {
		return err
	}
	return nil
}

var _ interface {
	CreateReleaseSnapshotTx(context.Context, txscope.Tx, Snapshot) error
	UpsertFileFactsTx(context.Context, txscope.Tx, FileRow) error
	ReadSnapshot(context.Context, string) (*Snapshot, error)
	ListFileRows(context.Context, string) ([]FileRow, error)
	ListFileRowsTx(context.Context, txscope.Tx, string) ([]FileRow, error)
	ListFileRowsBySnapshots(context.Context, []string) (map[string][]FileRow, error)
} = (*Repository)(nil)

// UpsertFileFactsTx persists one per-file facts row in the caller-owned
// transaction. The (snapshot_id, file_ref) pair is the stable identity.
func (r *Repository) UpsertFileFactsTx(ctx context.Context, scope txscope.Tx, row FileRow) error {
	tx := scope.SQLTx()
	if tx == nil {
		return errors.New("media facts file facts transaction is nil")
	}
	evidenceRaw, err := json.Marshal(row.FileEvidence)
	if err != nil {
		return fmt.Errorf("encode file evidence: %w", err)
	}
	effectiveRaw, err := json.Marshal(row.EffectiveFacts)
	if err != nil {
		return fmt.Errorf("encode effective facts: %w", err)
	}
	now := row.CreatedAt
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if err := generated.New(tx).UpsertFileFacts(ctx, generated.UpsertFileFactsParams{
		SnapshotID:        row.SnapshotID,
		FileRef:           row.FileRef,
		MediaKind:         row.MediaKind,
		SizeBytes:         row.SizeBytes,
		SourceFingerprint: nullableString(row.SourceFingerprint),
		FileEvidence:      evidenceRaw,
		EffectiveFacts:    effectiveRaw,
		CreatedAt:         now,
	}); err != nil {
		return err
	}
	return nil
}

// ReadSnapshot returns the latest Release-level snapshot for a release.
func (r *Repository) ReadSnapshot(ctx context.Context, releaseID string) (*Snapshot, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("media facts database is nil")
	}
	row, err := generated.New(r.db).ReadLatestSnapshot(ctx, releaseID)
	return snapshotFromGenerated(row, err)
}

// ListFileRows returns every per-file facts row of a snapshot.
func (r *Repository) ListFileRows(ctx context.Context, snapshotID string) ([]FileRow, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("media facts database is nil")
	}
	return r.listFileRows(ctx, r.db, snapshotID)
}

// ListFileRowsTx returns every per-file facts row through the caller-owned
// transaction.
func (r *Repository) ListFileRowsTx(ctx context.Context, scope txscope.Tx, snapshotID string) ([]FileRow, error) {
	tx := scope.SQLTx()
	if tx == nil {
		return nil, errors.New("media facts file facts transaction is nil")
	}
	return r.listFileRows(ctx, tx, snapshotID)
}

// ListFileRowsBySnapshots returns the per-file facts rows of several snapshots in
// one read, keyed by snapshot id (a snapshot without rows has no entry).
func (r *Repository) ListFileRowsBySnapshots(ctx context.Context, snapshotIDs []string) (map[string][]FileRow, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("media facts database is nil")
	}
	result := map[string][]FileRow{}
	if len(snapshotIDs) == 0 {
		return result, nil
	}
	rows, err := generated.New(r.db).ListFileRowsBySnapshots(ctx, snapshotIDs)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		mapped, err := fileRowFromGenerated(row)
		if err != nil {
			return nil, err
		}
		result[mapped.SnapshotID] = append(result[mapped.SnapshotID], mapped)
	}
	return result, nil
}

func (r *Repository) listFileRows(ctx context.Context, queryer generated.DBTX, snapshotID string) ([]FileRow, error) {
	rows, err := generated.New(queryer).ListFileRows(ctx, snapshotID)
	if err != nil {
		return nil, err
	}
	result := make([]FileRow, 0, len(rows))
	for _, row := range rows {
		mapped, err := fileRowFromGenerated(row)
		if err != nil {
			return nil, err
		}
		result = append(result, mapped)
	}
	return result, nil
}

func fileRowFromGenerated(row generated.MediaFactFiles) (FileRow, error) {
	mapped := FileRow{
		SnapshotID:        row.SnapshotID,
		FileRef:           row.FileRef,
		MediaKind:         row.MediaKind,
		SizeBytes:         row.SizeBytes,
		SourceFingerprint: row.SourceFingerprint.String,
		CreatedAt:         row.CreatedAt,
	}
	if err := json.Unmarshal(row.FileEvidence, &mapped.FileEvidence); err != nil {
		return FileRow{}, fmt.Errorf("decode file evidence %s: %w", row.FileRef, err)
	}
	if err := json.Unmarshal(row.EffectiveFacts, &mapped.EffectiveFacts); err != nil {
		return FileRow{}, fmt.Errorf("decode effective facts %s: %w", row.FileRef, err)
	}
	return mapped, nil
}

func snapshotFromGenerated(row generated.ReadLatestSnapshotRow, err error) (*Snapshot, error) {
	if err != nil {
		return nil, err
	}
	snapshot := Snapshot{
		ID:                      row.ID,
		ReleaseID:               row.ReleaseID,
		SchemaVersion:           row.SchemaVersion,
		ResolverVersion:         row.ResolverVersion,
		ParserKind:              row.ParserKind,
		ParserConfigFingerprint: row.ParserConfigFingerprint.String,
		ParserConfig:            rawMessage(row.ParserConfig),
		InputDigest:             row.InputDigest,
		CreatedAt:               row.CreatedAt,
	}
	if err := json.Unmarshal(row.ReleaseIdentity, &snapshot.ReleaseIdentity); err != nil {
		return nil, fmt.Errorf("decode release identity: %w", err)
	}
	if err := json.Unmarshal(row.ReleaseFacts, &snapshot.ReleaseFacts); err != nil {
		return nil, fmt.Errorf("decode release facts: %w", err)
	}
	return &snapshot, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func nullableString(value string) sql.NullString {
	return sql.NullString{String: value, Valid: value != ""}
}

func nullableRawMessage(value json.RawMessage) pqtype.NullRawMessage {
	if len(value) == 0 {
		return pqtype.NullRawMessage{}
	}
	return pqtype.NullRawMessage{RawMessage: value, Valid: true}
}

func rawMessage(value pqtype.NullRawMessage) json.RawMessage {
	if !value.Valid {
		return nil
	}
	return append(json.RawMessage(nil), value.RawMessage...)
}
