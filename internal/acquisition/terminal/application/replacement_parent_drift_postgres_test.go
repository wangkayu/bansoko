package application

import (
	"context"
	"database/sql"
	"testing"
)

func verifyReplacementApprovalParentDrift(t *testing.T, owner, runtimeDB *sql.DB, path string) {
	t.Helper()
	cases := []struct{ name, change, restore string }{
		{"policy", `UPDATE anime_business_configs SET retention_version=retention_version+1 WHERE managed_anime_id='anime-1'`, `UPDATE anime_business_configs SET retention_version=1 WHERE managed_anime_id='anime-1'`},
		{"subscription", `UPDATE subscriptions SET version=version+1 WHERE id='sub-1'`, `UPDATE subscriptions SET version=1 WHERE id='sub-1'`},
		{"decision", `UPDATE replacement_decisions SET state='settled' WHERE id='decision'`, `UPDATE replacement_decisions SET state='admitted' WHERE id='decision'`},
	}
	for _, c := range cases {
		t.Run("drift_"+c.name, func(t *testing.T) {
			ctx := context.Background()
			capture, err := CaptureReplacement(ctx, runtimeDB, "release", path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = owner.ExecContext(ctx, c.change); err != nil {
				t.Fatal(err)
			}
			tx, err := runtimeDB.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			_, err = capture.recheck(ctx, tx, "release", "anime-1")
			tx.Rollback()
			if err == nil {
				t.Fatal("changed approval parent accepted")
			}
			if _, err = owner.ExecContext(ctx, c.restore); err != nil {
				t.Fatal(err)
			}
		})
	}
}
