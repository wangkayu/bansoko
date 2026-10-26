package animeconfig

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	animedomain "bansoko/internal/anime/domain"
)

// ExecQuerier is the read+write surface the business snapshot sync needs.
// *sql.DB, *sql.Tx and the txscope wrappers satisfy it.
type ExecQuerier interface {
	Querier
	QueryRowContext(context.Context, string, ...any) *sql.Row
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

// SyncInstanceBusinessSnapshotTx rewrites one instance's frozen business
// snapshot from its own anime_business_configs row and its stored section
// values, inside the caller's transaction. Global is never read.
//
// It is the ruling-3 invariant: one row is the single configuration source, so
// after a single-item owner save (rules, subgroups, retention, parser or
// file-effect policy) the stored snapshot must describe the same configuration
// the row now holds. Call it after the changed axis has been written.
//
// The snapshot is not a second source of truth. Sections are re-resolved from
// stored values the save wrote; a section whose
// value did not change keeps its stored provenance, so a one-axis save moves
// only that axis. The policy-axis *source default* versions come from the row's
// provenance columns, exactly as activation wrote them: they are not the row's
// CAS versions.
//
// Applying a candidate revision does not call this: that path stores the frozen
// revision verbatim so the acceptance digest check keeps matching.
//
// It is a no-op (empty digest) when the instance has no frozen business yet, or
// when a policy axis was never provisioned: there is then nothing to keep
// consistent and no complete snapshot to build.
func SyncInstanceBusinessSnapshotTx(ctx context.Context, q ExecQuerier, managedAnimeID string) (string, error) {
	var (
		parserKind, admissionMode, unclassifiedRoute       sql.NullString
		qualityProfile, snapshotRaw                        []byte
		parserVersion, retentionVersion, fileEffectVersion sql.NullInt64
	)
	err := q.QueryRowContext(ctx, `
SELECT parser_kind, parser_source_default_version, admission_mode, quality_profile,
       retention_source_default_version, unclassified_route, file_effect_source_default_version, business_snapshot
FROM anime_business_configs WHERE managed_anime_id = $1 FOR UPDATE`, managedAnimeID).
		Scan(&parserKind, &parserVersion, &admissionMode, &qualityProfile,
			&retentionVersion, &unclassifiedRoute, &fileEffectVersion, &snapshotRaw)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read business config for snapshot sync: %w", err)
	}
	if snapshotRaw == nil {
		return "", nil
	}
	if !parserKind.Valid || !parserVersion.Valid || !admissionMode.Valid || !retentionVersion.Valid ||
		!unclassifiedRoute.Valid || !fileEffectVersion.Valid || qualityProfile == nil {
		return "", errors.New("instance business snapshot requires every frozen policy axis")
	}
	var previous animedomain.BusinessSnapshot
	if err := json.Unmarshal(snapshotRaw, &previous); err != nil {
		return "", fmt.Errorf("decode stored business snapshot: %w", err)
	}
	// Sections are re-resolved from the stored values the caller just wrote. A
	// section whose value is unchanged keeps its stored
	// bytes and provenance, so a one-axis save cannot rewrite unrelated sections.
	resolved, err := LoadResolved(ctx, q, managedAnimeID)
	if err != nil {
		return "", err
	}
	sections := make(map[string]json.RawMessage, len(resolved.Sections))
	versions := make(map[string]int64, len(resolved.Sections))
	schemas := make(map[string]string, len(resolved.Sections))
	for name, value := range resolved.Sections {
		if stored, ok := previous.Sections[name]; ok && jsonValuesEqual(stored, value) {
			sections[name] = stored
			versions[name] = previous.SectionVersions[name]
			schemas[name] = previous.SectionSchemas[name]
			continue
		}
		sections[name] = value
		versions[name] = resolved.Versions[name]
		schemas[name] = resolved.SchemaVersions[name]
	}
	envelope := previous.Parser
	if envelope.Kind != parserKind.String {
		envelope.Kind = parserKind.String
		envelope.PolicyVersion = parserVersion.Int64
		envelope.SchemaVersion = animedomain.ParserPolicyEnvelopeSchemaVersion
		envelope.Provenance = "synced_from_instance_config"
		envelope.SourceOwner = "anime_business_configs"
	}
	next := animedomain.BusinessSnapshot{
		SchemaVersion:   animedomain.BusinessSnapshotSchemaVersion,
		Sections:        sections,
		SectionVersions: versions,
		SectionSchemas:  schemas,
		Parser:          envelope,
		Retention: animedomain.RetentionValue{
			AdmissionMode:  admissionMode.String,
			QualityProfile: append(json.RawMessage(nil), qualityProfile...),
		},
		RetentionVersion:  retentionVersion.Int64,
		UnclassifiedRoute: unclassifiedRoute.String,
		FileEffectVersion: fileEffectVersion.Int64,
		Programs:          previous.Programs,
	}
	digest, err := next.Digest()
	if err != nil {
		return "", fmt.Errorf("digest synced business snapshot: %w", err)
	}
	encoded, err := json.Marshal(next)
	if err != nil {
		return "", fmt.Errorf("encode synced business snapshot: %w", err)
	}
	if _, err := q.ExecContext(ctx, `
UPDATE anime_business_configs
SET business_snapshot = $2, business_digest = $3, business_version = business_version + 1, updated_at = NOW()
WHERE managed_anime_id = $1`, managedAnimeID, encoded, digest); err != nil {
		return "", fmt.Errorf("write synced business snapshot: %w", err)
	}
	return digest, nil
}

// jsonValuesEqual compares two stored JSON documents by value, so JSONB key
// order and whitespace cannot make an unchanged section look modified.
func jsonValuesEqual(left, right json.RawMessage) bool {
	var l, r any
	if json.Unmarshal(left, &l) != nil || json.Unmarshal(right, &r) != nil {
		return false
	}
	return reflect.DeepEqual(l, r)
}
