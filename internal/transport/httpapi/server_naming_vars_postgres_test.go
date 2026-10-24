package httpapi

import (
	"context"
	"net/http"
	"testing"

	"bansoko/internal/namingvars"
	varspersistence "bansoko/internal/namingvars/adapter/persistence"
	"bansoko/internal/namingvars/editor"
	"bansoko/internal/transport/httpapi/generated"
)

func namingVarsRig(t *testing.T, seed string) *testRig {
	t.Helper()
	rig := newTestRig(t, "development", true, func(d *Dependencies) { d.NamingVars = editor.NewService(d.DB) })
	store := namingvars.NewStore(varspersistence.NewRepository(rig.db))
	vars, err := namingvars.Run(seed, namingvars.DesignInput(), 0)
	if err != nil {
		t.Fatal(err)
	}
	fp, _ := namingvars.SourceFingerprint(seed)
	if err := store.PublishSource(context.Background(), seed, vars, fp); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Current(context.Background()); err != nil {
		t.Fatal(err)
	}
	return rig
}

func TestNamingVarsEditorSaveActivatesWithoutRestartAndFreezesNothing(t *testing.T) {
	seed := "vars.anime = {ep: extract(input.release_title, /(\\d+)/) ?? \"\"};\n"
	rig := namingVarsRig(t, seed)

	recorder, body := rig.do(t, http.MethodGet, "/api/v1/naming-vars", nil, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("state = %d %s", recorder.Code, body)
	}
	state := decodeBody[generated.NamingVarsState](t, body)
	if state.Active.Version.Revision != 1 || state.Active.Source != seed || !state.Active.Version.Active || len(state.InputFacts) == 0 || len(state.Samples) != 3 {
		t.Fatalf("state = %+v", state)
	}

	// An invalid program changes nothing and reports where.
	recorder, body = rig.do(t, http.MethodPost, "/api/v1/naming-vars/versions", map[string]any{"source": "vars.a = {b: };\n"}, map[string]string{"If-Match": "1"})
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid save = %d %s", recorder.Code, body)
	}
	failure := decodeBody[generated.ErrorResponse](t, body)
	if failure.Code != "NAMING_VARS_INVALID" || failure.Details == nil || (*failure.Details)[0].Start == nil || (*failure.Details)[0].End == nil {
		t.Fatalf("invalid save details = %+v", failure)
	}
	var count int
	if err := rig.db.QueryRow(`SELECT count(*) FROM naming_vars_versions WHERE origin = 'editor'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("editor versions after an invalid save = %d / %v", count, err)
	}

	second := "vars.series = {name: \"second\"};\n"
	recorder, body = rig.do(t, http.MethodPost, "/api/v1/naming-vars/versions", map[string]any{"source": second, "note": "second"}, map[string]string{"If-Match": "1"})
	if recorder.Code != http.StatusCreated {
		t.Fatalf("save = %d %s", recorder.Code, body)
	}
	saved := decodeBody[generated.NamingVarsProgram](t, body)
	if saved.Version.Revision != 2 || saved.Version.Origin != generated.NamingVarsVersionOriginEditor || saved.Version.Note != "second" {
		t.Fatalf("saved = %+v", saved)
	}
	// Hot: the running process answers with the new program, no restart.
	active, fp, err := namingvars.Current()
	if err != nil || fp != saved.Version.Digest || active["series"]["name"] != "second" {
		t.Fatalf("process active after save = %v / %q / %v", active, fp, err)
	}

	// A save from the old revision conflicts and names the current one.
	recorder, body = rig.do(t, http.MethodPost, "/api/v1/naming-vars/versions", map[string]any{"source": "vars.third = {name: \"third\"};\n"}, map[string]string{"If-Match": "1"})
	failure = decodeBody[generated.ErrorResponse](t, body)
	if recorder.Code != http.StatusConflict || failure.CurrentVersion == nil || *failure.CurrentVersion != 2 {
		t.Fatalf("stale save = %d %+v", recorder.Code, failure)
	}

	// Rolling back is an activation; the earlier version is the very same row.
	recorder, body = rig.do(t, http.MethodPut, "/api/v1/naming-vars/active", map[string]any{"revision": 1}, map[string]string{"If-Match": "2"})
	if recorder.Code != http.StatusOK {
		t.Fatalf("rollback = %d %s", recorder.Code, body)
	}
	rolled := decodeBody[generated.NamingVarsProgram](t, body)
	if rolled.Version.Revision != 1 || rolled.Source != seed {
		t.Fatalf("rolled = %+v", rolled)
	}
	if _, fp, _ := namingvars.Current(); fp != rolled.Version.Digest {
		t.Fatalf("process active after rollback = %q, want %q", fp, rolled.Version.Digest)
	}
	// The history keeps both, the older one with its original digest.
	recorder, body = rig.do(t, http.MethodGet, "/api/v1/naming-vars", nil, nil)
	state = decodeBody[generated.NamingVarsState](t, body)
	if len(state.Versions) != 2 || state.Versions[0].Revision != 2 || state.Versions[0].Active || !state.Versions[1].Active {
		t.Fatalf("history = %+v", state.Versions)
	}
	recorder, body = rig.do(t, http.MethodGet, "/api/v1/naming-vars/versions/2", nil, nil)
	past := decodeBody[generated.NamingVarsProgram](t, body)
	if recorder.Code != http.StatusOK || past.Source != second || past.Version.Digest != saved.Version.Digest {
		t.Fatalf("past version = %d %+v", recorder.Code, past)
	}
	if recorder, _ = rig.do(t, http.MethodGet, "/api/v1/naming-vars/versions/99", nil, nil); recorder.Code != http.StatusNotFound {
		t.Fatalf("unknown revision = %d", recorder.Code)
	}
}

func TestNamingVarsEditorExportImportRoundTrip(t *testing.T) {
	seed := "vars.anime = {ep: \"1\"};\n"
	rig := namingVarsRig(t, seed)
	recorder, body := rig.do(t, http.MethodGet, "/api/v1/naming-vars/export", nil, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("export = %d %s", recorder.Code, body)
	}
	document := decodeBody[generated.NamingVarsDocument](t, body)
	if document.Source != seed || document.Revision != 1 || document.Runtime != namingvars.ProfileBase {
		t.Fatalf("document = %+v", document)
	}
	// Export writes nothing.
	var count int
	if err := rig.db.QueryRow(`SELECT count(*) FROM naming_vars_versions`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("versions after export = %d / %v", count, err)
	}

	other := "vars.imported = {name: \"yes\"};\n"
	otherFP, _ := namingvars.SourceFingerprint(other)
	document.Source, document.Digest, document.Note = other, otherFP, "moved"
	recorder, body = rig.do(t, http.MethodPost, "/api/v1/naming-vars/imports", document, map[string]string{"If-Match": "1"})
	if recorder.Code != http.StatusCreated {
		t.Fatalf("import = %d %s", recorder.Code, body)
	}
	imported := decodeBody[generated.NamingVarsProgram](t, body)
	if imported.Version.Origin != generated.NamingVarsVersionOriginImport || imported.Version.Revision != 2 || imported.Version.Note != "moved" {
		t.Fatalf("imported = %+v", imported)
	}
	if active, _, _ := namingvars.Current(); active["imported"]["name"] != "yes" {
		t.Fatalf("import did not take effect: %v", active)
	}

	// An unsupported version or a tampered digest never switches the program.
	for name, mutate := range map[string]func(*generated.NamingVarsDocument){
		"version": func(d *generated.NamingVarsDocument) { d.FormatVersion = 7 },
		"digest":  func(d *generated.NamingVarsDocument) { d.Digest = "sha256:tampered" },
		"runtime": func(d *generated.NamingVarsDocument) { d.Runtime = "naming-vars-v2" },
	} {
		bad := document
		mutate(&bad)
		recorder, body = rig.do(t, http.MethodPost, "/api/v1/naming-vars/imports", bad, map[string]string{"If-Match": "2"})
		if recorder.Code != http.StatusUnprocessableEntity {
			t.Fatalf("%s: import = %d %s", name, recorder.Code, body)
		}
	}
	if err := rig.db.QueryRow(`SELECT count(*) FROM naming_vars_versions WHERE active AND revision = 2`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("a refused import moved the active pointer: %d / %v", count, err)
	}
}

func TestNamingVarsEditorDryRunOverARealRelease(t *testing.T) {
	seed := "vars.anime = {ep: \"1\"};\n"
	rig := namingVarsRig(t, seed)
	recorder, body := rig.do(t, http.MethodPost, "/api/v1/anime/subscribe", withStart(draftPayload()), map[string]string{"Idempotency-Key": "naming-vars-dry-run-0001"})
	if recorder.Code != http.StatusCreated {
		t.Fatalf("subscribe = %d %s", recorder.Code, body)
	}
	subscribed := decodeBody[generated.SubscribeResult](t, body)
	sourceID := subscribed.RssSources[0].Id
	title := "[TestGroup] API Show - 03 [1080p]"
	if _, err := rig.db.Exec(`
INSERT INTO releases(id, rss_source_id, stable_item_key, raw_facts, internal_facts, source_facts, verdict, reason_code)
VALUES ('nv-release-1', $1, 'nv-release-1', $2::jsonb, $3::jsonb, '{}', 'discovered', 'discovered'),
       ('nv-release-2', $1, 'nv-release-2', $2::jsonb, $4::jsonb, '{}', 'discovered', 'discovered')`,
		sourceID,
		`{"title":"`+title+`"}`,
		`{"title":"`+title+`","release_files":[{"source_path":"API Show/API Show - 03.mkv","filename":"API Show - 03.mkv","size_bytes":4096}]}`,
		`{"title":"`+title+`"}`,
	); err != nil {
		t.Fatal(err)
	}

	program := "vars.show = {title: input.anime.title ?? \"\", n: `${input.torrent.file_count}`, size: `${input.torrent.size}`, release: input.release.title ?? \"\"};\n"
	recorder, body = rig.do(t, http.MethodPost, "/api/v1/naming-vars/dry-runs", map[string]any{
		"source": program,
		"targets": []any{
			map[string]any{"kind": "release", "release_id": "nv-release-1"},
			map[string]any{"kind": "release", "release_id": "nv-release-2"},
			map[string]any{"kind": "release", "release_id": "missing"},
		},
	}, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("dry run = %d %s", recorder.Code, body)
	}
	result := decodeBody[generated.NamingVarsDryRunResult](t, body)
	if len(result.Targets) != 3 {
		t.Fatalf("targets = %+v", result.Targets)
	}
	real := result.Targets[0]
	if real.Status != generated.NamingVarsDryRunTargetResultStatusOk {
		t.Fatalf("real release = %+v", real)
	}
	show := real.Output["show"]
	if show["title"] != "API Show" || show["n"] != "1" || show["size"] != "4096" || show["release"] != title {
		t.Fatalf("real release output = %v, want the persisted release's own facts", show)
	}
	if result.Targets[1].Status != generated.NamingVarsDryRunTargetResultStatusUnavailable || result.Targets[1].Reason == nil || string(*result.Targets[1].Reason) != editor.ReasonInventoryMissing {
		t.Fatalf("release without an inventory = %+v", result.Targets[1])
	}
	if result.Targets[2].Status != generated.NamingVarsDryRunTargetResultStatusUnavailable || string(*result.Targets[2].Reason) != editor.ReasonReleaseNotFound {
		t.Fatalf("missing release = %+v", result.Targets[2])
	}

	// The release picker lists them, newest first, with their anime.
	recorder, body = rig.do(t, http.MethodGet, "/api/v1/naming-vars/releases", nil, nil)
	options := decodeBody[[]generated.NamingVarsReleaseOption](t, body)
	if recorder.Code != http.StatusOK || len(options) != 2 || options[0].AnimeTitle != "API Show" {
		t.Fatalf("release options = %d %+v", recorder.Code, options)
	}
	// A dry run stores nothing.
	var count int
	if err := rig.db.QueryRow(`SELECT count(*) FROM naming_vars_versions WHERE origin <> 'file' AND active`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("a dry run changed the active program: %d / %v", count, err)
	}
}
