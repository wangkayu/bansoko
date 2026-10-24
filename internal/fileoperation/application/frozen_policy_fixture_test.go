package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"

	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/testutil/frozenpolicy"
)

// completeFrozenPolicyFixture returns the complete frozen policy a real
// DownloadTask admission writes. Post-download file-effect preparation reads
// this snapshot as its only configuration authority, so a fixture that seeds a
// download task with a sparse policy would fail closed by design.
func completeFrozenPolicyFixture(t *testing.T) string {
	t.Helper()
	return frozenpolicy.MustJSON()
}

// rewriteFrozenPolicy replaces part of the persisted DownloadTask frozen policy.
// A post-download file effect reads only that snapshot (never current
// globals/instance config), so a test that wants a different operation, naming
// rule, or filter must change the frozen value.
func rewriteFrozenPolicy(t *testing.T, db *sql.DB, taskID string, mutate func(policy map[string]any)) {
	t.Helper()
	ctx := context.Background()
	var raw []byte
	if err := db.QueryRowContext(ctx, `SELECT frozen_policy FROM download_tasks WHERE id = $1`, taskID).Scan(&raw); err != nil {
		t.Fatalf("read frozen policy for %s: %v", taskID, err)
	}
	var policy map[string]any
	if err := json.Unmarshal(raw, &policy); err != nil {
		t.Fatalf("decode frozen policy for %s: %v", taskID, err)
	}
	mutate(policy)
	encoded, err := frozenpolicy.Encode(policy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE download_tasks SET frozen_policy = $2 WHERE id = $1`, taskID, encoded); err != nil {
		t.Fatalf("write frozen policy for %s: %v", taskID, err)
	}
}

// setFrozenFileEffect sets the frozen file_effect section of one seeded task.
func setFrozenFileEffect(t *testing.T, db *sql.DB, taskID, operation, collisionPolicy string) {
	t.Helper()
	rewriteFrozenPolicy(t, db, taskID, func(policy map[string]any) {
		policy["effective_sections"].(map[string]any)[animedomain.SectionFileEffect] = map[string]any{
			"operation": operation, "collision_policy": collisionPolicy,
		}
	})
}

// setFrozenSectionValue sets one frozen resolved section of one seeded task.
func setFrozenSectionValue(t *testing.T, db *sql.DB, taskID, section string, value any) {
	t.Helper()
	rewriteFrozenPolicy(t, db, taskID, func(policy map[string]any) {
		policy["effective_sections"].(map[string]any)[section] = value
	})
}
