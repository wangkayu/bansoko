package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"

	configurationapplication "bansoko/internal/configuration/application"
	"bansoko/internal/transport/httpapi/generated"
)

// Every per-anime section is one-time input. Global
// sections are only the defaults a new draft copies, so saving a new value for
// every per-anime Global section leaves an existing saved draft and an existing
// instance's rules and subgroups tabs byte-identical.
func TestGlobalSaveLeavesDraftAndInstanceTabsByteIdentical(t *testing.T) {
	ensureTestAttrRules(t)
	rig := newTestRig(t, "development", true, func(deps *Dependencies) {
		deps.GlobalSections = configurationapplication.NewService(deps.DB, nil)
	})

	saveCandidate := func(entryRef string) generated.AnimeConfigCandidate {
		t.Helper()
		recorder, body := rig.do(t, http.MethodPost, "/api/v1/anime-config-candidates", providerDraftPayload(entryRef, "678"), nil)
		if recorder.Code != http.StatusCreated && recorder.Code != http.StatusOK {
			t.Fatalf("candidate save failed: %d %s", recorder.Code, body)
		}
		candidate := decodeBody[generated.AnimeConfigCandidate](t, body)
		if candidate.CurrentRevision == nil {
			t.Fatal("saved candidate has no revision")
		}
		return candidate
	}
	draft := saveCandidate("70101")
	activate := saveCandidate("70102")
	recorder, body := rig.do(t, http.MethodPost, "/api/v1/anime-config-candidates/"+activate.Id+"/subscribe",
		map[string]any{"revision": *activate.CurrentRevision},
		map[string]string{"Idempotency-Key": "one-time-sections-0001", "If-Match": strconv.FormatInt(activate.Version, 10)})
	if recorder.Code != http.StatusCreated {
		t.Fatalf("candidate activation failed: %d %s", recorder.Code, body)
	}
	animeID := decodeBody[generated.SubscribeResult](t, body).ManagedAnime.Id

	// The tabs: the saved draft, and the instance's rules and subgroups.
	tabs := func() map[string][]byte {
		t.Helper()
		out := map[string][]byte{}
		recorder, body := rig.do(t, http.MethodGet, "/api/v1/anime-config-candidates/"+draft.Id, nil, nil)
		if recorder.Code != http.StatusOK {
			t.Fatalf("draft read failed: %d %s", recorder.Code, body)
		}
		out["draft"] = body
		recorder, body = rig.do(t, http.MethodGet, "/api/v1/anime/"+animeID+"/workspace", nil, nil)
		if recorder.Code != http.StatusOK {
			t.Fatalf("workspace read failed: %d %s", recorder.Code, body)
		}
		var workspace map[string]json.RawMessage
		if err := json.Unmarshal(body, &workspace); err != nil {
			t.Fatal(err)
		}
		out["instance rules"] = workspace["rules"]
		out["instance subgroups"] = workspace["subgroups"]
		return out
	}
	before := tabs()

	// Save a different value for every per-anime Global section.
	recorder, body = rig.do(t, http.MethodGet, "/api/v1/config/global-sections", nil, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("global sections read failed: %d %s", recorder.Code, body)
	}
	views := decodeBody[[]generated.GlobalSectionView](t, body)
	changes := map[generated.GlobalSectionName]func(map[string]any){
		generated.GlobalSectionNameSubgroups: func(v map[string]any) { v["max_rss"] = 9 },
		generated.GlobalSectionNameParser: func(v map[string]any) {
			patterns, _ := v["episode_patterns"].([]any)
			v["episode_patterns"] = append([]any{`第(\d+)话`}, patterns...)
		},
		generated.GlobalSectionNameReleaseRules: func(v map[string]any) { v["must_contain"] = "720p" },
		generated.GlobalSectionNameFileRules:    func(v map[string]any) { v["must_contain"] = "mkv" },
		generated.GlobalSectionNameNamingRules:  func(v map[string]any) { v["subtitle_suffix_template"] = ".zh" },
		generated.GlobalSectionNameFileEffect:   func(v map[string]any) { v["collision_policy"] = "versioned" },
		generated.GlobalSectionNameTagFilters: func(v map[string]any) {
			v["anibt"] = map[string]any{"require": []any{}, "exclude": []any{"language:CHT"}}
		},
	}
	saved := 0
	for _, view := range views {
		change, ok := changes[view.SectionName]
		if !ok {
			continue
		}
		value := map[string]any(view.Value)
		change(value)
		recorder, body := rig.do(t, http.MethodPut, "/api/v1/config/global-sections/"+string(view.SectionName),
			map[string]any{"schema_version": view.SchemaVersion, "value": value},
			map[string]string{"If-Match": strconv.FormatInt(view.Version, 10)})
		if recorder.Code != http.StatusOK {
			t.Fatalf("save Global %s failed: %d %s", view.SectionName, recorder.Code, body)
		}
		saved++
	}
	if saved != len(changes) {
		t.Fatalf("saved %d of %d per-anime Global sections", saved, len(changes))
	}

	after := tabs()
	for name, value := range before {
		if !bytes.Equal(value, after[name]) {
			t.Fatalf("%s tab changed after a Global save:\nbefore %s\nafter  %s", name, value, after[name])
		}
	}
}
