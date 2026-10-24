package persistence

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"bansoko/internal/namingvars"
)

// SourceByFingerprint reuses retained immutable rows without changing active.
// SQL and JSONB decoding stay at the persistence boundary. The caller verifies
// the canonical source digest before constructing an evaluable Program.
func (r *Repository) SourceByFingerprint(ctx context.Context, fingerprint string) (string, error) {
	if r == nil || r.db == nil {
		return "", errors.New("naming vars database is nil")
	}
	var content []byte
	err := r.db.QueryRowContext(ctx, `SELECT content FROM naming_vars_versions WHERE fingerprint = $1`, fingerprint).Scan(&content)
	if errors.Is(err, sql.ErrNoRows) {
		return "", namingvars.ErrProgramUnavailable
	}
	if err != nil {
		return "", fmt.Errorf("read historical naming vars source: %w", err)
	}
	var source string
	if err := json.Unmarshal(content, &source); err != nil || source == "" {
		return "", errors.New("historical naming vars content is not source-backed")
	}
	return source, nil
}

var _ namingvars.HistoricalSourceReader = (*Repository)(nil)
