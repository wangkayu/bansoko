package application

import (
	animedomain "bansoko/internal/anime/domain"
	filesystem "bansoko/internal/library/adapter/filesystem"
	"bansoko/internal/postprocessing/naming"
	"os"
	"path/filepath"
	"testing"
)

func TestTypedAdoptedVideoHasIndependentSubtitleWork(t *testing.T) {
	db, f := openFileOperationDBWithPolicy(t, func(p map[string]any) {
		bindingManifestFixture(t, map[string]any{"file_ref": "subtitle", "relative_path": "source.chs.ass", "exact_size": 8, "media_kind": "subtitle"})(p)
		rules := naming.DefaultNamingRules()
		rules.TV.DirectoryTemplate, rules.TV.VideoFilenameTemplate = "adopted", "Main.mkv"
		rules.Movie, rules.Unknown = rules.TV, rules.TV
		p["effective_sections"].(map[string]any)[animedomain.SectionNamingRules] = rules
	})
	source := filepath.Join(t.TempDir(), "source.chs.ass")
	if err := os.WriteFile(source, []byte("subtitle"), 0600); err != nil {
		t.Fatal(err)
	}
	addArtifactFile(t, db, f, "adopt-dependency-sub", "subtitle", "source.chs.ass", source, "subtitle", 8)
	target := filepath.Join(canonicalFixtureRoot(t, f), "adopted", "Main.mkv")
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("source-content"), 0600); err != nil {
		t.Fatal(err)
	}
	s := newFixtureServiceWithPorts(t, db, f, &testExecutor{}, filesystem.OSFileObserver{})
	prepareFixture(t, db, s, f.artifactID)
	var videoKind, subKind string
	var ops, works int
	if err := db.QueryRow(`SELECT plan_kind FROM acquisition_files f JOIN postprocessing_plans p ON p.acquisition_id=f.acquisition_id WHERE p.artifact_id=$1 AND f.file_ref='primary'`, f.artifactID).Scan(&videoKind); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT plan_kind FROM acquisition_files f JOIN postprocessing_plans p ON p.acquisition_id=f.acquisition_id WHERE p.artifact_id=$1 AND f.file_ref='subtitle'`, f.artifactID).Scan(&subKind); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM file_operations WHERE artifact_id=$1`, f.artifactID).Scan(&ops); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM work_item_history WHERE owner_type='file_operation'`).Scan(&works); err != nil {
		t.Fatal(err)
	}
	if videoKind != "adopt" || subKind != "execute" || ops != 1 || works != 1 {
		t.Fatalf("%s/%s ops%d works%d", videoKind, subKind, ops, works)
	}
	assertCommittedReentry(t, s, f.artifactID, nil, "confirmed", "")
	before := planGraphSnapshot(t, db)
	rows := loadOperations(t, db, f.artifactID)
	if rows[0].boundVideoOperationID.Valid {
		t.Fatal("fabricated video owner")
	}
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	assertCommittedReentry(t, s, f.artifactID, nil, "confirmed", "")
	if planGraphSnapshot(t, db) != before {
		t.Fatal("post-observation change rewrote history")
	}
}
