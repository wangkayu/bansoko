package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"reflect"
	"testing"

	acq "bansoko/internal/acquisition/domain"
	protocol "bansoko/internal/acquisition/terminal/domain"
	draft "bansoko/internal/anime/draft/application"
	"bansoko/internal/namingvars"
	varspersistence "bansoko/internal/namingvars/adapter/persistence"
)

// checkVarsPreviewEqualsFrozenPostgres proves the shared release-level Vars
// input: the evidence a preview site loads for a persisted release
// (varspersistence.ItemEvidence + namingvars.BuildInput) is exactly the
// known_inputs.facts acceptance freezes, and a naming-vars-v1 program reading
// those facts evaluates the same output from both.
func checkVarsPreviewEqualsFrozenPostgres(t *testing.T, ctx context.Context, owner, runtime *sql.DB, revisions *draft.Service, req qualificationRequest) {
	var savedBGM, savedTMDB, savedKind sql.NullString
	var savedSeason sql.NullInt64
	var savedSourceFacts []byte
	if err := owner.QueryRowContext(ctx, `SELECT m.bgm_work_id,m.tmdb_work_id,m.tmdb_media_kind,m.tmdb_season,r.source_facts FROM anime_metadata m, releases r WHERE m.managed_anime_id='anime-1' AND r.id='rel-1'`).Scan(&savedBGM, &savedTMDB, &savedKind, &savedSeason, &savedSourceFacts); err != nil {
		t.Fatal(err)
	}
	defer func() {
		for _, restore := range []struct {
			stmt string
			args []any
		}{
			{`UPDATE anime_metadata SET bgm_work_id=$1, tmdb_work_id=$2, tmdb_media_kind=$3, tmdb_season=$4, version=version+1 WHERE managed_anime_id='anime-1'`, []any{savedBGM, savedTMDB, savedKind, savedSeason}},
			{`DELETE FROM metadata_work_titles WHERE work_id IN ('424242','99')`, nil},
			{`UPDATE releases SET source_facts=$1::jsonb WHERE id='rel-1'`, []any{savedSourceFacts}},
		} {
			if _, err := owner.ExecContext(ctx, restore.stmt, restore.args...); err != nil {
				t.Fatal(err)
			}
		}
	}()
	for _, stmt := range []string{
		`UPDATE anime_metadata SET bgm_work_id='424242', tmdb_work_id='99', tmdb_media_kind='tv', tmdb_season=1, version=version+1 WHERE managed_anime_id='anime-1'`,
		`INSERT INTO metadata_work_titles(provider,media_kind,work_id,title,title_cn) VALUES('bgm','subject','424242','ショー','秀')`,
		`INSERT INTO metadata_work_titles(provider,media_kind,work_id,title_cn,original_title,overview) VALUES('tmdb','tv','99','中文剧名','ショー','作品的 TMDB 简介。')`,
		`UPDATE releases SET source_facts='{"title_tags":{"schema":"reltitle-attrs-v1","implementation":"r","tags":["resolution:1080p","sub_lang:CHT","sub_lang:chs"],"value_tags":["title:Show"]},"anibt":{"schema":"anibt-tags-v1","tags":["format:MKV"]}}' WHERE id='rel-1'`,
	} {
		if _, err := owner.ExecContext(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	req.entryMode = "subscription_direct"
	capture, err := captureQualification(ctx, runtime, revisions, req)
	if err != nil {
		t.Fatal(err)
	}
	result, err := evaluateAdmission(ctx, capture, serviceIntentFixture(t, capture))
	if err != nil {
		t.Fatal(err)
	}
	if err := protocol.Validate("AcquisitionInputV1", result.input); err != nil {
		t.Fatal(err)
	}
	// The recorded TMDB overview freezes beside the recorded titles
	// (media.tmdb_titles.value.overview, migration 047): a never-observed
	// work freezes null, an observed one the recorded value.
	tmdbTitles := result.input.Object["media"].Object["tmdb_titles"].Object["value"]
	if tmdbTitles.Object["overview"].String != "作品的 TMDB 简介。" ||
		tmdbTitles.Object["title_cn"].String != "中文剧名" {
		t.Fatalf("frozen tmdb_titles = %+v", tmdbTitles)
	}
	vars := result.input.Object["config"].Object["vars"]
	if runtime := vars.Object["program"].Object["runtime_version"].String; runtime != namingvars.ProfileBase {
		t.Fatalf("fixture program runtime %s, want %s", runtime, namingvars.ProfileBase)
	}
	var frozen namingvars.Input
	if err := json.Unmarshal(protocol.Canonical(vars.Object["known_inputs"]), &struct {
		Files *[]namingvars.File `json:"files"`
		Facts *map[string]any    `json:"facts"`
	}{&frozen.Files, &frozen.Facts}); err != nil {
		t.Fatal(err)
	}
	frozen.ReleaseTitle = result.input.Object["source"].Object["title"].Object["value"].String
	bgm, _ := frozen.Facts["bgm"].(map[string]any)
	rt, _ := frozen.Facts["rt"].(map[string]any)
	if bgm["title_cn"] != "秀" || rt["sub_lang"] != "chs+CHT" || frozen.Facts["anime"].(map[string]any)["id"] != "anime-1" {
		t.Fatalf("frozen facts = %+v", frozen.Facts)
	}
	// The preview side: the same release read back the way the RSS list and
	// admission sites load it. The episode identity is the parser's own
	// contract (one selection per release), so it is taken as given here.
	var episode *namingvars.EpisodeEvidence
	if ep, _ := frozen.Facts["ep"].(map[string]any); ep["number"] != nil {
		offset, _ := ep["offset"].(float64)
		episode = &namingvars.EpisodeEvidence{Raw: str(ep["raw"]), Number: str(ep["number"]), Display: str(ep["display"]), Offset: int64(offset)}
	}
	item := acq.FeedItem{Title: frozen.ReleaseTitle, TorrentInfoHashV1: result.manifest.InfoHashV1, TorrentInfoHashV2: result.manifest.InfoHashV2}
	evidence, err := varspersistence.ItemEvidence(ctx, runtime, "anime-1", "rel-1", item, episode)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range result.manifest.Files {
		evidence.Files = append(evidence.Files, namingvars.File{Name: file.Filename, Path: file.SourcePath, Size: file.SizeBytes})
	}
	preview := namingvars.BuildInput(evidence)
	if a, b := canonicalJSON(t, preview.Facts), canonicalJSON(t, frozen.Facts); !reflect.DeepEqual(a, b) {
		t.Fatalf("preview facts differ from frozen:\npreview %v\nfrozen  %v", a, b)
	}
	source := `vars.f = {bgm: input.bgm.title_cn, tmdb: input.tmdb.original_title, rt: input.rt.sub_lang, anibt: input.anibt.format, size: String(input.torrent.size), anime: input.anime.title};`
	digest, err := namingvars.SourceFingerprint(source)
	if err != nil {
		t.Fatal(err)
	}
	program, err := namingvars.NewProgram(namingvars.ProgramRef{Digest: digest, Version: namingvars.ProfileVersion(digest)}, source)
	if err != nil {
		t.Fatal(err)
	}
	fromPreview, err := program.Freeze(preview)
	if err != nil || fromPreview.Status != "ok" {
		t.Fatalf("preview evaluation: %v %+v", err, fromPreview)
	}
	fromFrozen, err := program.Freeze(frozen)
	if err != nil || fromFrozen.Status != "ok" {
		t.Fatalf("frozen evaluation: %v %+v", err, fromFrozen)
	}
	if !reflect.DeepEqual(fromPreview.Output, fromFrozen.Output) || fromFrozen.Output["f"]["bgm"] != "秀" || fromFrozen.Output["f"]["anibt"] != "MKV" {
		t.Fatalf("outputs differ: preview %v frozen %v", fromPreview.Output, fromFrozen.Output)
	}
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func canonicalJSON(t *testing.T, v any) any {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}
