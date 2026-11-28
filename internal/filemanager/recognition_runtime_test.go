package filemanager

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"bansoko/internal/download/domain"
	"bansoko/internal/episodetitle"
	"bansoko/internal/postprocessing/naming"
	"os"
	"path/filepath"
)

type recognitionTitlesFunc func(context.Context, map[episodetitle.WorkKey][]int, map[episodetitle.WorkKey]bool) (map[episodetitle.EpisodeKey]episodetitle.DetailedTitle, error)

func (f recognitionTitlesFunc) ResolveBatch(ctx context.Context, work map[episodetitle.WorkKey][]int, cacheOnly map[episodetitle.WorkKey]bool) (map[episodetitle.EpisodeKey]episodetitle.DetailedTitle, error) {
	return f(ctx, work, cacheOnly)
}

func TestRecognitionRendersKnownAndAbsentFactsBesideUnresolvedFacts(t *testing.T) {
	ctx := context.Background()
	f := newRecognitionFixture(t, fakeRecognitionPolicy{})
	video := f.abs("Loose/Show - 01.mkv")
	subtitle := f.abs("Loose/Show - 01.zh.ass")
	writeFile(t, video, 4)
	writeFile(t, subtitle, 4)
	bgm := episodetitle.WorkKey{Source: episodetitle.SourceBGM, WorkID: "42"}
	tmdb := episodetitle.WorkKey{Source: episodetitle.SourceTMDB, WorkID: "17", Season: 2}
	f.service.SetRecognition(nil, recognitionTitlesFunc(func(_ context.Context, work map[episodetitle.WorkKey][]int, _ map[episodetitle.WorkKey]bool) (map[episodetitle.EpisodeKey]episodetitle.DetailedTitle, error) {
		if len(work) != 2 || !slices.Equal(work[bgm], []int{7}) || !slices.Equal(work[tmdb], []int{7}) {
			t.Fatalf("title dependencies = %v", work)
		}
		return map[episodetitle.EpisodeKey]episodetitle.DetailedTitle{
			{WorkKey: bgm, Episode: 7}:  {},
			{WorkKey: tmdb, Episode: 7}: {Reason: string(episodetitle.LookupUnreachable)},
		}, nil
	}), fakeRecognitionPolicy{})
	prober := &fakeRecognitionMediaProber{facts: map[string]domain.MediaFacts{video: {Resolution: "1080p"}}}
	f.service.SetRecognitionMediaProber(prober)
	request := recognitionRequest(video, subtitle)
	request.Match = &RecognitionMatch{Season: intPointer(0), Episode: intPointer(7), Anime: &RecognitionAnimeDraft{
		Title: "Show", MediaKind: "tv", BGM: &RecognitionAnimeBinding{WorkID: bgm.WorkID},
		TMDB: &RecognitionTMDBBinding{WorkID: tmdb.WorkID, MediaKind: "tv", Season: tmdb.Season},
	}}
	request.Naming.TV.DirectoryTemplate += "/${tmdb.ep_title}"
	request.Naming.TV.VideoFilenameTemplate = "${anime.season}-${media.resolution}-${bgm.ep_title || 'no-bgm'}.${file.ext}"
	request.Naming.SubtitleSuffixTemplate = "${tmdb.ep_title}"
	preview, err := f.service.PreviewRecognition(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	facts := preview.RuntimeFacts
	if len(facts.MediaProbe) != 1 || facts.MediaProbe[0].State != "ready" || len(facts.EpisodeTitles) != 2 ||
		facts.EpisodeTitles[0].State != "absent" || facts.EpisodeTitles[1].Reason != "unreachable" ||
		!strings.HasPrefix(preview.PlanDigest, "r2.blocked.") || len(preview.Conflicts) != 0 {
		t.Fatalf("preview = %+v", preview)
	}
	for _, row := range preview.Rows {
		if row.Disposition != RecognitionError || !row.HasPlaceholder || !strings.Contains(row.TargetPath, "⟨ep_title⟩") ||
			len(row.Blockers) != 1 || row.Blockers[0].Code != RecognitionBlockerRuntimeFacts ||
			!slices.Equal(row.Blockers[0].Facts, []string{"tmdb.ep_title"}) || !slices.Equal(row.Blockers[0].DiagnosticIDs, []string{"titles-2"}) {
			t.Fatalf("row = %+v", row)
		}
	}
	if row := recognitionRow(t, preview, "Show - 01.mkv"); row.TargetName != "0-1080p-no-bgm.mkv" {
		t.Fatalf("video = %+v", row)
	}
	if row := recognitionRow(t, preview, "Show - 01.zh.ass"); row.TargetName != "0-1080p-no-bgm.⟨ep_title⟩.ass" {
		t.Fatalf("subtitle = %+v", row)
	}
}

func TestRecognitionIgnoresRuntimeFactsForSkippedAndOrphanRows(t *testing.T) {
	f := newRecognitionFixture(t, fakeRecognitionPolicy{route: "skip"})
	paths := []string{f.abs("Loose/mystery.mkv"), f.abs("Loose/mystery.zh.ass"), f.abs("Loose/orphan.ass")}
	for _, path := range paths {
		writeFile(t, path, 4)
	}
	prober := &fakeRecognitionMediaProber{}
	f.service.SetRecognitionMediaProber(prober)
	request := recognitionRequest(paths...)
	request.RefreshMedia = true
	request.Naming.Unknown.VideoFilenameTemplate = "${media.resolution}-${fn}"
	request.Naming.SubtitleSuffixTemplate = "${media.hdr}"
	preview, err := f.service.PreviewRecognition(context.Background(), request)
	if err != nil || len(prober.calls) != 0 || len(preview.RuntimeFacts.MediaProbe) != 0 {
		t.Fatalf("preview=%+v probes=%v error=%v", preview, prober.calls, err)
	}
	for _, name := range []string{"mystery.mkv", "mystery.zh.ass"} {
		if row := recognitionRow(t, preview, name); row.Disposition != RecognitionSkip {
			t.Fatalf("skipped row = %+v", row)
		}
	}
	if row := recognitionRow(t, preview, "orphan.ass"); len(row.Blockers) != 1 || row.Blockers[0].Code != RecognitionBlockerVideoMissing {
		t.Fatalf("orphan row = %+v", row)
	}
}

func TestRecognitionRuntimeOptionsAndLiteralPlaceholder(t *testing.T) {
	ctx := context.Background()
	f := newRecognitionFixture(t, fakeRecognitionPolicy{})
	path := f.abs("Loose/Show - 01.mkv")
	writeFile(t, path, 4)
	request := recognitionRequest(path)
	request.Naming.TV.VideoFilenameTemplate = "literal-⟨.mkv"
	preview, err := f.service.PreviewRecognition(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	row := preview.Rows[0]
	if row.Disposition != RecognitionError || row.TargetPath != "" || len(row.Blockers) != 1 || row.Blockers[0].Code != RecognitionBlockerTargetInvalid {
		t.Fatalf("literal placeholder = %+v", row)
	}
	request.Naming.TV.VideoFilenameTemplate = "${media.resolution}-${fn}"
	f.service.SetRecognitionMediaProber(nil)
	preview, err = f.service.PreviewRecognition(ctx, request)
	if err != nil || len(preview.RuntimeFacts.MediaProbe) != 1 || preview.RuntimeFacts.MediaProbe[0].Reason != "probe_unavailable" || !preview.Rows[0].HasPlaceholder {
		t.Fatalf("unwired preview=%+v error=%v", preview, err)
	}
	request.DisableRuntimeFacts = true
	preview, err = f.service.PreviewRecognition(ctx, request)
	if err != nil || len(preview.RuntimeFacts.MediaProbe) != 1 || preview.RuntimeFacts.MediaProbe[0].Reason != "not_loaded" || !preview.Rows[0].HasPlaceholder {
		t.Fatalf("disabled preview=%+v error=%v", preview, err)
	}
	request.RefreshMedia = true
	_, err = f.service.PreviewRecognition(ctx, request)
	mustCode(t, err, CodeRecognitionInvalid)
	request.RefreshMedia = false
	request.EpisodeTitleCacheOnly = []episodetitle.WorkKey{{Source: episodetitle.SourceBGM, WorkID: "42"}}
	_, err = f.service.PreviewRecognition(ctx, request)
	mustCode(t, err, CodeRecognitionInvalid)
	request.DisableRuntimeFacts = false
	request.EpisodeTitleCacheOnly[0].Season = -1
	_, err = f.service.PreviewRecognition(ctx, request)
	mustCode(t, err, CodeRecognitionInvalid)
}

func TestRecognitionTitleDiagnosticsGroupEpisodesAndRoundRetryAfter(t *testing.T) {
	work := episodetitle.WorkKey{Source: episodetitle.SourceTMDB, WorkID: "42", Season: 1}
	unavailable := episodetitle.DetailedTitle{Reason: "rate_limited", RetryAfter: 1100 * time.Millisecond}
	runtime := recognitionRuntime{titles: map[episodetitle.EpisodeKey]episodetitle.DetailedTitle{
		{WorkKey: work, Episode: 3}: unavailable,
		{WorkKey: work, Episode: 2}: {},
		{WorkKey: work, Episode: 1}: unavailable,
	}, titleIDs: map[episodetitle.EpisodeKey]string{}}
	var plan recognitionPlan
	runtime.indexTitles(&plan)
	diagnostics := plan.runtimeFacts.EpisodeTitles
	if len(diagnostics) != 2 || diagnostics[0].ID != "titles-1" || !slices.Equal(diagnostics[0].Episodes, []int{1, 3}) ||
		diagnostics[0].RetryAfterSeconds == nil || *diagnostics[0].RetryAfterSeconds != 2 || diagnostics[1].State != "absent" {
		t.Fatalf("diagnostics = %+v", diagnostics)
	}
}

type stubRecognitionAnimeFacts struct {
	loaded naming.AnimeNamingFacts
}

func (s stubRecognitionAnimeFacts) LoadAnimeFacts(context.Context, string) (naming.AnimeNamingFacts, error) {
	return s.loaded, nil
}

func (stubRecognitionAnimeFacts) FillWorkTitles(context.Context, *naming.AnimeNamingFacts) error {
	return nil
}

func TestRecognitionAnimeDraftReplacesAndClearsStoredFacts(t *testing.T) {
	storedYear, storedSeason := 2019, 2
	svc := &Service{
		recognitionFacts: stubRecognitionAnimeFacts{
			loaded: naming.AnimeNamingFacts{
				Title: "Stored", TitleOverride: "Stored override", ProviderTitle: "Provider",
				Year: &storedYear, Season: &storedSeason,
				BGM:  naming.BGMNamingFacts{WorkID: "bgm-1", Title: "BGM"},
				TMDB: naming.TMDBNamingFacts{WorkID: "tmdb-1", MediaKind: "tv", Season: storedSeason, SeasonPresent: true},
			},
		},
	}
	year := 2024
	replaced, err := svc.recognitionAnimeFacts(context.Background(), &RecognitionMatch{
		ManagedAnimeID: "anime-1",
		Anime: &RecognitionAnimeDraft{
			Title: "Edited", MediaKind: "movie", Year: &year,
			BGM:  &RecognitionAnimeBinding{WorkID: "bgm-9"},
			TMDB: &RecognitionTMDBBinding{WorkID: "99", MediaKind: "movie", Season: 3},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if replaced.Title != "Edited" || replaced.TitleOverride != "Edited" || replaced.Year == nil || *replaced.Year != 2024 ||
		replaced.BGM.WorkID != "bgm-9" || replaced.TMDB.WorkID != "99" || replaced.TMDB.MediaKind != "movie" ||
		!replaced.TMDB.SeasonPresent || replaced.TMDB.Season != 3 || replaced.Season == nil || *replaced.Season != 2 {
		t.Fatalf("replaced = %+v", replaced)
	}
	cleared, err := svc.recognitionAnimeFacts(context.Background(), &RecognitionMatch{
		ManagedAnimeID: "anime-1",
		Anime:          &RecognitionAnimeDraft{Title: "Edited", MediaKind: "tv"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if cleared.Year != nil || cleared.BGM.WorkID != "" || cleared.TMDB.WorkID != "" || cleared.TMDB.MediaKind != "" || cleared.TMDB.SeasonPresent {
		t.Fatalf("cleared = %+v", cleared)
	}
}

func TestRecognitionPreparesOnlyFileTitleEvidence(t *testing.T) {
	filePath := filepath.Join(t.TempDir(), "[ExampleGroup] Example - 01 [1080p].mkv")
	writeFile(t, filePath, 4)
	info, err := os.Lstat(filePath)
	if err != nil {
		t.Fatal(err)
	}
	file := recognitionFile{
		inspection: inspection{
			absolute: filePath,
			info:     info,
		},
		kind: "video",
	}
	file.inspection.detail.Entry.Name = filepath.Base(filePath)
	ctx := recognitionFileFacts(file, naming.AnimeNamingFacts{}, nil, nil, nil)
	facts := naming.UnifiedFacts(ctx)
	if got, _ := facts.FileRt.Get("Resolution"); got != "1080p" {
		t.Fatalf("frt resolution = %q", got)
	}
	if got, _ := facts.Rt.Get("Resolution"); got != "" {
		t.Fatalf("file evidence leaked into rt: %q", got)
	}
	if ctx.File.Media.Resolution != "" {
		t.Fatal("file title leaked into measured media")
	}
}
