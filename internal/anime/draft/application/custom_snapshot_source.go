package application

import (
	domain "bansoko/internal/anime/domain"
	"bansoko/internal/apperrors"
	catalog "bansoko/internal/catalog/application"
	"bansoko/internal/persistence/txscope"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

func (s *Service) ResolveCustomSnapshotSource(ctx context.Context, id string, revision, version int64) (catalog.FrozenCustomSnapshotSource, error) {
	if s == nil || s.db == nil {
		return catalog.FrozenCustomSnapshotSource{}, fmt.Errorf("custom revision database unavailable")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return catalog.FrozenCustomSnapshotSource{}, err
	}
	defer tx.Rollback()
	source, err := resolveCustomSourceTx(ctx, tx, id, revision, version)
	if err != nil {
		return source, err
	}
	return source, tx.Commit()
}
func (s *Service) ValidateCustomSnapshotSourceTx(ctx context.Context, scope txscope.Tx, id string, revision, version int64, expected catalog.FrozenCustomSnapshotSource) error {
	tx := scope.SQLTx()
	if tx == nil {
		return fmt.Errorf("custom source validation requires transaction")
	}
	source, err := resolveCustomSourceTx(ctx, tx, id, revision, version)
	if err != nil {
		return err
	}
	if source != expected {
		return &apperrors.Business{Status: 409, Code: "CUSTOM_SOURCE_CHANGED", Message: "Frozen custom source changed."}
	}
	return nil
}
func resolveCustomSourceTx(ctx context.Context, tx *sql.Tx, id string, revision, version int64) (catalog.FrozenCustomSnapshotSource, error) {
	var actualVersion int64
	var current sql.NullInt64
	var status string
	if err := tx.QueryRowContext(ctx, `SELECT version,current_revision,status FROM anime_config_candidates WHERE id=$1 FOR UPDATE`, id).Scan(&actualVersion, &current, &status); err != nil {
		return catalog.FrozenCustomSnapshotSource{}, err
	}
	// A subscribed preset is dormant: its frozen source still names the feed,
	// and browsing that feed for a one-shot writes nothing to the preset or the
	// subscription. Only an archived (retired) preset has no feed to browse.
	if actualVersion != version || !current.Valid || current.Int64 != revision || (status != "working" && status != "dormant") {
		return catalog.FrozenCustomSnapshotSource{}, &apperrors.Business{Status: 409, Code: "CUSTOM_CANDIDATE_CHANGED", Message: "Candidate version or revision changed."}
	}
	var raw []byte
	if err := tx.QueryRowContext(ctx, `SELECT draft FROM anime_config_candidate_revisions WHERE candidate_id=$1 AND revision=$2`, id, revision).Scan(&raw); err != nil {
		return catalog.FrozenCustomSnapshotSource{}, err
	}
	var d domain.AnimeDraft
	if err := json.Unmarshal(raw, &d); err != nil {
		return catalog.FrozenCustomSnapshotSource{}, err
	}
	if (d.Origin != domain.SubscriptionOriginCustom && d.Origin != domain.SubscriptionOriginOneshot) || len(d.RssSources) != 1 {
		return catalog.FrozenCustomSnapshotSource{}, &apperrors.Business{Status: 422, Code: "CUSTOM_SOURCE_REQUIRED", Message: "An exact custom candidate with one RSS source is required."}
	}
	source := d.RssSources[0]
	if string(source.SourceKind) != "custom_rss" || source.GroupName == "" {
		return catalog.FrozenCustomSnapshotSource{}, &apperrors.Business{Status: 422, Code: "CUSTOM_SOURCE_REQUIRED", Message: "One frozen custom RSS group is required."}
	}
	entry, group := catalog.CustomSnapshotScope(catalog.CandidateCustomSnapshotIdentity(id, revision), source.Locator, source.GroupName)
	return catalog.FrozenCustomSnapshotSource{Identity: entry, GroupRef: group, GroupName: source.GroupName, Locator: source.Locator}, nil
}
