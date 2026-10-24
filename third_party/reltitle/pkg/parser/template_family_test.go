package parser

import (
	"testing"

	"github.com/wangkayu/reltitle/pkg/config"
	"github.com/wangkayu/reltitle/pkg/model"
)

func TestDetectRouteFamily_RawMedia(t *testing.T) {
	cfg := &config.Config{
		RouteFamilies: config.RouteFamiliesConfig{
			Families: []config.RouteFamilyRule{
				{
					ID:       "raw_media",
					Enabled:  true,
					Priority: 100,
					Match: config.RouteFamilyMatch{
						RawMustContainAny: []string{"bdmv"},
						SourceTypeIn:      []string{"BD"},
						NoteIn:            []string{"BDMV"},
					},
				},
			},
		},
	}
	attrs := []model.Attribute{
		{Type: model.AttrSourceType, Normalized: "BD"},
		{Type: model.AttrNote, Normalized: "BDMV"},
	}
	tokens := []model.Token{
		{Text: "[RawGroup]"},
		{Text: "Title"},
		{Text: "BDMV"},
	}
	rule := detectRouteFamily(cfg, attrs, tokens)
	if rule == nil || rule.ID != "raw_media" {
		t.Fatalf("route family=%v want raw_media", rule)
	}
}

func TestMatchTemplate_RawMediaPreferredCategory(t *testing.T) {
	cfg := &config.Config{
		RouteFamilies: config.RouteFamiliesConfig{
			Families: []config.RouteFamilyRule{
				{
					ID:       "raw_media",
					Enabled:  true,
					Priority: 100,
					Match: config.RouteFamilyMatch{
						RawMustContainAny: []string{"bdmv"},
						SourceTypeIn:      []string{"BD"},
					},
					Select: config.RouteFamilySelect{
						PreferredTemplateCategories: []string{"batch", "movie"},
					},
				},
			},
		},
		TemplateCatalog: config.TemplateCatalogConfig{
			TVEpisodeTemplates: []config.TemplateDef{
				{ID: "TF_TV_STANDARD_01", Signature: "G T E", Confidence: 0.95},
			},
			BatchTemplates: []config.TemplateDef{
				{ID: "TF_BATCH_COMPLETE_01", Signature: "G T", Confidence: 0.65},
			},
		},
	}
	attrs := []model.Attribute{
		{Type: model.AttrSourceType, Normalized: "BD"},
	}
	tokens := []model.Token{
		{Text: "["},
		{Text: "RawGroup"},
		{Text: "]"},
		{Text: "Title"},
		{Text: "01"},
		{Text: "BDMV"},
	}

	id, _, _ := matchTemplate(
		cfg,
		"ABS_EP",
		"TV_EP",
		"RawGroup",
		"1",
		"",
		"",
		"",
		"",
		"",
		true,
		false,
		attrs,
		tokens,
	)
	if id != "TF_BATCH_COMPLETE_01" {
		t.Fatalf("template_id=%q want TF_BATCH_COMPLETE_01", id)
	}
}

func TestMatchTemplate_RawMediaStrictRouteIDFilter(t *testing.T) {
	cfg := &config.Config{
		RouteFamilies: config.RouteFamiliesConfig{
			Families: []config.RouteFamilyRule{
				{
					ID:       "raw_media",
					Enabled:  true,
					Priority: 100,
					Match: config.RouteFamilyMatch{
						RawMustContainAny: []string{"bdmv"},
						SourceTypeIn:      []string{"BD"},
					},
					Select: config.RouteFamilySelect{
						StrictTemplateFilter:        true,
						AllowedRouteIDs:             []string{"raw_media_v1"},
						PreferredTemplateCategories: []string{"batch"},
					},
				},
			},
		},
		TemplateCatalog: config.TemplateCatalogConfig{
			TVEpisodeTemplates: []config.TemplateDef{
				{ID: "TF_TV_STANDARD_01", Signature: "G T E", Confidence: 0.95},
			},
			BatchTemplates: []config.TemplateDef{
				{ID: "TF_BATCH_NOT_RAW", Signature: "G T E", Confidence: 0.9},
				{ID: "TF_BATCH_RAW_MEDIA", Signature: "G T E", RouteID: "raw_media_v1", Confidence: 0.6},
			},
		},
	}
	attrs := []model.Attribute{
		{Type: model.AttrSourceType, Normalized: "BD"},
	}
	tokens := []model.Token{
		{Text: "["},
		{Text: "RawGroup"},
		{Text: "]"},
		{Text: "Title"},
		{Text: "01"},
		{Text: "BDMV"},
	}

	id, _, _ := matchTemplate(
		cfg,
		"ABS_EP",
		"TV_EP",
		"RawGroup",
		"1",
		"",
		"",
		"",
		"",
		"",
		false,
		false,
		attrs,
		tokens,
	)
	if id != "TF_BATCH_RAW_MEDIA" {
		t.Fatalf("template_id=%q want TF_BATCH_RAW_MEDIA", id)
	}
}

func TestMatchTemplate_RawMediaStrictFilterFallbackToDefault(t *testing.T) {
	cfg := &config.Config{
		RouteFamilies: config.RouteFamiliesConfig{
			Families: []config.RouteFamilyRule{
				{
					ID:       "raw_media",
					Enabled:  true,
					Priority: 100,
					Match: config.RouteFamilyMatch{
						RawMustContainAny: []string{"bdmv"},
						SourceTypeIn:      []string{"BD"},
					},
					Select: config.RouteFamilySelect{
						StrictTemplateFilter:        true,
						AllowedRouteIDs:             []string{"raw_media_v1"},
						PreferredTemplateCategories: []string{"batch"},
					},
				},
			},
		},
		TemplateCatalog: config.TemplateCatalogConfig{
			TVEpisodeTemplates: []config.TemplateDef{
				{ID: "TF_TV_STANDARD_01", Signature: "G T E", Confidence: 0.95},
			},
			BatchTemplates: []config.TemplateDef{
				{ID: "TF_BATCH_NOT_RAW", Signature: "G T E", Confidence: 0.9},
			},
		},
	}
	attrs := []model.Attribute{
		{Type: model.AttrSourceType, Normalized: "BD"},
	}
	tokens := []model.Token{
		{Text: "["},
		{Text: "RawGroup"},
		{Text: "]"},
		{Text: "Title"},
		{Text: "01"},
		{Text: "BDMV"},
	}

	id, _, _ := matchTemplate(
		cfg,
		"ABS_EP",
		"TV_EP",
		"RawGroup",
		"1",
		"",
		"",
		"",
		"",
		"",
		false,
		false,
		attrs,
		tokens,
	)
	if id != "TF_TV_STANDARD_01" {
		t.Fatalf("template_id=%q want TF_TV_STANDARD_01", id)
	}
}

func TestMatchTemplate_StrictRouteTemplateExcludedWithoutFamily(t *testing.T) {
	cfg := &config.Config{
		RouteFamilies: config.RouteFamiliesConfig{
			Families: []config.RouteFamilyRule{
				{
					ID:       "raw_media",
					Enabled:  true,
					Priority: 100,
					Match: config.RouteFamilyMatch{
						RawMustContainAny: []string{"bdmv"},
						SourceTypeIn:      []string{"BD"},
						NoteIn:            []string{"BDMV"},
					},
					Select: config.RouteFamilySelect{
						StrictTemplateFilter:        true,
						AllowedRouteIDs:             []string{"raw_media_v1"},
						PreferredTemplateCategories: []string{"batch"},
					},
				},
			},
		},
		TemplateCatalog: config.TemplateCatalogConfig{
			BatchTemplates: []config.TemplateDef{
				{ID: "TF_BATCH_RAW_ONLY", Signature: "G T", RouteID: "raw_media_v1", Confidence: 0.95},
				{ID: "TF_BATCH_GENERIC", Signature: "G T", Confidence: 0.7},
			},
		},
	}

	attrs := []model.Attribute{
		{Type: model.AttrSourceType, Normalized: "WEB-DL"},
	}
	tokens := []model.Token{
		{Text: "["},
		{Text: "Group"},
		{Text: "]"},
		{Text: "Title"},
	}

	id, _, _ := matchTemplate(
		cfg,
		"NONE",
		"BATCH",
		"Group",
		"",
		"",
		"",
		"",
		"",
		"",
		false,
		false,
		attrs,
		tokens,
	)
	if id != "TF_BATCH_GENERIC" {
		t.Fatalf("template_id=%q want TF_BATCH_GENERIC", id)
	}
}

func TestMatchTemplate_StrictRouteTemplateAllowedWithFamily(t *testing.T) {
	cfg := &config.Config{
		RouteFamilies: config.RouteFamiliesConfig{
			Families: []config.RouteFamilyRule{
				{
					ID:       "raw_media",
					Enabled:  true,
					Priority: 100,
					Match: config.RouteFamilyMatch{
						RawMustContainAny: []string{"bdmv"},
						SourceTypeIn:      []string{"BD"},
						NoteIn:            []string{"BDMV"},
					},
					Select: config.RouteFamilySelect{
						StrictTemplateFilter:        true,
						AllowedRouteIDs:             []string{"raw_media_v1"},
						PreferredTemplateCategories: []string{"batch"},
					},
				},
			},
		},
		TemplateCatalog: config.TemplateCatalogConfig{
			BatchTemplates: []config.TemplateDef{
				{ID: "TF_BATCH_RAW_ONLY", Signature: "G T", RouteID: "raw_media_v1", Confidence: 0.6},
				{ID: "TF_BATCH_GENERIC", Signature: "G T", Confidence: 0.9},
			},
		},
	}

	attrs := []model.Attribute{
		{Type: model.AttrSourceType, Normalized: "BD"},
		{Type: model.AttrNote, Normalized: "BDMV"},
	}
	tokens := []model.Token{
		{Text: "["},
		{Text: "Group"},
		{Text: "]"},
		{Text: "Title"},
		{Text: "BDMV"},
	}

	id, _, _ := matchTemplate(
		cfg,
		"NONE",
		"BATCH",
		"Group",
		"",
		"",
		"",
		"",
		"",
		"",
		false,
		false,
		attrs,
		tokens,
	)
	if id != "TF_BATCH_RAW_ONLY" {
		t.Fatalf("template_id=%q want TF_BATCH_RAW_ONLY", id)
	}
}

func TestMatchTemplate_RawMediaPrefersRawBatchClone(t *testing.T) {
	cfg := &config.Config{
		RouteFamilies: config.RouteFamiliesConfig{
			Families: []config.RouteFamilyRule{
				{
					ID:       "raw_media",
					Enabled:  true,
					Priority: 100,
					Match: config.RouteFamilyMatch{
						RawMustContainAny: []string{"bdmv"},
						SourceTypeIn:      []string{"BD"},
						NoteIn:            []string{"BDMV"},
					},
					Select: config.RouteFamilySelect{
						StrictTemplateFilter:        true,
						AllowedRouteIDs:             []string{"raw_media_v1"},
						PreferredTemplateCategories: []string{"batch"},
					},
				},
			},
		},
		TemplateCatalog: config.TemplateCatalogConfig{
			BatchTemplates: []config.TemplateDef{
				{
					ID:         "TF_BATCH_COMPLETE_01",
					Signature:  "G T C X",
					Confidence: 0.95,
				},
				{
					ID:         "TF_BATCH_RAW_COMPLETE_01",
					Signature:  "G T C X",
					RouteID:    "raw_media_v1",
					Confidence: 0.96,
					Conditions: &config.TemplateConditions{
						Require: &config.TemplateConditionRequire{
							SourceTypeIn: []string{"BD"},
						},
					},
				},
			},
		},
	}

	attrs := []model.Attribute{
		{Type: model.AttrSourceType, Normalized: "BD"},
		{Type: model.AttrNote, Normalized: "BDMV"},
	}
	tokens := []model.Token{
		{Text: "[Group]"},
		{Text: "Title"},
		{Text: "Complete"},
		{Text: "BDMV"},
	}

	id, _, _ := matchTemplate(
		cfg,
		"NONE",
		"BATCH",
		"Group",
		"",
		"",
		"",
		"BATCH",
		"",
		"",
		true,
		false,
		attrs,
		tokens,
	)
	if id != "TF_BATCH_RAW_COMPLETE_01" {
		t.Fatalf("template_id=%q want TF_BATCH_RAW_COMPLETE_01", id)
	}
}
