package filemaintenance

import (
	"context"
	"testing"

	"bansoko/internal/persistence/txscope"
	"bansoko/internal/testutil/disposabledb"
)

// TestFileEffectPolicySnapshotSemantics proves the instance-scoped snapshot
// contract: the singleton default is copied at subscribe time, the instance
// row is independent afterwards, and editing the default never changes
// existing instances.
func TestFileEffectPolicySnapshotSemantics(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `
INSERT INTO managed_anime(id, title) VALUES ('anime-fx', 'FX Anime')`); err != nil {
		t.Fatal(err)
	}
	service := NewFileEffectPolicyService(db)

	// 1) Missing default falls back to the code default (skip, version 0).
	def, err := service.GetDefault(ctx)
	if err != nil || def != nil {
		t.Fatalf("absent default=%v err=%v, want nil", def, err)
	}

	// 2) Copy at subscribe freezes skip with source version 0 (no default row).
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.CopyDefaultToInstanceTx(ctx, txscope.Wrap(tx), "anime-fx"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	instance, err := service.GetInstance(ctx, "anime-fx")
	if err != nil || instance == nil {
		t.Fatalf("instance=%v err=%v, want frozen row", instance, err)
	}
	if instance.UnclassifiedRoute != UnclassifiedRoutePolicyValueSkip || instance.SourceDefaultVersion != 0 || instance.Version != 1 {
		t.Fatalf("instance snapshot=%+v, want skip/source-0/version-1", instance)
	}

	// 3) The instance can be edited independently (CAS).
	saved, err := service.SaveInstance(ctx, "anime-fx", 1, UnclassifiedRoutePolicyValueRoute)
	if err != nil || saved.Version != 2 || saved.UnclassifiedRoute != UnclassifiedRoutePolicyValueRoute {
		t.Fatalf("save instance=%+v err=%v, want route/version-2", saved, err)
	}
	if _, err := service.SaveInstance(ctx, "anime-fx", 1, UnclassifiedRoutePolicyValueRoute); err != ErrPolicyConflict {
		t.Fatalf("stale instance CAS err=%v, want ErrPolicyConflict", err)
	}

	// 4) Editing the global default afterwards must not touch the instance.
	savedDefault, err := service.SaveDefault(ctx, 0, UnclassifiedRoutePolicyValueRoute)
	if err != nil || savedDefault.Version != 1 {
		t.Fatalf("save default=%+v err=%v, want version-1", savedDefault, err)
	}
	instance, err = service.GetInstance(ctx, "anime-fx")
	if err != nil || instance == nil {
		t.Fatalf("re-read instance err=%v", err)
	}
	if instance.UnclassifiedRoute != UnclassifiedRoutePolicyValueRoute || instance.Version != 2 {
		t.Fatalf("instance must stay frozen after default edit: %+v", instance)
	}

	// 5) A later subscribe copies the CURRENT default (route, version 1).
	if _, err := db.ExecContext(ctx, `
INSERT INTO managed_anime(id, title) VALUES ('anime-fx-2', 'FX Anime 2')`); err != nil {
		t.Fatal(err)
	}
	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.CopyDefaultToInstanceTx(ctx, txscope.Wrap(tx), "anime-fx-2"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	second, err := service.GetInstance(ctx, "anime-fx-2")
	if err != nil || second == nil {
		t.Fatalf("second instance err=%v", err)
	}
	if second.UnclassifiedRoute != UnclassifiedRoutePolicyValueRoute || second.SourceDefaultVersion != 1 {
		t.Fatalf("second instance=%+v, want route copied from default version 1", second)
	}
}
