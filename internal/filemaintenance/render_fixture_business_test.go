package filemaintenance

import (
	"bansoko/internal/namingvars"
	varspersistence "bansoko/internal/namingvars/adapter/persistence"
	"bansoko/internal/testutil/frozenpolicy"
	"context"
	"database/sql"
	"encoding/json"
	"path"
	"strconv"
	"testing"
)

// freezeRenderFixtureBusiness persists real historical programs, evaluates the
// operation's exact release and relative file evidence, and adds the admitted
// output without altering binding, rules, fingerprints or placement identity.
// Call after seedPlacedRenderChain; reusable by other maintenance fixture owners.
func freezeRenderFixtureBusiness(t *testing.T, db *sql.DB, operationID string) {
	t.Helper()
	ctx := context.Background()
	var title, relative string
	var size int64
	if err := db.QueryRowContext(ctx, `SELECT r.raw_facts->>'title',af.relative_path,af.size_bytes FROM file_operations fo JOIN artifact_files af ON af.id=fo.artifact_file_id JOIN artifacts a ON a.id=fo.artifact_id JOIN download_tasks dt ON dt.id=a.download_task_id JOIN releases r ON r.id=dt.release_id WHERE fo.id=$1`, operationID).Scan(&title, &relative, &size); err != nil {
		t.Fatal(err)
	}
	business, err := frozenpolicy.SeedBusiness(ctx, db, `vars.actual = {title: input.release_title, path: input.files[0].path, size: input.files[0].size};`)
	if err != nil {
		t.Fatal(err)
	}
	program, err := namingvars.NewStore(varspersistence.NewRepository(db)).LoadProgram(ctx, business.Programs.Vars)
	if err != nil {
		t.Fatal(err)
	}
	// A literal admission record of the seeded program over the exact release
	// and file facts; fixtures never run the time-limited evaluator.
	frozen := namingvars.FrozenEvaluation{Program: program.Ref(), Input: namingvars.Input{ReleaseTitle: title, Files: []namingvars.File{{Name: path.Base(relative), Path: relative, Size: size}}}, Output: namingvars.Vars{"actual": {"title": title, "path": relative, "size": strconv.FormatInt(size, 10)}}, Status: "ok"}
	raw, err := json.Marshal(frozen)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE file_operations SET frozen_config_snapshot=frozen_config_snapshot||jsonb_build_object('vars',$2::jsonb) WHERE id=$1`, operationID, raw); err != nil {
		t.Fatal(err)
	}
}
func freezeAnimeRenderFixtureBusiness(t *testing.T, db *sql.DB, animeID string) {
	t.Helper()
	rows, err := db.Query(`SELECT fo.id FROM file_operations fo JOIN placements p ON p.source_owner_ref=fo.id WHERE p.managed_anime_id=$1`, animeID)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		freezeRenderFixtureBusiness(t, db, id)
	}
}
