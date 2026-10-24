package persistence

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"bansoko/internal/namingvars"
	generated "bansoko/internal/namingvars/adapter/persistence/generated"
)

type versionRow struct {
	Revision    int64
	Fingerprint string
	Content     json.RawMessage
	Origin      string
	Note        string
	CreatedAt   time.Time
	ActivatedAt sql.NullTime
	Active      bool
}

func (r versionRow) info(sourceBytes int) namingvars.VersionInfo {
	info := namingvars.VersionInfo{
		Revision: r.Revision, Digest: r.Fingerprint, Origin: r.Origin, Note: r.Note,
		SourceBytes: sourceBytes, CreatedAt: r.CreatedAt.UTC(), Active: r.Active,
	}
	if r.ActivatedAt.Valid {
		at := r.ActivatedAt.Time.UTC()
		info.ActivatedAt = &at
	}
	return info
}

// version decodes and verifies a stored row: the content is the canonical
// source and hashes to the stored fingerprint, or the row is refused.
func (r versionRow) version() (namingvars.Version, error) {
	var source string
	if err := json.Unmarshal(r.Content, &source); err != nil || source == "" {
		return namingvars.Version{}, errors.New("naming vars content is not source-backed")
	}
	canonical, err := namingvars.CanonicalSource(source)
	if err != nil {
		return namingvars.Version{}, err
	}
	actual, err := namingvars.SourceFingerprint(source)
	if err != nil || actual != r.Fingerprint || string(canonical) != source {
		return namingvars.Version{}, errors.New("naming vars fingerprint/content mismatch")
	}
	return namingvars.Version{VersionInfo: r.info(len(source)), Source: source}, nil
}

func (r *Repository) ready() error {
	if r == nil || r.db == nil {
		return errors.New("naming vars database is nil")
	}
	return nil
}

// ActiveVersion returns the active version with its source.
func (r *Repository) ActiveVersion(ctx context.Context) (namingvars.Version, error) {
	if err := r.ready(); err != nil {
		return namingvars.Version{}, err
	}
	row, err := generated.New(r.db).GetActiveNamingVarsVersion(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return namingvars.Version{}, namingvars.ErrVersionNotFound
	}
	if err != nil {
		return namingvars.Version{}, fmt.Errorf("read active naming vars: %w", err)
	}
	return versionRow(row).version()
}

// VersionByRevision returns one published version with its source.
func (r *Repository) VersionByRevision(ctx context.Context, revision int64) (namingvars.Version, error) {
	if err := r.ready(); err != nil {
		return namingvars.Version{}, err
	}
	row, err := generated.New(r.db).GetNamingVarsVersionByRevision(ctx, revision)
	if errors.Is(err, sql.ErrNoRows) {
		return namingvars.Version{}, namingvars.ErrVersionNotFound
	}
	if err != nil {
		return namingvars.Version{}, fmt.Errorf("read naming vars version: %w", err)
	}
	return versionRow(row).version()
}

// ListVersions returns the newest versions first.
func (r *Repository) ListVersions(ctx context.Context, limit int) ([]namingvars.VersionInfo, error) {
	if err := r.ready(); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > namingvars.VersionListLimit {
		limit = namingvars.VersionListLimit
	}
	rows, err := generated.New(r.db).ListNamingVarsVersions(ctx, int32(limit))
	if err != nil {
		return nil, fmt.Errorf("list naming vars versions: %w", err)
	}
	out := make([]namingvars.VersionInfo, 0, len(rows))
	for _, row := range rows {
		out = append(out, versionRow{
			Revision: row.Revision, Fingerprint: row.Fingerprint, Origin: row.Origin, Note: row.Note,
			CreatedAt: row.CreatedAt, ActivatedAt: row.ActivatedAt, Active: row.Active,
		}.info(int(row.SourceBytes)))
	}
	return out, nil
}

func validateSaveParams(params namingvars.SaveParams) ([]byte, error) {
	canonical, err := namingvars.CanonicalSource(params.Source)
	if err != nil {
		return nil, err
	}
	fingerprint, err := namingvars.SourceFingerprint(params.Source)
	if err != nil {
		return nil, err
	}
	if params.Fingerprint != fingerprint || string(canonical) != params.Source {
		return nil, fmt.Errorf("naming vars fingerprint mismatch: supplied %q, canonical %q", params.Fingerprint, fingerprint)
	}
	switch params.Origin {
	case namingvars.OriginFile, namingvars.OriginEditor, namingvars.OriginImport:
	default:
		return nil, fmt.Errorf("naming vars origin %q is not recognised", params.Origin)
	}
	content, err := json.Marshal(string(canonical))
	if err != nil {
		return nil, fmt.Errorf("encode naming vars source: %w", err)
	}
	return content, nil
}

// lockedActive reads the active row under the publish lock; found is false
// when no version is active.
func lockedActive(ctx context.Context, queries *generated.Queries) (row versionRow, found bool, err error) {
	if err = queries.LockNamingVarsPublish(ctx); err != nil {
		return row, false, fmt.Errorf("lock naming vars publish: %w", err)
	}
	active, err := queries.GetActiveNamingVarsVersionForUpdate(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return row, false, nil
	}
	if err != nil {
		return row, false, fmt.Errorf("read active naming vars: %w", err)
	}
	return versionRow(active), true, nil
}

func conflictFrom(active versionRow, found bool) error {
	if !found {
		return &namingvars.ConflictError{}
	}
	var source string
	_ = json.Unmarshal(active.Content, &source)
	return &namingvars.ConflictError{Current: active.info(len(source))}
}

// SaveVersion publishes a source and moves the active pointer to it when
// ExpectedRevision is still active. A source that already is a version is
// activated again; its content, note and origin never change.
func (r *Repository) SaveVersion(ctx context.Context, params namingvars.SaveParams) (namingvars.VersionInfo, error) {
	if err := r.ready(); err != nil {
		return namingvars.VersionInfo{}, err
	}
	content, err := validateSaveParams(params)
	if err != nil {
		return namingvars.VersionInfo{}, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return namingvars.VersionInfo{}, fmt.Errorf("begin naming vars save: %w", err)
	}
	defer tx.Rollback()
	queries := generated.New(tx)
	active, found, err := lockedActive(ctx, queries)
	if err != nil {
		return namingvars.VersionInfo{}, err
	}
	var activeRevision int64
	if found {
		activeRevision = active.Revision
	}
	if activeRevision != params.ExpectedRevision {
		return namingvars.VersionInfo{}, conflictFrom(active, found)
	}
	existing, err := queries.GetNamingVarsVersionByFingerprint(ctx, params.Fingerprint)
	switch {
	case err == nil:
		row := versionRow(existing)
		// The stored identity must hold this exact body.
		var stored string
		if json.Unmarshal(row.Content, &stored) != nil || stored != params.Source {
			return namingvars.VersionInfo{}, errors.New("naming vars identity is held by different content")
		}
		if !row.Active {
			if found {
				if err = queries.DeactivateNamingVars(ctx); err != nil {
					return namingvars.VersionInfo{}, fmt.Errorf("deactivate naming vars: %w", err)
				}
			}
			if err = queries.ActivateNamingVarsRevision(ctx, row.Revision); err != nil {
				return namingvars.VersionInfo{}, fmt.Errorf("activate naming vars: %w", err)
			}
		}
		activated, err := generated.New(tx).GetNamingVarsVersionByFingerprint(ctx, params.Fingerprint)
		if err != nil {
			return namingvars.VersionInfo{}, fmt.Errorf("read activated naming vars: %w", err)
		}
		if err = tx.Commit(); err != nil {
			return namingvars.VersionInfo{}, fmt.Errorf("commit naming vars save: %w", err)
		}
		return versionRow(activated).info(len(params.Source)), nil
	case errors.Is(err, sql.ErrNoRows):
	default:
		return namingvars.VersionInfo{}, fmt.Errorf("read naming vars identity: %w", err)
	}
	if found {
		if err = queries.DeactivateNamingVars(ctx); err != nil {
			return namingvars.VersionInfo{}, fmt.Errorf("deactivate naming vars: %w", err)
		}
	}
	inserted, err := queries.InsertNamingVarsVersion(ctx, generated.InsertNamingVarsVersionParams{
		Fingerprint: params.Fingerprint, Content: content, Origin: params.Origin, Note: params.Note,
	})
	if err != nil {
		return namingvars.VersionInfo{}, fmt.Errorf("insert naming vars version: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return namingvars.VersionInfo{}, fmt.Errorf("commit naming vars save: %w", err)
	}
	return versionRow{
		Revision: inserted.Revision, Fingerprint: inserted.Fingerprint, Origin: params.Origin, Note: params.Note,
		CreatedAt: inserted.CreatedAt, ActivatedAt: inserted.ActivatedAt, Active: true,
	}.info(len(params.Source)), nil
}

// ActivateRevision moves the active pointer to a published revision when
// expected is still active.
func (r *Repository) ActivateRevision(ctx context.Context, expected, target int64) (namingvars.VersionInfo, error) {
	if err := r.ready(); err != nil {
		return namingvars.VersionInfo{}, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return namingvars.VersionInfo{}, fmt.Errorf("begin naming vars activation: %w", err)
	}
	defer tx.Rollback()
	queries := generated.New(tx)
	active, found, err := lockedActive(ctx, queries)
	if err != nil {
		return namingvars.VersionInfo{}, err
	}
	var activeRevision int64
	if found {
		activeRevision = active.Revision
	}
	if activeRevision != expected {
		return namingvars.VersionInfo{}, conflictFrom(active, found)
	}
	row, err := queries.GetNamingVarsVersionByRevision(ctx, target)
	if errors.Is(err, sql.ErrNoRows) {
		return namingvars.VersionInfo{}, namingvars.ErrVersionNotFound
	}
	if err != nil {
		return namingvars.VersionInfo{}, fmt.Errorf("read naming vars version: %w", err)
	}
	if !row.Active {
		if found {
			if err = queries.DeactivateNamingVars(ctx); err != nil {
				return namingvars.VersionInfo{}, fmt.Errorf("deactivate naming vars: %w", err)
			}
		}
		if err = queries.ActivateNamingVarsRevision(ctx, target); err != nil {
			return namingvars.VersionInfo{}, fmt.Errorf("activate naming vars: %w", err)
		}
	}
	activated, err := queries.GetNamingVarsVersionByRevision(ctx, target)
	if err != nil {
		return namingvars.VersionInfo{}, fmt.Errorf("read activated naming vars: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return namingvars.VersionInfo{}, fmt.Errorf("commit naming vars activation: %w", err)
	}
	version, err := versionRow(activated).version()
	if err != nil {
		return namingvars.VersionInfo{}, err
	}
	return version.VersionInfo, nil
}

// Seed publishes a source as the first active version, only when none is
// active; otherwise it changes nothing.
func (r *Repository) Seed(ctx context.Context, params namingvars.SaveParams) (bool, error) {
	if err := r.ready(); err != nil {
		return false, err
	}
	content, err := validateSaveParams(params)
	if err != nil {
		return false, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin naming vars seed: %w", err)
	}
	defer tx.Rollback()
	queries := generated.New(tx)
	_, found, err := lockedActive(ctx, queries)
	if err != nil {
		return false, err
	}
	if found {
		return false, nil
	}
	existing, err := queries.GetNamingVarsVersionByFingerprint(ctx, params.Fingerprint)
	switch {
	case err == nil:
		var stored string
		if json.Unmarshal(existing.Content, &stored) != nil || stored != params.Source {
			return false, errors.New("naming vars identity is held by different content")
		}
		if err = queries.ActivateNamingVarsRevision(ctx, existing.Revision); err != nil {
			return false, fmt.Errorf("activate naming vars: %w", err)
		}
	case errors.Is(err, sql.ErrNoRows):
		if _, err = queries.InsertNamingVarsVersion(ctx, generated.InsertNamingVarsVersionParams{
			Fingerprint: params.Fingerprint, Content: content, Origin: params.Origin, Note: params.Note,
		}); err != nil {
			return false, fmt.Errorf("insert naming vars version: %w", err)
		}
	default:
		return false, fmt.Errorf("read naming vars identity: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return false, fmt.Errorf("commit naming vars seed: %w", err)
	}
	return true, nil
}

var _ namingvars.VersionedPersistence = (*Repository)(nil)
