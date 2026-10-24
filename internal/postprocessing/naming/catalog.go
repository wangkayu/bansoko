package naming

import (
	providerdomain "bansoko/internal/provider/domain"
	"bansoko/internal/templatefacts"
)

// CatalogField documents one template-visible object field path.
type CatalogField struct {
	Path        string `json:"path"`
	Type        string `json:"type"`
	Nullable    bool   `json:"nullable"`
	Source      string `json:"source"`
	Description string `json:"description"`
	SampleValue string `json:"sample_value"`
	// Phase is release or execution (templatefacts.Phase).
	Phase string `json:"phase"`
	// LabelKey and DescriptionKey name the generated copy of the field.
	LabelKey       string `json:"label_key,omitempty"`
	DescriptionKey string `json:"description_key,omitempty"`
}

// CatalogFunction documents one whitelisted template helper.
type CatalogFunction struct {
	Name        string   `json:"name"`
	Signature   string   `json:"signature"`
	Arguments   []string `json:"arguments"`
	Description string   `json:"description"`
	Example     string   `json:"example"`
}

// SampleLockedTarget carries the frozen bound-video target a subtitle sample
// is bound to: the rendered directory, the video basename, and the video
// extension. It is fixed redacted sample data, not a resolver output, and it
// never enters the template context.
type SampleLockedTarget struct {
	Directory string `json:"directory"`
	Basename  string `json:"basename"`
	Extension string `json:"extension"`
}

// CatalogSample is a fixed redacted context used by preview and tests.
// Subtitle samples additionally carry the frozen bound-video target their
// suffix preview is composed against.
type CatalogSample struct {
	Ref     string              `json:"ref"`
	Label   string              `json:"label"`
	Context NamingContext       `json:"context"`
	Locked  *SampleLockedTarget `json:"locked,omitempty"`
}

// CatalogDocument is the generated naming template catalog:
// object schema, function signatures, shorthand tokens, and fixed redacted
// samples.
type CatalogDocument struct {
	Language   string            `json:"language"`
	Fields     []CatalogField    `json:"fields"`
	Functions  []CatalogFunction `json:"functions"`
	Shorthands []Shorthand       `json:"shorthands"`
	Samples    []CatalogSample   `json:"samples"`
}

// GenerateCatalogDocument builds the naming template catalog from the same
// tables the engine enforces, so editor help cannot drift from execution:
// fields come from the templatefacts registry.
func GenerateCatalogDocument() CatalogDocument {
	return CatalogDocument{
		Language:   Language,
		Fields:     generateCatalogFields(),
		Functions:  catalogFunctions(),
		Shorthands: append([]Shorthand(nil), Shorthands...),
		Samples: []CatalogSample{
			{Ref: "sample-tv", Label: "TV episode with fansub group, 1080p HEVC, simplified Chinese", Context: sampleTVContext()},
			{Ref: "sample-movie", Label: "Movie with 1080p AVC and no episode or language facts", Context: sampleMovieContext()},
			{Ref: "sample-tv-subtitle", Label: "Simplified Chinese forced subtitle bound to the sample TV episode", Context: sampleTVSubtitleContext(), Locked: sampleTVSubtitleLocked()},
		},
	}
}

// ResolveSampleContext returns one fixed redacted sample with Parsed/Derived
// filled by the default resolver registry, keeping preview and execution on
// the same resolver path. The second return value reports whether the ref is
// a known sample.
func ResolveSampleContext(ref string) (NamingContext, bool) {
	for _, sample := range GenerateCatalogDocument().Samples {
		if sample.Ref == ref {
			resolved := DefaultRegistry().ResolveAll(ResolverInput{Facts: sample.Context})
			return resolved.Context, true
		}
	}
	return NamingContext{}, false
}

func catalogFunctions() []CatalogFunction {
	return helperCatalog()
}

// intPtr is the fixed-sample year helper; nil means unknown.
func intPtr(value int) *int { return &value }

func sampleTVContext() NamingContext {
	return NamingContext{
		Anime: AnimeNamingFacts{
			ID:     "ma_silent_witch_001",
			Title:  "进击的巨人",
			Year:   intPtr(2025),
			Season: intPtr(1),
			BGM:    BGMNamingFacts{WorkID: "12345"},
			TMDB:   TMDBNamingFacts{WorkID: "259972", MediaKind: "tv", Season: 1, Year: intPtr(2025)},
		},
		Source: SourceNamingFacts{Kind: "provider_group", GroupName: "示例字幕组", Group: providerdomain.GroupRef{Provider: providerdomain.ProviderAniBT, ExternalID: "shilizimuazu"}},
		Release: ReleaseNamingFacts{
			ID: "rel_001", OriginalTitle: "[示例字幕组] 进击的巨人 - 07 [WebRip 1080p HEVC-10bit AAC][CHS]",
			Version: 1, FrozenEpisode: &FrozenEpisodeNamingFacts{Raw: "07", Number: "7", Display: "7"},
		},
		File: FileNamingFacts{
			OriginalName: "[示例字幕组] 进击的巨人 - 07 [WebRip 1080p HEVC-10bit AAC].mkv",
			Basename:     "[示例字幕组] 进击的巨人 - 07 [WebRip 1080p HEVC-10bit AAC]", Extension: "mkv",
			SizeBytes: 734003200, Kind: "video", Media: MediaNamingFacts{Resolution: "1080p", VideoCodec: "HEVC", Container: "mkv"},
		},
		// An AniBT release: both value namespaces are frozen.
		Rt: templatefacts.Values{"Title": "进击的巨人", "Subgroup": "示例字幕组", "Resolution": "1080p", "Source": "WEBRip", "Codec": "HEVC", "SubLang": "SC"},
		AniBTTags: templatefacts.Values{
			"Resolution": "1080p", "Subtitle": "INTERNAL", "Language": "CHS", "Format": "MKV", "Tag": "10bit+AAC+HEVC+WEBRip",
		},
	}
}

// sampleTVSubtitleContext is the standalone subtitle companion of sample-tv:
// same anime, source, and release, with the subtitle file facts Layer 1
// carries. Parsed.Subtitle (format, suffix languages, flags) is filled by the
// resolver registry like every other sample.
func sampleTVSubtitleContext() NamingContext {
	ctx := sampleTVContext()
	ctx.File = FileNamingFacts{
		OriginalName: "[示例字幕组] 进击的巨人 - 07 [WebRip 1080p HEVC-10bit AAC].chs_forced.ass",
		Basename:     "[示例字幕组] 进击的巨人 - 07 [WebRip 1080p HEVC-10bit AAC].chs_forced",
		Extension:    "ass",
		SizeBytes:    102400,
		Kind:         "subtitle",
	}
	return ctx
}

// sampleTVSubtitleLocked is the frozen bound-video target for the subtitle
// sample: the default-rules rendering of sample-tv, kept as fixed sample data
// (a catalog gate test proves it stays in sync).
func sampleTVSubtitleLocked() *SampleLockedTarget {
	return &SampleLockedTarget{
		Directory: "/media/TV/进击的巨人 (2025)/Season 01",
		Basename:  "进击的巨人 S01E07",
		Extension: "mkv",
	}
}

func sampleMovieContext() NamingContext {
	return NamingContext{
		Anime: AnimeNamingFacts{
			ID:    "ma_aot_movie_001",
			Title: "进击的巨人 剧场版",
			Year:  intPtr(2022),
			BGM:   BGMNamingFacts{WorkID: "444444", Title: "進撃の巨人 劇場版", TitleCN: "进击的巨人 剧场版"},
			TMDB: TMDBNamingFacts{
				WorkID:    "888888",
				MediaKind: "movie",
				Season:    0,
				TitleCN:   "进击的巨人 剧场版",
				Year:      intPtr(2022),
			},
		},
		Source: SourceNamingFacts{Kind: "custom_rss", GroupName: "示例字幕组"},
		Release: ReleaseNamingFacts{
			ID:            "rel_002",
			OriginalTitle: "[示例字幕组] 进击的巨人 剧场版 (WebRip 1920x1080 AVC AAC MP4)",
			PublishedAt:   "2026-07-01T12:00:00Z",
			Version:       1,
		},
		File: FileNamingFacts{
			OriginalName: "[示例字幕组] 进击的巨人 剧场版 (WebRip 1920x1080 AVC AAC MP4).mp4",
			Basename:     "[示例字幕组] 进击的巨人 剧场版 (WebRip 1920x1080 AVC AAC MP4)",
			Extension:    "mp4",
			SizeBytes:    1503238553,
			Kind:         "video",
			Media:        MediaNamingFacts{Resolution: "1080p", VideoCodec: "AVC", Container: "mp4"},
		},
		// A custom-RSS release: no AniBT values, and reltitle read no codec
		// or subtitle language.
		Rt: templatefacts.Values{"Title": "进击的巨人 剧场版", "Subgroup": "示例字幕组", "Resolution": "1080p", "Source": "WEB-DL"},
	}
}

// generateCatalogFields is the naming language field list: every visible naming
// fact of the templatefacts registry, in registry order, with the catalog's
// type vocabulary.
func generateCatalogFields() []CatalogField {
	facts := templatefacts.ScenarioFacts(templatefacts.ScenarioNaming)
	fields := make([]CatalogField, 0, len(facts))
	for _, fact := range facts {
		if fact.Hidden {
			continue
		}
		fieldType := "string"
		switch fact.Type {
		case templatefacts.TypeInteger:
			fieldType = "int"
		case templatefacts.TypeBoolean:
			fieldType = "bool"
		case templatefacts.TypeList:
			fieldType = "list"
		}
		fields = append(fields, CatalogField{
			Path: fact.Path, Type: fieldType, Nullable: fact.NullableIn(templatefacts.ScenarioNaming), Source: fact.Root(),
			Description: fact.Description.EN, SampleValue: templatefacts.FormatSample(fact.SampleFor(templatefacts.ScenarioNaming)),
			Phase: string(fact.Phase), LabelKey: fact.LabelKey(), DescriptionKey: fact.DescriptionKey(),
		})
	}
	return fields
}
