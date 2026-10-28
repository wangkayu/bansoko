package application

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/persistence/txscope"
)

// Provisioner writes the policy and configuration owner rows shared by direct
// Subscribe, candidate activation and one-shot admission. Caller owns the
// transaction. Every per-anime axis is one-time input: an instance takes its
// retention, parser and file-effect axes from the business snapshot its draft
// froze when it was created, never from the Global defaults at subscribe time.
type Provisioner struct {
	frozenParser FrozenParserPolicyPort
}

// NewProvisioner configures the frozen parser-policy port the parser axis is
// written through.
func NewProvisioner(frozenParser FrozenParserPolicyPort) Provisioner {
	return Provisioner{frozenParser: frozenParser}
}

// ProvisionBusinessAxesTx writes the instance's retention, parser and
// file-effect axes from business. It is the one axis provisioning path: direct
// Subscribe, candidate activation and one-shot admission all call it. It never
// reads Global.
func (p Provisioner) ProvisionBusinessAxesTx(ctx context.Context, tx *sql.Tx, animeID string, business animedomain.BusinessSnapshot) (animedomain.BusinessSnapshot, error) {
	if tx == nil {
		return animedomain.BusinessSnapshot{}, errors.New("owner provisioning requires a caller-owned transaction")
	}
	frozen, err := business.Freeze()
	if err != nil {
		return animedomain.BusinessSnapshot{}, fmt.Errorf("business snapshot: %w", err)
	}
	if frozen.ReplacementVersion < 1 {
		return animedomain.BusinessSnapshot{}, errors.New("frozen retention provenance must have a persisted version")
	}
	if p.frozenParser == nil {
		return animedomain.BusinessSnapshot{}, errors.New("frozen parser provisioning port is required")
	}
	if err := p.frozenParser.CopyFrozenParserPolicyToInstanceTx(ctx, txscope.Wrap(tx), animeID, frozen.Parser.Kind, frozen.Parser.PolicyVersion); err != nil {
		return animedomain.BusinessSnapshot{}, fmt.Errorf("create instance parser policy from the business snapshot: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE anime_business_configs
SET admission_mode = $2, quality_profile = $3,
    replacement_source_default_version = $4, replacement_version = 1,
    unclassified_route = $5, file_effect_source_default_version = $6, file_effect_version = 1,
    updated_at = NOW()
WHERE managed_anime_id = $1`,
		animeID, frozen.Replacement.AdmissionMode, []byte(frozen.Replacement.QualityProfile), frozen.ReplacementVersion,
		frozen.UnclassifiedRoute, frozen.FileEffectVersion); err != nil {
		return animedomain.BusinessSnapshot{}, fmt.Errorf("create instance retention and file effect axes: %w", err)
	}
	return frozen, nil
}

// ProvisionOwnerSetTx writes the axes from business and the complete resolved
// section values. Caller owns the transaction.
func (p Provisioner) ProvisionOwnerSetTx(ctx context.Context, tx *sql.Tx, animeID string, resolved animedomain.ResolvedSections, business animedomain.BusinessSnapshot) error {
	if _, err := p.ProvisionBusinessAxesTx(ctx, tx, animeID, business); err != nil {
		return err
	}
	return materializeResolvedOverrides(ctx, tx, animeID, resolved)
}

// InitializeAnimeOwnerRowsTx creates the shared base metadata and config-owner
// rows for a newly inserted ManagedAnime. The caller owns the transaction.
func (p Provisioner) InitializeAnimeOwnerRowsTx(ctx context.Context, tx *sql.Tx, animeID string, metadataDraft animedomain.AnimeMetadataDraft) (AnimeMetadata, error) {
	if tx == nil {
		return AnimeMetadata{}, errors.New("owner initialization requires a caller-owned transaction")
	}
	// The owner media kind is backfilled from the TMDB tuple the draft binds
	// while it is unset, so a bound tuple always comes with a kind.
	mediaKind := metadataDraft.BackfilledMediaKind()
	metadata := AnimeMetadata{
		ManagedAnimeID:    animeID,
		BGM:               applicationBGMFromDomain(metadataDraft.Bgm),
		TMDB:              applicationTMDBFromDomain(metadataDraft.Tmdb),
		TotalEpisodes:     CloneInt(metadataDraft.TotalEpisodes),
		MediaKindOverride: CloneString(mediaKind),
		Year:              CloneInt(metadataDraft.Year),
		Season:            CloneInt(metadataDraft.Season),
		Version:           1,
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO anime_metadata(managed_anime_id, bgm_work_id, tmdb_work_id, tmdb_media_kind, tmdb_season, media_kind_override, total_episodes, year, season)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		animeID, nullableTrimmed(bgmWorkID(metadataDraft.Bgm)), nullableTrimmed(tmdbWorkID(metadataDraft.Tmdb)), nullableString(tmdbMediaKind(metadataDraft.Tmdb)), nullableInt(tmdbSeason(metadataDraft.Tmdb)), nullableOptionalText(mediaKind), nullableIntPtr(metadataDraft.TotalEpisodes), nullableIntPtr(metadataDraft.Year), nullableIntPtr(metadataDraft.Season)); err != nil {
		return AnimeMetadata{}, fmt.Errorf("create anime metadata: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO anime_business_configs(managed_anime_id) VALUES ($1)`, animeID); err != nil {
		return AnimeMetadata{}, fmt.Errorf("create business config row: %w", err)
	}
	return metadata, nil
}
