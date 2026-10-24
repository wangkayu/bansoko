package application

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	acquisitiontarget "bansoko/internal/acquisition/target"
	animedomain "bansoko/internal/anime/domain"
	animecatalogdomain "bansoko/internal/catalog/domain"
	"bansoko/internal/parser"
	"bansoko/internal/postprocessing/naming"
)

// recordingPreviewParser is a non-default registered parser used to prove the
// saved-revision preview runs the revision's selected parser (and only that
// parser), instead of the legacy preview regex.
type recordingPreviewParser struct {
	kind    string
	episode string

	mu    sync.Mutex
	texts []string
}

func (a *recordingPreviewParser) Descriptor() parser.ParserDescriptor {
	return parser.ParserDescriptor{
		ID:                    a.kind,
		ImplementationVersion: "test-preview-parser-v1",
		OutputContractVersion: "episode-evidence-v1",
		DisplayName:           "Test preview parser",
		Capabilities:          []string{"release_title", "file_name"},
		ConfigVersion:         "test-preview-config-v1",
	}
}

func (a *recordingPreviewParser) ValidateConfig(json.RawMessage) error { return nil }

func (a *recordingPreviewParser) ParseDraft(_ context.Context, input parser.EpisodeParseInput, _ json.RawMessage) (parser.EpisodeEvidenceDraft, error) {
	a.mu.Lock()
	a.texts = append(a.texts, input.Text)
	a.mu.Unlock()
	if strings.TrimSpace(input.Text) == "" {
		return parser.EpisodeEvidenceDraft{Status: parser.StatusMissing, Shape: parser.ShapeUnknown}, nil
	}
	return parser.EpisodeEvidenceDraft{
		Status: parser.StatusValue, Shape: parser.ShapeSingle,
		RawCapture: a.episode, Episode: a.episode,
	}, nil
}

func (a *recordingPreviewParser) parsedInputs() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.texts...)
}

func registerPreviewParser(t *testing.T, kind, episode string) *recordingPreviewParser {
	t.Helper()
	adapter := &recordingPreviewParser{kind: kind, episode: episode}
	if parser.IsRegistered(parser.ParserKind(kind)) {
		t.Fatalf("test parser kind %q is already registered", kind)
	}
	if err := parser.DefaultRegistry().Register(adapter); err != nil {
		t.Fatalf("register test preview parser %q: %v", kind, err)
	}
	return adapter
}

// previewSnapshotSource serves one persisted snapshot whose seeds are the exact
// provider entries a saved revision would render.
type previewSnapshotSource struct {
	seeds        []SeedPreviewFact
	calls        int
	payloadCalls int
}

func (s *previewSnapshotSource) FetchEntryGroupSnapshotByID(_ context.Context, _, _, _ string, _ animecatalogdomain.ProviderRSSPreviewIdentity) (SeedPreviewSnapshot, error) {
	s.calls++
	return SeedPreviewSnapshot{Seeds: s.seeds, Freshness: "fresh"}, nil
}

// ReadSeedPayload is not exercised by these preview tests; a read here would
// mean a preview path started downloading, which it must never do.
func (s *previewSnapshotSource) ReadSeedPayload(_ context.Context, _, _, _ string, _ animecatalogdomain.ProviderRSSPreviewIdentity, _ string) (SeedPayload, error) {
	s.payloadCalls++
	return SeedPayload{}, errors.New("preview must not read seed payloads")
}

// --- revision parser selection carries the frozen media binding ---------------

// defaultResolvedSectionsForPreview builds a complete resolved-sections body
// from the code defaults, the same shape a saved revision stores.
func defaultResolvedSectionsForPreview(t *testing.T) animedomain.ResolvedSections {
	t.Helper()
	resolved := animedomain.ResolvedSections{
		Sections:       map[string]json.RawMessage{},
		Versions:       map[string]int64{},
		SchemaVersions: map[string]string{},
	}
	for _, section := range animedomain.EffectiveSectionOrder {
		value, err := animedomain.DefaultSectionValue(section)
		if err != nil {
			t.Fatalf("default section %s: %v", section, err)
		}
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("encode default section %s: %v", section, err)
		}
		resolved.Sections[section] = raw
		resolved.Versions[section] = 1
		resolved.SchemaVersions[section] = animedomain.SectionSchemaVersions[section]
	}
	return resolved
}

func TestCandidateRevisionParserSelectionCarriesFrozenMediaBinding(t *testing.T) {
	movie := &animedomain.TMDBBinding{WorkId: "265010", MediaKind: animedomain.TMDBMediaKindMovie, Season: 0}
	tv := &animedomain.TMDBBinding{WorkId: "265010", MediaKind: animedomain.TMDBMediaKindTv, Season: 3}
	bgmOnly := &animedomain.BGMBinding{WorkId: "486109"}

	cases := []struct {
		name        string
		tmdb        *animedomain.TMDBBinding
		wantKind    string
		wantSeason  int
		wantBound   bool
		wantMovieID bool
	}{
		{name: "movie binding", tmdb: movie, wantKind: "movie", wantSeason: 0, wantBound: false, wantMovieID: true},
		{name: "tv binding keeps the bound season", tmdb: tv, wantKind: "tv", wantSeason: 3, wantBound: false},
		{name: "bgm only is the unbound tv default", tmdb: nil, wantKind: "tv", wantSeason: 0, wantBound: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			revision := CandidateRevision{
				Draft: animedomain.AnimeDraft{
					Title:    "Preview Show",
					Metadata: animedomain.AnimeMetadataDraft{Bgm: bgmOnly, Tmdb: tc.tmdb},
				},
				ParserPolicy: animedomain.ParserPolicyEnvelope{
					Kind: "regex", PolicyVersion: 1, ProfileVersion: 1, RulesOwnerVersion: 1,
				},
			}
			resolved := defaultResolvedSectionsForPreview(t)
			selection, err := revision.ParserSelectionInput(resolved)
			if err != nil {
				t.Fatalf("selection: %v", err)
			}
			if selection.MediaKind != tc.wantKind || selection.Season != tc.wantSeason || selection.SeasonBound != tc.wantBound {
				t.Fatalf("binding = %s/%d/%t, want %s/%d/%t", selection.MediaKind, selection.Season, selection.SeasonBound, tc.wantKind, tc.wantSeason, tc.wantBound)
			}
			if selection.Kind != "regex" {
				t.Fatalf("policy projection = %s", selection.Kind)
			}
			if tc.wantMovieID {
				evidence, err := acquisitiontarget.ResolveParserEvidence(context.Background(), selection, "Preview Show 2024")
				if err != nil {
					t.Fatalf("resolve movie evidence: %v", err)
				}
				if evidence.Identity.Kind != "movie" || evidence.Identity.CoverageKey == nil || *evidence.Identity.CoverageKey != "movie:0" {
					t.Fatalf("movie identity = %+v", evidence.Identity)
				}
			}
		})
	}
}

// --- saved-revision preview runs the selected parser, exactly once -----------

func TestSavedRevisionPreviewRunsTheSelectedParserOnce(t *testing.T) {
	adapter := registerPreviewParser(t, "test-preview-selected", "42")
	selection := acquisitiontarget.ParserSelectionInput{Kind: adapter.kind, MediaKind: "tv"}
	// Both seeds carry a manifest: only an expanded seed is parsed, and this test
	// is about which parser runs, not about the unexpanded pending row. The file
	// list is empty so the count below is release titles only.
	source := &previewSnapshotSource{seeds: []SeedPreviewFact{
		{StableSeedKey: "seed-1", Title: "[Group] Preview Show - 07 1080p", DownloadURL: "https://example.test/one.torrent", TorrentState: animecatalogdomain.TorrentAcquisitionAvailable, Manifest: titleOnlyManifest(t, "Preview Show - 07")},
		{StableSeedKey: "seed-2", Title: "[Group] Preview Show - 08 1080p", DownloadURL: "https://example.test/two.torrent", TorrentState: animecatalogdomain.TorrentAcquisitionAvailable, Manifest: titleOnlyManifest(t, "Preview Show - 08")},
	}}
	service := &Service{seedPreviews: source}
	draft := previewTestDraft(animedomain.ProviderKindAnibt, "12")
	seedPreviewBusiness(t, service, &draft)
	effective := previewTestEffective(animedomain.ProviderKindAnibt, "12")

	view, err := service.composeRSSListView(context.Background(), "candidate_revision", "candidate#1", draft, effective, selection, []PreviewFeedSnapshotRef{previewTestRef(animedomain.ProviderKindAnibt, "12")}, 0)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	// One parse per release title. The empty file list keeps file-name parses out
	// of the count, so a second parse of the same title would show up here.
	releaseInputs := adapter.parsedInputs()
	if len(releaseInputs) != 2 {
		t.Fatalf("selected parser ran %d times (%v), want exactly one parse per release", len(releaseInputs), releaseInputs)
	}
	if len(view.Groups) != 1 || len(view.Groups[0].Items) != 2 {
		t.Fatalf("groups/items = %+v, want one group with two seeds", view.Groups)
	}
	for _, item := range view.Groups[0].Items {
		// The evaluation carries the selected parser's evidence, which is the
		// observable proof the read model ran that parser and only that parser.
		if item.Evaluation.State != RSSListEvaluationEvaluated {
			t.Fatalf("evaluation = %+v, want evaluated", item.Evaluation)
		}
		if item.Evaluation.ParsedFacts == nil || item.Evaluation.ParsedFacts.Episode == nil || *item.Evaluation.ParsedFacts.Episode != 42 {
			t.Fatalf("episode = %+v, want the selected parser's evidence (42)", item.Evaluation.ParsedFacts)
		}
	}
}

func TestSavedRevisionPreviewFailsClosedWhenTheSelectedParserIsUnavailable(t *testing.T) {
	adapter := registerPreviewParser(t, "test-preview-online", "7")
	selection := acquisitiontarget.ParserSelectionInput{Kind: "test-preview-offline", MediaKind: "tv"}
	// The seed is expanded so the selected parser is actually reached: an
	// unexpanded seed is pending and never selects a parser at all.
	source := &previewSnapshotSource{seeds: []SeedPreviewFact{
		{StableSeedKey: "seed-1", Title: "[Group] Preview Show - 07 1080p", DownloadURL: "https://example.test/one.torrent", TorrentState: animecatalogdomain.TorrentAcquisitionAvailable, Manifest: singleFileManifest(t, "Preview Show - 07.mkv")},
	}}
	service := &Service{seedPreviews: source}
	draft := previewTestDraft(animedomain.ProviderKindAnibt, "12")
	seedPreviewBusiness(t, service, &draft)
	effective := previewTestEffective(animedomain.ProviderKindAnibt, "12")

	_, err := service.composeRSSListView(context.Background(), "candidate_revision", "candidate#1", draft, effective, selection, []PreviewFeedSnapshotRef{previewTestRef(animedomain.ProviderKindAnibt, "12")}, 0)
	if code := businessCode(t, err); code != "PARSER_SELECTION_UNAVAILABLE" {
		t.Fatalf("code = %q, want PARSER_SELECTION_UNAVAILABLE", code)
	}
	if inputs := adapter.parsedInputs(); len(inputs) != 0 {
		t.Fatalf("an unrelated parser ran after the selected parser failed: %v", inputs)
	}
}

// --- movie binding drives the naming branch ----------------------------------

func TestSavedRevisionMoviePreviewUsesMovieIdentityAndNaming(t *testing.T) {
	for _, title := range []string{"Preview Show", "Preview Show (2024)", "Preview Show - 07"} {
		t.Run(title, func(t *testing.T) {
			selection := acquisitiontarget.ParserSelectionInput{Kind: "regex", MediaKind: "movie"}
			source := &previewSnapshotSource{seeds: []SeedPreviewFact{{
				StableSeedKey: "seed-1", Title: title, DownloadURL: "https://example.test/movie.torrent",
				// The movie's video is the torrent's single first-level file.
				Manifest: singleFileManifest(t, "Preview Show Movie.mkv"),
			}}}
			service := &Service{seedPreviews: source}
			draft := previewTestDraft(animedomain.ProviderKindAnibt, "12")
			seedPreviewBusiness(t, service, &draft)
			// Complete metadata: the hold must not mask the naming branch under test.
			draft.Metadata = animedomain.AnimeMetadataDraft{
				Bgm:  &animedomain.BGMBinding{WorkId: "486109"},
				Tmdb: &animedomain.TMDBBinding{WorkId: "265010", MediaKind: animedomain.TMDBMediaKindMovie, Season: 0},
			}
			setDraftOwnerYear(&draft)
			effective := previewTestEffective(animedomain.ProviderKindAnibt, "12")
			effective.Rules.Naming = animedomain.NamingRulesFromDomain(naming.DefaultNamingRules())

			view, err := service.composeRSSListView(context.Background(), "candidate_revision", "candidate#1", draft, effective, selection, []PreviewFeedSnapshotRef{previewTestRef(animedomain.ProviderKindAnibt, "12")}, 0)
			if err != nil {
				t.Fatalf("preview: %v", err)
			}
			if len(view.Groups) != 1 || len(view.Groups[0].Items) != 1 {
				t.Fatalf("groups/items = %+v, want one group with one seed", view.Groups)
			}
			item := view.Groups[0].Items[0]
			if item.Evaluation.Verdict != "accepted_pending_download" {
				t.Fatalf("verdict = %s/%s, want accepted_pending_download", item.Evaluation.Verdict, item.Evaluation.ReasonCode)
			}
			// The canonical movie:0 identity projects episode 0 — never a number
			// taken from the title (a year, or the "- 07" token).
			if item.Evaluation.ParsedFacts == nil || item.Evaluation.ParsedFacts.Episode == nil || *item.Evaluation.ParsedFacts.Episode != 0 {
				t.Fatalf("movie episode scalar = %+v, want the canonical 0", item.Evaluation.ParsedFacts)
			}
			if len(item.Files) != 1 {
				t.Fatalf("files = %d, want 1", len(item.Files))
			}
			rendered := item.Files[0].RenderedTarget
			if rendered == "" {
				t.Fatalf("movie preview rendered no target: safety=%s reason=%s", item.Files[0].Safety, item.Files[0].ReasonCode)
			}
			// The movie branch has no Season folder and no episode marker; a TV
			// render of the same title would carry both.
			if strings.Contains(rendered, "Season") || strings.Contains(rendered, "- E") {
				t.Fatalf("rendered target %q used the TV naming branch for a movie identity", rendered)
			}
			if !strings.Contains(rendered, "Preview Show") {
				t.Fatalf("rendered target %q lost the anime title", rendered)
			}
			// The movie directory is the media kind, the title and the year the
			// bound work's provider observed — never a year read off the title.
			if want := "/media/Movie/Preview Show (2025)/"; !strings.HasPrefix(rendered, want) {
				t.Fatalf("rendered target %q is not under the movie layout %q", rendered, want)
			}
		})
	}

	// Control: the same "- 07" title under a TV binding must render the TV
	// branch, so the movie assertions above are discriminating and not
	// vacuously true.
	t.Run("tv control keeps the season and episode branch", func(t *testing.T) {
		selection := acquisitiontarget.ParserSelectionInput{Kind: "regex", MediaKind: "tv", Season: 2}
		source := &previewSnapshotSource{seeds: []SeedPreviewFact{{
			StableSeedKey: "seed-1", Title: "Preview Show - 07", DownloadURL: "https://example.test/movie.torrent",
			// The episode comes from the file's own name, never the title.
			Manifest: singleFileManifest(t, "Preview Show - 07.mkv"),
		}}}
		service := &Service{seedPreviews: source}
		draft := previewTestDraft(animedomain.ProviderKindAnibt, "12")
		seedPreviewBusiness(t, service, &draft)
		draft.Metadata = animedomain.AnimeMetadataDraft{
			Bgm:  &animedomain.BGMBinding{WorkId: "486109"},
			Tmdb: &animedomain.TMDBBinding{WorkId: "265010", MediaKind: animedomain.TMDBMediaKindTv, Season: 2},
		}
		setDraftOwnerYear(&draft)
		effective := previewTestEffective(animedomain.ProviderKindAnibt, "12")
		effective.Rules.Naming = animedomain.NamingRulesFromDomain(naming.DefaultNamingRules())

		view, err := service.composeRSSListView(context.Background(), "candidate_revision", "candidate#1", draft, effective, selection, []PreviewFeedSnapshotRef{previewTestRef(animedomain.ProviderKindAnibt, "12")}, 0)
		if err != nil {
			t.Fatalf("tv control preview: %v", err)
		}
		if len(view.Groups) != 1 || len(view.Groups[0].Items) != 1 || len(view.Groups[0].Items[0].Files) != 1 {
			t.Fatalf("tv control produced no single-file row: %+v", view.Groups)
		}
		control := view.Groups[0].Items[0].Files[0].RenderedTarget
		if control == "" {
			t.Fatalf("tv control rendered no target: %+v", view.Groups[0].Items[0].Files)
		}
		// The TV branch renders the season folder from the bound season and the
		// episode marker from the typed identity, in the shipped layout: the
		// media kind, the title with the observed year, the bound season
		// directory and the file's own episode.
		if want := "/media/TV/Preview Show (2025)/Season 02/Preview Show S02E07.mkv"; control != want {
			t.Fatalf("tv control target = %q, want the TV layout %q", control, want)
		}
	})
}
