package parserpolicy

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"bansoko/internal/apperrors"
	"bansoko/internal/parser"
)

// ErrInstanceParserPolicyRequired is the typed integrity failure every
// production path raises when the per-instance parser policy row it needs is
// missing or unusable. No runtime path may substitute the code default for a
// missing instance row: the automatic pipeline would then execute the bundled
// regex parser the instance never selected.
var ErrInstanceParserPolicyRequired = errors.New("instance parser policy is required")

// InstancePolicyRequiredError carries the offending ManagedAnime and the exact
// reason the frozen policy could not be used. It unwraps to the typed 409
// BUSINESS error the HTTP boundary maps, so a leaked mutation returns the
// stable INSTANCE_PARSER_POLICY_REQUIRED code instead of a generic 500.
type InstancePolicyRequiredError struct {
	ManagedAnimeID string
	Detail         string
}

func (e *InstancePolicyRequiredError) Error() string {
	detail := strings.TrimSpace(e.Detail)
	if detail == "" {
		detail = "no anime_business_configs parser axis"
	}
	return fmt.Sprintf("instance parser policy for %q is required: %s", e.ManagedAnimeID, detail)
}

// Is reports the sentinel identity so callers can fail closed with
// errors.Is(err, ErrInstanceParserPolicyRequired) without matching text.
func (e *InstancePolicyRequiredError) Is(target error) bool {
	return target == ErrInstanceParserPolicyRequired
}

// Unwrap exposes the typed transport error the HTTP boundary already maps.
func (e *InstancePolicyRequiredError) Unwrap() error {
	return &apperrors.Business{
		Status: 409, Code: "INSTANCE_PARSER_POLICY_REQUIRED",
		Message: e.Error(),
	}
}

// RowQuerier is the read surface the shared loader needs. *sql.DB, *sql.Tx and
// the application packages' own queryer interfaces satisfy it.
type RowQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// LoadInstanceRequired reads the one frozen per-instance parser policy and
// fails closed when it is absent or unusable. It is the single loader every
// production path shares, so RSS evaluation, RSS refresh, the one-shot/manual
// helper and the RSS source offset patch cannot drift into their own fallback.
//
// Unusable means an empty kind, a kind the authoritative recognition registry
// does not know. Both are integrity faults: the row exists but cannot produce a parser selection.
func LoadInstanceRequired(ctx context.Context, q RowQuerier, managedAnimeID string) (Instance, error) {
	var (
		kind                 string
		sourceDefaultVersion int64
		version              int64
	)
	err := q.QueryRowContext(ctx, `
SELECT parser_kind, parser_source_default_version, parser_version
FROM anime_business_configs WHERE managed_anime_id = $1 AND parser_version IS NOT NULL`, managedAnimeID).
		Scan(&kind, &sourceDefaultVersion, &version)
	if errors.Is(err, sql.ErrNoRows) {
		return Instance{}, &InstancePolicyRequiredError{ManagedAnimeID: managedAnimeID}
	}
	if err != nil {
		return Instance{}, fmt.Errorf("read instance parser policy: %w", err)
	}
	return InstanceFromRow(managedAnimeID, kind, sourceDefaultVersion, version)
}

// InstanceFromRow validates one already-scanned instance_parser_policies row
// into the frozen policy value. Callers that join the policy table themselves
// use it so the validation stays identical to LoadInstanceRequired.
func InstanceFromRow(managedAnimeID, kind string, sourceDefaultVersion, version int64) (Instance, error) {
	policy := Policy{Kind: parser.ParserKind(strings.TrimSpace(kind))}
	if verr := Validate(policy); verr != nil {
		return Instance{}, &InstancePolicyRequiredError{ManagedAnimeID: managedAnimeID, Detail: verr.Error()}
	}
	return Instance{
		ManagedAnimeID:       managedAnimeID,
		Policy:               policy,
		SourceDefaultVersion: sourceDefaultVersion,
		Version:              version,
	}, nil
}
