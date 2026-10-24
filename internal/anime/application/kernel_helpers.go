package application

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	acquisitiontarget "bansoko/internal/acquisition/target"
	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/apperrors"
	"bansoko/internal/parser"
	"bansoko/internal/parserpolicy"
	"bansoko/internal/redact"
)

// ResolveInstanceParserEvidenceTx loads and resolves parser evidence inside the caller's transaction.
func ResolveInstanceParserEvidenceTx(ctx context.Context, tx *sql.Tx, animeID, title string, effective animedomain.EffectiveConfig, tmdb *animedomain.NullableTMDBBinding, mediaKindOverride *string) (acquisitiontarget.ParserEvidence, error) {
	policy, err := parserpolicy.LoadInstanceRequired(ctx, tx, animeID)
	if err != nil {
		return acquisitiontarget.ParserEvidence{}, err
	}
	mediaKind, season, seasonBound := animedomain.FrozenMediaBinding(tmdb, mediaKindOverride)
	return acquisitiontarget.ResolveParserEvidence(ctx, acquisitiontarget.ParserSelectionInput{
		Kind:            string(policy.Policy.Kind),
		EpisodePatterns: effective.Rules.Parser.EpisodePatterns,
		MediaKind:       mediaKind,
		Season:          season,
		SeasonBound:     seasonBound,
		PolicyVersion:   policy.Version,
		ProfileVersion:  effective.Versions[animedomain.SectionParser],
	}, title)
}

func resolveInstanceParserEvidenceTx(ctx context.Context, tx *sql.Tx, animeID, title string, effective animedomain.EffectiveConfig, tmdb *animedomain.NullableTMDBBinding, mediaKindOverride *string) (acquisitiontarget.ParserEvidence, error) {
	return ResolveInstanceParserEvidenceTx(ctx, tx, animeID, title, effective, tmdb, mediaKindOverride)
}

func cloneStringPointer(value *string) *string {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}

// CloneStringSlice copies a string slice, preserving nil versus empty.
func CloneStringSlice(value []string) []string {
	if value == nil {
		return nil
	}
	result := make([]string, len(value))
	copy(result, value)
	return result
}

func cloneStringSlice(value []string) []string { return CloneStringSlice(value) }

func canonicalJSONHash(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var normalized any
	if err := decoder.Decode(&normalized); err != nil {
		return "", err
	}
	canonical, err := marshalCanonicalJSON(normalized)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

// canonicalAnimeDraftHash digests an already-validated draft. It also
// re-verifies stored revision drafts, so it applies the stored-read rules (a
// title dictionary refresh never breaks a saved revision's digest check).
func canonicalAnimeDraftHash(draft animedomain.AnimeDraft) (string, error) {
	if violations := animedomain.ValidateStoredDraft(draft); len(violations) > 0 {
		return "", fmt.Errorf("invalid anime draft: %s", violations[0].Message)
	}
	return canonicalJSONHash(draft)
}

func marshalCanonicalJSON(value any) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	canonical := bytes.TrimSuffix(buffer.Bytes(), []byte{'\n'})
	canonical = normalizeJSONUnicodeSeparators(canonical)
	return canonical, nil
}

func normalizeJSONUnicodeSeparators(value []byte) []byte {
	result := make([]byte, 0, len(value))
	inString := false
	escaped := false
	for index := 0; index < len(value); index++ {
		current := value[index]
		if !inString {
			if current == '"' {
				inString = true
			}
			result = append(result, current)
			continue
		}
		if escaped {
			if current == 'u' && index+4 < len(value) {
				hexdigits := value[index+1 : index+5]
				if bytes.Equal(hexdigits, []byte("2028")) {
					result = result[:len(result)-1]
					result = append(result, []byte("\xe2\x80\xa8")...)
					index += 4
					escaped = false
					continue
				}
				if bytes.Equal(hexdigits, []byte("2029")) {
					result = result[:len(result)-1]
					result = append(result, []byte("\xe2\x80\xa9")...)
					index += 4
					escaped = false
					continue
				}
			}
			escaped = false
			result = append(result, current)
			continue
		}
		if current == '\\' {
			escaped = true
			result = append(result, current)
			continue
		}
		if current == '"' {
			inString = false
		}
		result = append(result, current)
	}
	return result
}

func safeReason(raw string) string {
	safe, _ := redact.Apply(raw, 512, redact.Mask, redact.Redacted)
	return safe
}

// NullableOptionalText trims optional text and maps blank values to SQL NULL.
func NullableOptionalText(value *string) any {
	if value == nil || strings.TrimSpace(*value) == "" {
		return nil
	}
	return strings.TrimSpace(*value)
}

func nullableOptionalText(value *string) any { return NullableOptionalText(value) }

// FixedTitle is the managed anime's fixed title (managed_anime.title_override)
// written when the anime is created: the draft's title override, else its
// title. Both are trimmed; it is nil only when both are blank, which draft
// validation already refuses.
func FixedTitle(title string, titleOverride *string) *string {
	for _, value := range []*string{titleOverride, &title} {
		if value != nil && strings.TrimSpace(*value) != "" {
			trimmed := strings.TrimSpace(*value)
			return &trimmed
		}
	}
	return nil
}

// ValidateIdempotencyKey validates the length of a request idempotency key.
func ValidateIdempotencyKey(key string) error {
	trimmed := strings.TrimSpace(key)
	if len(trimmed) < 8 || len(trimmed) > 128 {
		return &apperrors.BadRequest{Code: "IDEMPOTENCY_KEY_REQUIRED", Message: "Idempotency-Key must be between 8 and 128 characters."}
	}
	return nil
}

// DefaultMaxAttempts is the deployment retry bound for anime-owned work.
const DefaultMaxAttempts = 5

const defaultMaxAttempts = DefaultMaxAttempts

// LoadGlobalParserPolicyEnvelopeTx freezes the parser-policy authority a
// candidate save resolves: the global creation default at save time. A missing
// singleton falls back to the versioned code default (reltitle, version 1) so a save never depends on unset state. The kind's registration is
// the global row's own write contract; an unregistered stored kind fails closed
// here rather than fabricating a selection. The caller owns the transaction.
func LoadGlobalParserPolicyEnvelopeTx(ctx context.Context, tx *sql.Tx, parserSectionVersion int64) (animedomain.ParserPolicyEnvelope, error) {
	var (
		kind    string
		version int64
	)
	err := tx.QueryRowContext(ctx, `
SELECT value->>'parser_kind', version
FROM global_config_sections WHERE section_name = 'parser_policy'`).Scan(&kind, &version)
	if errors.Is(err, sql.ErrNoRows) {
		kind = string(parser.ParserReltitle)
		version = 1
	} else if err != nil {
		return animedomain.ParserPolicyEnvelope{}, fmt.Errorf("read global parser policy default: %w", err)
	}
	if !parser.IsRegistered(parser.ParserKind(kind)) {
		return animedomain.ParserPolicyEnvelope{}, &apperrors.Business{
			Status: 422, Code: "POLICY_INVALID",
			Message: "the global parser policy kind is not a registered parser",
		}
	}
	if version < 1 {
		version = 1
	}
	envelope := animedomain.ParserPolicyEnvelope{
		Kind:              kind,
		PolicyVersion:     version,
		Provenance:        "frozen_at_candidate_save",
		ProfileVersion:    parserSectionVersion,
		RulesOwnerVersion: 0,
		SchemaVersion:     animedomain.ParserPolicyEnvelopeSchemaVersion,
		SourceOwner:       "global_parser_policy_default",
	}
	if err := envelope.Validate(); err != nil {
		return animedomain.ParserPolicyEnvelope{}, err
	}
	return envelope, nil
}

// NullableTrimmed maps a blank trimmed string to SQL NULL.
func NullableTrimmed(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func nullableTrimmed(value string) any { return NullableTrimmed(value) }

// NullableString maps an empty string to SQL NULL.
func NullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullableString(value string) any { return NullableString(value) }

// NullableInt maps a negative integer to SQL NULL.
func NullableInt(value int) any {
	if value < 0 {
		return nil
	}
	return value
}

func nullableInt(value int) any { return NullableInt(value) }

// NullableValueChanged reports whether the next optional text differs from the stored value.
func NullableValueChanged(current sql.NullString, next string) bool {
	if !current.Valid {
		return strings.TrimSpace(next) != ""
	}
	return current.String != next
}

func nullableValueChanged(current sql.NullString, next string) bool {
	return NullableValueChanged(current, next)
}

// NullableIntChanged reports whether the next optional integer differs from the stored value.
func NullableIntChanged(current sql.NullInt64, next int) bool {
	if !current.Valid {
		return next >= 0
	}
	return current.Int64 != int64(next)
}

func nullableIntChanged(current sql.NullInt64, next int) bool {
	return NullableIntChanged(current, next)
}

// BGMWorkID projects the optional BGM work id, empty when unbound.
func BGMWorkID(bgm *animedomain.BGMBinding) string {
	if bgm == nil {
		return ""
	}
	return bgm.WorkId
}

func bgmWorkID(bgm *animedomain.BGMBinding) string { return BGMWorkID(bgm) }

// TMDBWorkID projects the optional TMDB work id, empty when unbound.
func TMDBWorkID(tmdb *animedomain.TMDBBinding) string {
	if tmdb == nil {
		return ""
	}
	return tmdb.WorkId
}

func tmdbWorkID(tmdb *animedomain.TMDBBinding) string { return TMDBWorkID(tmdb) }

// TMDBMediaKind projects the optional TMDB media kind.
func TMDBMediaKind(tmdb *animedomain.TMDBBinding) string {
	if tmdb == nil {
		return ""
	}
	return string(tmdb.MediaKind)
}

func tmdbMediaKind(tmdb *animedomain.TMDBBinding) string { return TMDBMediaKind(tmdb) }

// TMDBSeason projects the optional TMDB season, -1 when unbound.
func TMDBSeason(tmdb *animedomain.TMDBBinding) int {
	if tmdb == nil {
		return -1
	}
	return tmdb.Season
}

func tmdbSeason(tmdb *animedomain.TMDBBinding) int { return TMDBSeason(tmdb) }

// ApplicationBGMFromDomain projects a domain BGM binding onto the owner shape.
func ApplicationBGMFromDomain(value *animedomain.BGMBinding) *BGMBinding {
	if value == nil {
		return nil
	}
	return &BGMBinding{WorkID: value.WorkId}
}

func applicationBGMFromDomain(value *animedomain.BGMBinding) *BGMBinding {
	return ApplicationBGMFromDomain(value)
}

// ApplicationTMDBFromDomain projects a domain TMDB binding onto the owner shape.
func ApplicationTMDBFromDomain(value *animedomain.TMDBBinding) *TMDBBinding {
	if value == nil {
		return nil
	}
	return &TMDBBinding{WorkID: value.WorkId, MediaKind: string(value.MediaKind), Season: value.Season}
}

func applicationTMDBFromDomain(value *animedomain.TMDBBinding) *TMDBBinding {
	return ApplicationTMDBFromDomain(value)
}

// CloneString copies an optional string without sharing its storage.
func CloneString(value *string) *string {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneString(value *string) *string { return CloneString(value) }

// CloneTime copies an optional time without sharing its storage.
func CloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneTime(value *time.Time) *time.Time { return CloneTime(value) }

// CloneInt copies an optional integer without sharing its storage.
func CloneInt(value *int) *int {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneInt(value *int) *int { return CloneInt(value) }

// NullableIntPtr maps a nil optional integer to SQL NULL.
func NullableIntPtr(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullableIntPtr(value *int) any { return NullableIntPtr(value) }

// NullableIntPtrChanged reports whether the next optional integer differs from the stored value.
func NullableIntPtrChanged(current sql.NullInt64, next *int) bool {
	if next == nil {
		return current.Valid
	}
	return !current.Valid || current.Int64 != int64(*next)
}

func nullableIntPtrChanged(current sql.NullInt64, next *int) bool {
	return NullableIntPtrChanged(current, next)
}
