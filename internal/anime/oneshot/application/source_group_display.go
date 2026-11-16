package application

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/apperrors"
)

// sourceGroupDisplayName resolves the display name of the selected seed's
// source group: the value a normal subscription records in rss_sources.group_name
// and every naming render reads back as anime.group. It is a display name, never
// the provider's group identity (a Mikan id, an AniBT slug, or a custom-group
// digest), which is display-blind and must never reach a path name.
//
// A custom seed's identity is a digest of the draft's own frozen group name
// (catalogapplication.CustomSnapshotScope), so the draft's single custom source
// is the recorded name. A provider seed names its group by identity only: the
// group observation discovery recorded — the same provider read a normal
// subscription's display name comes from — supplies the name the provider
// published for that exact group.
//
// A name that cannot be resolved fails the admission closed instead of falling
// back to the identity: a placeholder source has to carry a display name, and an
// identity in its place is exactly the defect this resolver exists to prevent.
func sourceGroupDisplayName(ctx context.Context, tx *sql.Tx, draft animedomain.AnimeDraft, seed SelectedSeed) (string, error) {
	if seed.Provider == animedomain.ProviderKindCustom {
		if len(draft.RssSources) == 1 && draft.RssSources[0].SourceKind == animedomain.SourceKindCustomRss {
			if name := strings.TrimSpace(draft.RssSources[0].GroupName); name != "" {
				return name, nil
			}
		}
		return "", sourceGroupUnavailableError()
	}
	if tx == nil {
		return "", sourceGroupUnavailableError()
	}
	// The latest observation of the seed's catalog entry, never a provider read:
	// the seed's own snapshot was persisted from an observation of the same entry.
	name, err := recordedSourceGroupName(ctx, tx, string(seed.Provider), seed.ProviderEntryRef, seed.ProviderGroupRef)
	if err != nil || name == "" {
		return "", sourceGroupUnavailableError()
	}
	return name, nil
}

// recordedSourceGroupName reads the display name one provider published for one
// group of one catalog entry, from the newest recorded group observation. The
// observation is a whole-observation write, so a group the newest one no longer
// carries has no recorded name: an older observation listed it while it was still
// published, and that superseded listing is not the provider's answer today.
func recordedSourceGroupName(ctx context.Context, tx *sql.Tx, provider, entryRef, groupRef string) (string, error) {
	var name sql.NullString
	err := tx.QueryRowContext(ctx, `
SELECT btrim(e->>'display_name')
FROM (SELECT entries FROM provider_group_snapshots
      WHERE provider = $1 AND catalog_entry_ref = $2
      ORDER BY version DESC
      LIMIT 1) newest
CROSS JOIN LATERAL jsonb_array_elements(newest.entries) e
WHERE e->>'group_ref' = $3
LIMIT 1`, provider, entryRef, groupRef).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(name.String), nil
}

// sourceGroupUnavailableError is the one typed refusal of an admission whose
// source group has no recorded display name.
func sourceGroupUnavailableError() error {
	return &apperrors.Business{Status: 422, Code: "SOURCE_GROUP_UNAVAILABLE", Message: "所选种子的来源组没有可用的显示名，请重新发现分组后再试。"}
}
