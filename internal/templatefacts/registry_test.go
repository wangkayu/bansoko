package templatefacts

import (
	"regexp"
	"strings"
	"testing"
)

// Roots stay short (seven letters at most; the "download" root has eight),
// fields are snake_case, and a path has at most three segments.
var factPathPattern = regexp.MustCompile(`^[a-z]{1,8}(\.[a-z][a-z0-9_]*){1,2}$`)

func TestRegistryFactsAreComplete(t *testing.T) {
	for _, fact := range All() {
		if !factPathPattern.MatchString(fact.Path) {
			t.Errorf("%s: path breaks the naming rules", fact.Path)
		}
		if fact.Phase != PhaseRelease && fact.Phase != PhaseExecution {
			t.Errorf("%s: phase %q", fact.Path, fact.Phase)
		}
		if fact.Scenarios == 0 || fact.Nullable&^fact.Scenarios != 0 {
			t.Errorf("%s: scenarios %b nullable %b", fact.Path, fact.Scenarios, fact.Nullable)
		}
		for _, text := range []string{fact.Label.ZH, fact.Label.EN, fact.Description.ZH, fact.Description.EN} {
			if strings.TrimSpace(text) == "" {
				t.Errorf("%s: missing copy", fact.Path)
			}
		}
		if fact.get == nil {
			t.Errorf("%s: no getter", fact.Path)
		}
		for _, scenario := range Scenarios {
			if !fact.In(scenario) {
				continue
			}
			sample := fact.SampleFor(scenario)
			if Absent(sample) {
				t.Errorf("%s: no sample", fact.Path)
				continue
			}
			ok := false
			switch fact.Type {
			case TypeString, TypeTimestamp:
				_, ok = sample.(string)
			case TypeInteger:
				_, ok = sample.(int64)
			case TypeBoolean:
				_, ok = sample.(bool)
			case TypeList:
				_, isStrings := sample.([]string)
				_, objects := sample.([]map[string]any)
				ok = isStrings || objects
			}
			if !ok {
				t.Errorf("%s: sample %T does not match type %s", fact.Path, sample, fact.Type)
			}
		}
	}
}

func TestRegistryNeverExposesNamingOnlyRootsToNotificationOrOutToNaming(t *testing.T) {
	for _, fact := range ScenarioFacts(ScenarioNaming) {
		switch fact.Root() {
		case "out", "op", "event", "result", "download":
			t.Errorf("naming must not read %s", fact.Path)
		}
	}
	for _, fact := range All() {
		if strings.Contains(fact.Path, "url") || strings.Contains(fact.Path, "locator") {
			t.Errorf("%s: download locators are never exposed", fact.Path)
		}
	}
}

// The payload key universe is the registry itself: every payload key is a
// registered notification fact under its own path (identity, no aliases), and
// no legacy spelling survives.
func TestNotificationPayloadKeysAreRegistryPaths(t *testing.T) {
	for key, path := range NotificationPayloadKeys {
		if key != path {
			t.Errorf("payload key %s maps to %s: the payload layer must be identity", key, path)
		}
		fact, ok := Lookup(path)
		if !ok || fact.Scenarios&scenarioNotify == 0 {
			t.Errorf("payload key %s is not a registered notification fact", path)
		}
	}
	for _, fact := range All() {
		if fact.Scenarios&scenarioNotify == 0 {
			continue
		}
		if NotificationPayloadKeys[fact.Path] != fact.Path {
			t.Errorf("notification fact %s is missing from the payload key set", fact.Path)
		}
	}
	for _, retired := range []string{
		"info.hdr", "anime.custom_title", "episode.raw", "episode.number", "episode.display", "episode.offset",
		"bangumi.id", "bangumi.episode_title", "tmdb.media_kind", "torrent.info_hash_v1", "torrent.info_hash_v2",
		"event.occurred_at", "download.client_name", "download.task_name",
		"file.basename", "file.extension", "file.size_bytes", "file.media_kind",
		"file.container", "file.resolution", "file.video_codec", "file.audio_codec",
		"output.directory", "output.filename", "output.relative_path",
		"file_operation.kind", "file_operation.collision_policy",
	} {
		if _, ok := NotificationPayloadKeys[retired]; ok {
			t.Errorf("retired payload key %s is still listed", retired)
		}
	}
}

// media.hdr is the naming reference of MediaFacts.HDR (hdr10 / hlg / dovi,
// comma-joined when the file carries several), empty — never undefined — for
// SDR and unread files. It lives on the media root next to the probe facts.
func TestInfoHDRFactReference(t *testing.T) {
	fact, ok := Lookup("media.hdr")
	if !ok {
		t.Fatal("media.hdr is not registered")
	}
	if fact.Type != TypeString || fact.Phase != PhaseExecution {
		t.Fatalf("media.hdr type/phase = %s/%s", fact.Type, fact.Phase)
	}
	if !fact.In(ScenarioNaming) || !fact.In(ScenarioFileCompleted) || !fact.In(ScenarioFileFailed) {
		t.Fatalf("media.hdr scenarios = %b", fact.Scenarios)
	}
	var empty, set FactSet
	if value := fact.Value(&empty); value != nil {
		t.Fatalf("absent HDR value = %#v, want nil", value)
	}
	set.Info.HDR = "hdr10"
	if value := fact.Value(&set); value != "hdr10" {
		t.Fatalf("HDR value = %#v, want \"hdr10\"", value)
	}
	if _, ok := NotificationPayloadKeys["info.hdr"]; ok {
		t.Fatal("the retired info.hdr payload key must not be listed")
	}
	if NotificationPayloadKeys["media.hdr"] != "media.hdr" {
		t.Fatal("media.hdr must be a payload key under its own registry path")
	}
}

func TestScenarioObjectCarriesEveryFactAndNullsAbsentValues(t *testing.T) {
	object := ScenarioObject(ScenarioNaming, &FactSet{Anime: AnimeFacts{Title: "  "}, Rt: Values{"Title": "进击的巨人"}})
	anime := object["anime"].(map[string]any)
	if value, ok := anime["title"]; !ok || value != nil {
		t.Fatalf("blank title = %#v, %v; want present null", value, ok)
	}
	if object["rt"].(map[string]any)["title"] != "进击的巨人" {
		t.Fatalf("rt.title = %#v", object["rt"])
	}
	if sub := object["file"].(map[string]any)["sub"].(map[string]any); sub["forced"] != false {
		t.Fatalf("file.sub.forced = %#v", sub["forced"])
	}
	media := object["media"].(map[string]any)
	if _, ok := media["hdr"]; !ok {
		t.Fatalf("media = %#v; want the registered media.hdr", media)
	}
	if _, ok := object["info"]; ok {
		t.Fatalf("media = %#v; the retired info root must not carry facts", object["media"])
	}
	if _, ok := object["out"]; ok {
		t.Fatal("naming object must not carry out")
	}
}

func TestEffectiveTitleOrder(t *testing.T) {
	for _, tc := range []struct {
		in            TitleCandidates
		title, source string
	}{
		{TitleCandidates{Override: "C", Provider: "P", TmdbCN: "T", BgmCN: "B", Bgm: "G", Base: "A"}, "C", TitleSourceOverride},
		{TitleCandidates{Provider: "P", TmdbCN: "T", BgmCN: "B", Bgm: "G", Base: "A"}, "P", TitleSourceProvider},
		{TitleCandidates{TmdbCN: "T", BgmCN: "B", Bgm: "G", Base: "A"}, "T", TitleSourceTmdbCN},
		{TitleCandidates{BgmCN: "B", Bgm: "G", Base: "A"}, "B", TitleSourceBgmCN},
		{TitleCandidates{Bgm: "G", Base: "A"}, "G", TitleSourceBgm},
		{TitleCandidates{Base: "A"}, "A", TitleSourceBase},
		{TitleCandidates{Override: "  ", Provider: " P "}, "P", TitleSourceProvider},
		{TitleCandidates{}, "", ""},
	} {
		if title, source := EffectiveTitle(tc.in); title != tc.title || source != tc.source {
			t.Errorf("%+v = %q/%q; want %q/%q", tc.in, title, source, tc.title, tc.source)
		}
	}
}

func TestEffectiveYearOrderAndBounds(t *testing.T) {
	year := func(value int) *int { return &value }
	for _, tc := range []struct {
		name      string
		tmdb, bgm *int
		year      int
		source    string
	}{
		{"tmdb wins", year(2020), year(2019), 2020, "tmdb"},
		{"bgm alone", nil, year(2019), 2019, "bgm"},
		{"neither known", nil, nil, 0, ""},
		{"out of range tmdb falls through", year(1800), year(2019), 2019, "bgm"},
		{"both out of range", year(2200), year(1800), 0, ""},
		{"bounds are inclusive", year(MinYear), year(MaxYear), MinYear, "tmdb"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, source := EffectiveYear(tc.tmdb, tc.bgm)
			if got != tc.year || source != tc.source {
				t.Fatalf("EffectiveYear(%v, %v) = %d/%q; want %d/%q", tc.tmdb, tc.bgm, got, source, tc.year, tc.source)
			}
		})
	}
}

// tmdb.summary joins the title-family payload facts on the media scenarios
// (file completed/failed, download submitted) and is deliberately absent from
// the naming scenario: a multi-paragraph overview in a path is structurally
// wrong (owner ruling 2026-10-22), so a naming template referencing it must
// fail the save-time compile as an unregistered leaf.
func TestTMDBSummaryScenarioMembership(t *testing.T) {
	fact, ok := Lookup("tmdb.summary")
	if !ok {
		t.Fatal("tmdb.summary is not registered")
	}
	if fact.Type != TypeString || fact.Phase != PhaseRelease {
		t.Fatalf("tmdb.summary type/phase = %s/%s", fact.Type, fact.Phase)
	}
	if !fact.In(ScenarioDownloadSubmitted) || !fact.In(ScenarioFileCompleted) || !fact.In(ScenarioFileFailed) {
		t.Fatalf("tmdb.summary scenarios = %b; want the media scenarios", fact.Scenarios)
	}
	for _, scenario := range []Scenario{ScenarioNaming, ScenarioReleaseDiscovered, ScenarioDownloadFailed, ScenarioSubscriptionChanged, ScenarioUnknownImported} {
		if fact.In(scenario) {
			t.Fatalf("tmdb.summary must not be in scenario %b", scenario)
		}
	}
	for _, listed := range ScenarioFacts(ScenarioNaming) {
		if listed.Path == "tmdb.summary" {
			t.Fatal("the naming catalog must not list tmdb.summary")
		}
	}
}
