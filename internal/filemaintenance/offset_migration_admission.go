package filemaintenance

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"

	"bansoko/internal/persistence/txscope"
)

// OffsetMigrationAdmissionService atomically admits durable pending generations
// inside the policy owner's existing CAS transaction.
type OffsetMigrationAdmissionService struct{}

// NewOffsetMigrationAdmissionService wires the transaction-only admission owner.
func NewOffsetMigrationAdmissionService() *OffsetMigrationAdmissionService {
	return &OffsetMigrationAdmissionService{}
}

// AdmitRSSSourceTx admits one RSS link offset migration generation through the
// opaque application transaction boundary. The RSS link is the only offset
// owner, so it is the only migration scope.
func (s *OffsetMigrationAdmissionService) AdmitRSSSourceTx(ctx context.Context, scope txscope.Tx, animeID, rssSourceID string, sourceVersion int64, oldOffset, newOffset int) (string, bool, error) {
	return s.admitScopeTx(ctx, scope, OffsetMigrationScopeRSSSource, animeID, rssSourceID, sourceVersion, oldOffset, newOffset)
}

func (s *OffsetMigrationAdmissionService) admitScopeTx(ctx context.Context, scope txscope.Tx, scopeKind OffsetMigrationScopeKind, animeID, scopeOwnerID string, policyVersion int64, oldOffset, newOffset int) (string, bool, error) {
	tx := scope.SQLTx()
	if tx == nil {
		return "", false, errors.New("offset migration admission transaction is nil")
	}
	return s.admitTx(ctx, tx, scopeKind, animeID, scopeOwnerID, policyVersion, oldOffset, newOffset)
}

func (s *OffsetMigrationAdmissionService) admitTx(ctx context.Context, tx *sql.Tx, scopeKind OffsetMigrationScopeKind, animeID, scopeOwnerID string, policyVersion int64, oldOffset, newOffset int) (string, bool, error) {
	if oldOffset == newOffset {
		return "", false, nil
	}
	generationID := offsetMigrationGenerationID(scopeKind, scopeOwnerID, policyVersion)
	result, err := tx.ExecContext(ctx, `
INSERT INTO offset_migration_generations
(id, managed_anime_id, scope_kind, scope_owner_id, policy_version, old_offset, new_offset, state)
VALUES ($1, $2, $3, $4, $5, $6, $7, 'pending')
ON CONFLICT (scope_kind, scope_owner_id, policy_version) DO NOTHING`,
		generationID, animeID, scopeKind, scopeOwnerID, policyVersion, oldOffset, newOffset)
	if err != nil {
		return "", false, fmt.Errorf("admit offset migration generation: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return "", false, fmt.Errorf("read offset migration admission result: %w", err)
	}

	var existing OffsetMigrationGeneration
	if err := tx.QueryRowContext(ctx, `
SELECT id, managed_anime_id, old_offset, new_offset
FROM offset_migration_generations
WHERE scope_kind = $1 AND scope_owner_id = $2 AND policy_version = $3`,
		scopeKind, scopeOwnerID, policyVersion).Scan(
		&existing.ID, &existing.ManagedAnimeID, &existing.OldOffset, &existing.NewOffset); err != nil {
		return "", false, fmt.Errorf("verify offset migration admission: %w", err)
	}
	if existing.ID != generationID || existing.ManagedAnimeID != animeID || existing.OldOffset != oldOffset || existing.NewOffset != newOffset {
		return "", false, fmt.Errorf("offset migration admission idempotency conflict for %s %q version %d", scopeKind, scopeOwnerID, policyVersion)
	}
	return existing.ID, rows == 1, nil
}

func offsetMigrationGenerationID(scopeKind OffsetMigrationScopeKind, scopeOwnerID string, policyVersion int64) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%d", scopeKind, scopeOwnerID, policyVersion)))
	return "offgen:" + hex.EncodeToString(digest[:])
}
