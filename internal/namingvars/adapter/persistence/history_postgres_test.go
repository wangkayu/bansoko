package persistence

import (
	"context"
	"errors"
	"testing"

	"bansoko/internal/namingvars"
	"bansoko/internal/testutil/disposabledb"
)

func TestHistoricalProgramReadDoesNotReactivate(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	repo := NewRepository(db)
	ctx := context.Background()
	old := `vars.release = {title: input.release_title};`
	newer := `vars.release = {title: "new"};`
	oldFP, _ := namingvars.SourceFingerprint(old)
	newFP, _ := namingvars.SourceFingerprint(newer)
	if err := repo.Publish(ctx, old, oldFP); err != nil {
		t.Fatal(err)
	}
	if err := repo.Publish(ctx, newer, newFP); err != nil {
		t.Fatal(err)
	}
	program, err := namingvars.NewStore(repo).LoadProgram(ctx, namingvars.ProgramRef{Version: namingvars.ProfileVersion(oldFP), Digest: oldFP})
	if err != nil {
		t.Fatal(err)
	}
	got, err := program.Evaluate(namingvars.Input{ReleaseTitle: "Historical release - 09"}, 0)
	if err != nil || got["release"]["title"] != "Historical release - 09" {
		t.Fatalf("historical evaluation: %v %v", got, err)
	}
	_, currentFP, err := repo.Current(ctx)
	if err != nil || currentFP != newFP || activeRowCount(t, repo) != 1 {
		t.Fatalf("historical read changed active: %s %v", currentFP, err)
	}
	if _, err := repo.SourceByFingerprint(ctx, "sha256:missing"); !errors.Is(err, namingvars.ErrProgramUnavailable) {
		t.Fatalf("missing: %v", err)
	}
}
