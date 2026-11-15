package notification

import (
	"encoding/json"
	"strings"
	"testing"

	"bansoko/internal/templatefacts"
)

// Every payload key a media-trigger producer writes is a registry fact path:
// payloads are stored under the registry paths with no translation layer.
func TestPayloadKeysAreRegistryPaths(t *testing.T) {
	for _, trigger := range []string{TriggerDownloadSubmitted, TriggerFileOperationCompleted, TriggerFileOperationFailed} {
		for _, key := range PayloadKeys(trigger) {
			fact, ok := templatefacts.Lookup(key)
			if !ok {
				t.Errorf("%s: payload key %s is not a registered fact", trigger, key)
				continue
			}
			if templatefacts.NotificationPayloadKeys[key] != fact.Path {
				t.Errorf("%s: payload key %s is not in the notification payload key set", trigger, key)
			}
		}
	}
}

func TestRenderMessageFormatsDecodedNumbersAndNestedFacts(t *testing.T) {
	facts := map[string]any{"file.size": float64(734003200), "anime": map[string]any{"title": "嵌套"}}
	message, ok := RenderMessage(TriggerFileOperationCompleted, "{{anime.title}}", "{{file.size}}|{{file.name}}", facts)
	if !ok || !message.Valid() {
		t.Fatalf("render failed: %+v", message)
	}
	if message.Title != "嵌套" || message.Body != "734003200|" {
		t.Fatalf("title=%q body=%q", message.Title, message.Body)
	}
	if len(message.Warnings) != 1 || message.Warnings[0].Variable != "file.name" {
		t.Fatalf("warnings=%+v", message.Warnings)
	}
	if got := FormatFact(json.Number("12")); got != "12" {
		t.Fatalf("json number=%q", got)
	}
}

func TestRenderMessageRejectsEmptyAndOversizedOutput(t *testing.T) {
	if message, _ := RenderMessage(TriggerDownloadSubmitted, "{{anime.title_override}}", " ", map[string]any{}); message.Valid() || message.Errors[0].Code != "rendered_empty" {
		t.Fatalf("empty output accepted: %+v", message.Errors)
	}
	long := strings.Repeat("长", 600)
	title := strings.Repeat("{{anime.title}}", 10)
	if message, _ := RenderMessage(TriggerDownloadSubmitted, title, "x", map[string]any{"anime.title": long}); message.Valid() || message.Errors[0].Code != "rendered_too_large" {
		t.Fatalf("oversized title accepted: %+v", message.Errors)
	}
}

func TestCompileReportsSourceSpans(t *testing.T) {
	errors, ok := Compile(TriggerDownloadSubmitted, "ok {{anime.nope}}", "{{lower()}}")
	if !ok || len(errors) != 2 {
		t.Fatalf("errors=%+v", errors)
	}
	if errors[0].Field != FieldTitle || errors[0].Start != 3 || errors[0].End != 17 {
		t.Fatalf("title span=%+v", errors[0])
	}
	if errors[1].Field != FieldBody || errors[1].Start != 0 || errors[1].End != 11 {
		t.Fatalf("body span=%+v", errors[1])
	}
	if _, ok := Compile("nope", "", ""); ok {
		t.Fatal("unknown trigger compiled")
	}
}

func TestRenderReadsValueNamespacesOnEveryTrigger(t *testing.T) {
	sourceFacts := []byte(`{"title_tags":{"schema":"reltitle-attrs-v1","implementation":"x","tags":["resolution:1080p","sub_lang:CHT","sub_lang:chs","sub_lang:CHS"],"value_tags":["title:进击的巨人"]},"anibt":{"tags":["language:CHT","language:CHS","format:MKV"]}}`)
	vars := map[string]any{"info": map[string]any{"tag": "WEB", "source_url": "https://x"}, "bad": "x"}
	facts := ValueFacts(vars, sourceFacts, func(v string) any { return v })
	for key, want := range map[string]any{"rt.title": "进击的巨人", "rt.res": "1080p", "rt.lang": "chs+CHT", "rt.group": nil, "anibt.lang": "CHS+CHT", "anibt.format": "MKV", "anibt.tag": nil, "vars.info.tag": "WEB"} {
		if got, ok := facts[key]; !ok || got != want {
			t.Errorf("%s = %#v (%v); want %#v", key, got, ok, want)
		}
	}
	if _, ok := facts["vars.info.source_url"]; ok {
		t.Error("a vars key the payload safety check rejects must be left out")
	}
	for _, trigger := range Triggers {
		if trigger == TriggerSubscriptionChanged || trigger == TriggerReplacementDeletionFailed {
			// A subscription transition or a failed deletion concerns no release.
			continue
		}
		if trigger == TriggerFileDisplaced {
			// A displaced file carries the taker's identity only, not a
			// release whose rt/anibt tags the payload froze.
			continue
		}
		message, ok := RenderMessage(trigger, "{{vars.info.tag}} {{rt.lang}}", "{{defaultIfEmpty(anibt.tag, '-')}} {{vars.info.missing}}", facts)
		if !ok || !message.Valid() || message.Title != "WEB chs+CHT" || message.Body != "- " {
			t.Fatalf("%s: %+v", trigger, message)
		}
	}
	if errors, _ := Compile(TriggerDownloadSubmitted, "{{vars.input.x}}", "{{vars.info}}"); len(errors) != 2 {
		t.Fatalf("vars.input and a bare group must not compile: %+v", errors)
	}
	if facts := ValueFacts(nil, []byte(`{"anibt":"broken"}`), func(v string) any { return v }); facts["anibt.format"] != nil || len(facts) != len(ValueFactKeys) {
		t.Fatalf("undecodable source facts = %#v", facts)
	}
}

func TestTitleFactsReadOnlyFrozenNativeTitles(t *testing.T) {
	identity := func(v string) any { return v }
	empty := TitleFacts([]byte(`{"schema_version":4,"media":{}}`), identity)
	for _, key := range TitleFactKeys {
		if value, ok := empty[key]; !ok || value != nil {
			t.Fatalf("%s = %v, want present null", key, value)
		}
	}
	facts := TitleFacts([]byte(`{"schema_version":5,"media":{"bgm_titles":{"state":"known","value":{"title":"B","title_cn":null}},"tmdb_titles":{"state":"known","value":{"title_cn":"T","original_title":"O"}}}}`), identity)
	if facts["bgm.title"] != "B" || facts["bgm.title_cn"] != nil || facts["tmdb.title_cn"] != "T" || facts["tmdb.title"] != "O" {
		t.Fatalf("facts = %+v", facts)
	}
}

// The TMDB work summary joins the title-family facts: it reads the frozen
// media.tmdb_titles.value.overview member (migration 047), and an input frozen
// before that member existed — like an observed-but-absent overview — reads
// null rather than a placeholder.
func TestTitleFactsReadsFrozenOverview(t *testing.T) {
	identity := func(v string) any { return v }
	facts := TitleFacts([]byte(`{"schema_version":5,"media":{"tmdb_titles":{"state":"known","value":{"title_cn":"弧之谜语","original_title":"Literary Arcana","overview":"作品的简介。","year":2024}}}}`), identity)
	if facts["tmdb.summary"] != "作品的简介。" {
		t.Fatalf("tmdb.summary = %#v, want the frozen overview", facts["tmdb.summary"])
	}
	absent := TitleFacts([]byte(`{"schema_version":5,"media":{"tmdb_titles":{"state":"known","value":{"title_cn":"弧之谜语","year":2024}}}}`), identity)
	if absent["tmdb.summary"] != nil {
		t.Fatalf("absent member = %#v, want null", absent["tmdb.summary"])
	}
}

// Every trigger's system default renders with its own sample facts in both
// shipped languages: a default that cannot render is a preview the owner can
// never open.
func TestEveryDefaultRendersWithItsSampleFacts(t *testing.T) {
	for _, trigger := range Triggers {
		for _, locale := range []string{LocaleZH, LocaleEN} {
			title, body := DefaultsFor(trigger, locale)
			if title == "" || body == "" {
				t.Fatalf("%s/%s has no system default", trigger, locale)
			}
			if errors, ok := Compile(trigger, title, body); !ok || len(errors) != 0 {
				t.Fatalf("%s/%s default does not compile: %+v", trigger, locale, errors)
			}
			message, _ := RenderMessage(trigger, title, body, Sample(trigger))
			if !message.Valid() {
				t.Fatalf("%s/%s default does not render: %+v", trigger, locale, message.Errors)
			}
			refs := TemplateReferences(title, body)
			if !refs[ImageVariable] || refs["link"] {
				t.Fatalf("%s/%s default has invalid semantic attachments: %v", trigger, locale, refs)
			}
			sample := Sample(trigger)
			if _, exists := sample["bgm.id"]; exists && !refs[BGMLinkVariable] {
				t.Fatalf("%s/%s default lost its Bangumi link: %v", trigger, locale, refs)
			}
			if _, exists := sample["tmdb.id"]; exists && !refs[TMDBLinkVariable] {
				t.Fatalf("%s/%s default lost its TMDB link: %v", trigger, locale, refs)
			}
		}
	}
}

// The replacement notice names the winning release and every replaced version
// with the identity it recorded; the displacement notice words the cause and
// names the file that took the place.
func TestReplacementAndDisplacementDefaultsWordTheEvent(t *testing.T) {
	title, body := DefaultsFor(TriggerReplacementApplied, LocaleZH)
	message, _ := RenderMessage(TriggerReplacementApplied, title, body, Sample(TriggerReplacementApplied))
	if !message.Valid() {
		t.Fatalf("replacement default does not render: %+v", message.Errors)
	}
	if !strings.Contains(message.Title, "已替换旧版本：示例") && !strings.Contains(message.Title, "已替换旧版本") {
		t.Fatalf("replacement title = %q", message.Title)
	}
	if !strings.Contains(message.Body, "替换了 1 个旧版本") || !strings.Contains(message.Body, " (示例字幕组 · ") {
		t.Fatalf("replacement body = %q", message.Body)
	}

	overwrite, ok := RenderMessage(TriggerFileDisplaced, "{{if(eq(event.cause, 'redownload'), '重新下载覆盖了旧文件', '新文件覆盖了旧文件')}}", "{{file.name}}|{{defaultIfEmpty(taker.title, taker.file)}}|{{defaultIfEmpty(taker.group, '未知')}}", Sample(TriggerFileDisplaced))
	if !ok || !overwrite.Valid() {
		t.Fatalf("displacement render: %+v", overwrite.Errors)
	}
	if overwrite.Title != "新文件覆盖了旧文件" || !strings.Contains(overwrite.Body, "|示例字幕组") {
		t.Fatalf("overwrite render = %q / %q", overwrite.Title, overwrite.Body)
	}
	redownloadFacts := Sample(TriggerFileDisplaced)
	redownloadFacts["event.cause"] = "redownload"
	redownload, ok := RenderMessage(TriggerFileDisplaced, "{{if(eq(event.cause, 'redownload'), '重新下载覆盖了旧文件', '新文件覆盖了旧文件')}}", "{{file.name}}", redownloadFacts)
	if !ok || !redownload.Valid() || redownload.Title != "重新下载覆盖了旧文件" {
		t.Fatalf("redownload render = %+v", redownload)
	}
}

// The file outcome defaults name the original download file's parse only when
// its frozen block recorded that field: the label and its line disappear
// together, and the completed notice keeps its existing subtitle line.
func TestFileOutcomeDefaultsShowTheOriginalFileGroup(t *testing.T) {
	for _, locale := range []string{LocaleZH, LocaleEN} {
		groupLabel, subtitleLabel, fileLabel, reasonLabel := "原文件字幕组", "字幕：", "文件：", "原因："
		if locale == LocaleEN {
			groupLabel, subtitleLabel, fileLabel, reasonLabel = "Original file group", "Subtitles: ", "File: ", "Reason: "
		}
		for _, trigger := range []string{TriggerFileOperationCompleted, TriggerFileOperationFailed} {
			facts := Sample(trigger)
			facts["frt.group"] = "文件组A"
			message := renderDefault(t, trigger, locale, facts)
			if !strings.Contains(message.Body, groupLabel+"：文件组A") && !strings.Contains(message.Body, groupLabel+": 文件组A") {
				t.Fatalf("%s/%s group line = %q", trigger, locale, message.Body)
			}
			assertNoEmptyLabels(t, message.Body)
			if trigger == TriggerFileOperationCompleted && !strings.Contains(message.Body, subtitleLabel) {
				t.Fatalf("%s/%s lost its subtitle line: %q", trigger, locale, message.Body)
			}
			if trigger == TriggerFileOperationFailed {
				if !strings.Contains(message.Body, fileLabel) || !strings.Contains(message.Body, reasonLabel) {
					t.Fatalf("%s/%s lost its file or reason line: %q", trigger, locale, message.Body)
				}
			}

			partial := Sample(trigger)
			delete(partial, "frt.group")
			partial["frt.res"] = "1080p"
			message = renderDefault(t, trigger, locale, partial)
			if strings.Contains(message.Body, groupLabel) {
				t.Fatalf("%s/%s kept an unvalued group label: %q", trigger, locale, message.Body)
			}
			if trigger == TriggerFileOperationCompleted && !strings.Contains(message.Body, subtitleLabel) {
				t.Fatalf("%s/%s lost its subtitle line without the group: %q", trigger, locale, message.Body)
			}

			empty := Sample(trigger)
			empty["frt.group"] = ""
			message = renderDefault(t, trigger, locale, empty)
			if strings.Contains(message.Body, groupLabel) {
				t.Fatalf("%s/%s kept the group label for an empty parse: %q", trigger, locale, message.Body)
			}
			assertNoEmptyLabels(t, message.Body)
			assertNoFrtWarnings(t, message)

			missing := deleteFactsWithPrefix(Sample(trigger), "frt.")
			message = renderDefault(t, trigger, locale, missing)
			if strings.Contains(message.Body, groupLabel) {
				t.Fatalf("%s/%s kept the group label without any frt: %q", trigger, locale, message.Body)
			}
			assertNoEmptyLabels(t, message.Body)
			assertNoFrtWarnings(t, message)
		}
	}
}

type displacedLabels struct {
	oldGroup, newGroup, oldRes, newRes, oldVideo, newVideo, oldAudio, newAudio string
}

func displacedLabelsFor(locale string) displacedLabels {
	if locale == LocaleEN {
		return displacedLabels{
			oldGroup: "Old file group: ", newGroup: "New file group: ",
			oldRes: "Old file resolution: ", newRes: "New file resolution: ",
			oldVideo: "Old file video codec: ", newVideo: "New file video codec: ",
			oldAudio: "Old file audio codec: ", newAudio: "New file audio codec: ",
		}
	}
	return displacedLabels{
		oldGroup: "旧文件字幕组：", newGroup: "新文件字幕组：",
		oldRes: "旧文件分辨率：", newRes: "新文件分辨率：",
		oldVideo: "旧文件视频编码：", newVideo: "新文件视频编码：",
		oldAudio: "旧文件音频编码：", newAudio: "新文件音频编码：",
	}
}

// The displacement default shows both files and each file's own frozen parse.
// The old side never borrows the new side's values (or the release group), and
// a side without a recorded parse keeps its labels hidden.
func TestDisplacedDefaultKeepsTheTwoFileParsesApart(t *testing.T) {
	const (
		oldGroup, newGroup = "旧组OLD", "新组NEW"
		oldRes, newRes     = "480p-old", "2160p-new"
		oldVideo, newVideo = "OLDVC", "NEWVC"
		oldAudio, newAudio = "OLDAC", "NEWAC"
	)
	for _, locale := range []string{LocaleZH, LocaleEN} {
		labels := displacedLabelsFor(locale)
		full := Sample(TriggerFileDisplaced)
		full["frt.group"], full["frt.res"], full["frt.vc"], full["frt.ac"] = oldGroup, oldRes, oldVideo, oldAudio
		full["taker.frt.group"], full["taker.frt.res"], full["taker.frt.vc"], full["taker.frt.ac"] = newGroup, newRes, newVideo, newAudio
		message := renderDefault(t, TriggerFileDisplaced, locale, full)
		for _, want := range []string{
			labels.oldGroup + oldGroup, labels.newGroup + newGroup,
			labels.oldRes + oldRes, labels.newRes + newRes,
			labels.oldVideo + oldVideo, labels.newVideo + newVideo,
			labels.oldAudio + oldAudio, labels.newAudio + newAudio,
		} {
			if !strings.Contains(message.Body, want) {
				t.Fatalf("%s: body misses %q: %q", locale, want, message.Body)
			}
		}
		assertNoEmptyLabels(t, message.Body)

		oldOnly := deleteFactsWithPrefix(Sample(TriggerFileDisplaced), "taker.frt.")
		oldOnly["frt.group"], oldOnly["frt.res"], oldOnly["frt.vc"], oldOnly["frt.ac"] = oldGroup, oldRes, oldVideo, oldAudio
		oldOnly["taker.group"] = "发布组MISUSE"
		message = renderDefault(t, TriggerFileDisplaced, locale, oldOnly)
		for _, want := range []string{labels.oldGroup + oldGroup, labels.oldRes + oldRes, labels.oldVideo + oldVideo, labels.oldAudio + oldAudio} {
			if !strings.Contains(message.Body, want) {
				t.Fatalf("%s old-only: body misses %q: %q", locale, want, message.Body)
			}
		}
		for _, unwanted := range []string{
			labels.newGroup, labels.newRes, labels.newVideo, labels.newAudio,
			newGroup, newRes, newVideo, newAudio, "发布组MISUSE",
		} {
			if strings.Contains(message.Body, unwanted) {
				t.Fatalf("%s old-only: body borrowed %q: %q", locale, unwanted, message.Body)
			}
		}
		assertNoEmptyLabels(t, message.Body)
		assertNoFrtWarnings(t, message)

		newOnly := deleteFactsWithPrefix(Sample(TriggerFileDisplaced), "frt.")
		newOnly["taker.frt.group"], newOnly["taker.frt.res"], newOnly["taker.frt.vc"], newOnly["taker.frt.ac"] = newGroup, newRes, newVideo, newAudio
		message = renderDefault(t, TriggerFileDisplaced, locale, newOnly)
		for _, want := range []string{labels.newGroup + newGroup, labels.newRes + newRes, labels.newVideo + newVideo, labels.newAudio + newAudio} {
			if !strings.Contains(message.Body, want) {
				t.Fatalf("%s new-only: body misses %q: %q", locale, want, message.Body)
			}
		}
		for _, unwanted := range []string{
			labels.oldGroup, labels.oldRes, labels.oldVideo, labels.oldAudio,
			oldGroup, oldRes, oldVideo, oldAudio,
		} {
			if strings.Contains(message.Body, unwanted) {
				t.Fatalf("%s new-only: body borrowed %q: %q", locale, unwanted, message.Body)
			}
		}
		assertNoEmptyLabels(t, message.Body)
		assertNoFrtWarnings(t, message)

		none := deleteFactsWithPrefix(Sample(TriggerFileDisplaced), "frt.", "taker.frt.")
		message = renderDefault(t, TriggerFileDisplaced, locale, none)
		for _, unwanted := range []string{labels.oldGroup, labels.newGroup, labels.oldRes, labels.newRes, labels.oldVideo, labels.newVideo, labels.oldAudio, labels.newAudio} {
			if strings.Contains(message.Body, unwanted) {
				t.Fatalf("%s no-parse: body kept label %q: %q", locale, unwanted, message.Body)
			}
		}
		oldFile, newFile := "旧文件：", "新文件："
		if locale == LocaleEN {
			oldFile, newFile = "Old file: ", "New file: "
		}
		if !strings.Contains(message.Body, oldFile) || !strings.Contains(message.Body, newFile) {
			t.Fatalf("%s no-parse: body lost the file names: %q", locale, message.Body)
		}
		assertNoEmptyLabels(t, message.Body)
		assertNoFrtWarnings(t, message)
	}
}

// The release group of the displacing release is not the file parse; the
// default must not present it in the file-group lines, even when it is known.
func TestDisplacedDefaultDoesNotUseTheReleaseGroupAsTheFileParse(t *testing.T) {
	for _, locale := range []string{LocaleZH, LocaleEN} {
		_, body := DefaultsFor(TriggerFileDisplaced, locale)
		if strings.Contains(body, "taker.group") {
			t.Fatalf("%s: default reads the release group %q", locale, body)
		}
		facts := deleteFactsWithPrefix(Sample(TriggerFileDisplaced), "frt.", "taker.frt.")
		facts["taker.group"] = "发布组MISUSE"
		message := renderDefault(t, TriggerFileDisplaced, locale, facts)
		if strings.Contains(message.Body, "发布组MISUSE") {
			t.Fatalf("%s: body presented the release group as the file parse: %q", locale, message.Body)
		}
	}
}

func renderDefault(t *testing.T, trigger, locale string, facts map[string]any) Message {
	t.Helper()
	title, body := DefaultsFor(trigger, locale)
	message, ok := RenderMessage(trigger, title, body, facts)
	if !ok {
		t.Fatalf("%s/%s: unknown trigger", trigger, locale)
	}
	if !message.Valid() {
		t.Fatalf("%s/%s: default does not render: %+v", trigger, locale, message.Errors)
	}
	return message
}

func deleteFactsWithPrefix(facts map[string]any, prefixes ...string) map[string]any {
	for key := range facts {
		for _, prefix := range prefixes {
			if strings.HasPrefix(key, prefix) {
				delete(facts, key)
			}
		}
	}
	return facts
}

// A label whose value is empty must not survive as a dangling line; the
// showIf gate removes the whole line instead.
func assertNoEmptyLabels(t *testing.T, body string) {
	t.Helper()
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasSuffix(line, "：") || strings.HasSuffix(line, ":") {
			t.Fatalf("body has an empty label line %q in %q", line, body)
		}
	}
}

func assertNoFrtWarnings(t *testing.T, message Message) {
	t.Helper()
	for _, warning := range message.Warnings {
		if strings.HasPrefix(warning.Variable, "frt.") || strings.HasPrefix(warning.Variable, "taker.frt.") {
			t.Fatalf("a frt label rendered empty: %+v", warning)
		}
	}
}
