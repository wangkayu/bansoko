package naming

import (
	"errors"
	"strings"
	"testing"

	"bansoko/internal/templatefacts"
)

func namespaceContext(rt, anibt templatefacts.Values) NamingContext {
	ctx := sampleTVContext()
	ctx.Rt, ctx.AniBTTags = rt, anibt
	return ctx
}

func renderNamespaceTemplate(t *testing.T, source string, ctx NamingContext) (string, error) {
	t.Helper()
	engine := NewEngine()
	compiled, err := engine.Compile(KindVideoFilename, source)
	if err != nil {
		t.Fatalf("compile %s: %v", source, err)
	}
	return engine.Render(compiled, DefaultRegistry().ResolveAll(ResolverInput{Facts: ctx}).Context)
}

func TestNamespaceRootsRenderFrozenValues(t *testing.T) {
	ctx := namespaceContext(
		templatefacts.Values{"Title": "进击的巨人", "Subgroup": "示例字幕组", "Version": "v2", "SubLang": "SC+TC"},
		templatefacts.Values{"Language": "CHS+CHT", "Tag": "10bit+HEVC"},
	)
	got, err := renderNamespaceTemplate(t, "[${rt.subgroup}] ${rt.title} ${rt.version} [${rt.sub_lang}][${anibt.language}][${anibt.tag}].${file.ext}", ctx)
	if err != nil {
		t.Fatal(err)
	}
	if want := "[示例字幕组] 进击的巨人 v2 [SC+TC][CHS+CHT][10bit+HEVC].mkv"; got != want {
		t.Fatalf("rendered %q, want %q", got, want)
	}
	// The `context.` alias reaches the same roots.
	got, err = renderNamespaceTemplate(t, "${context.rt.subgroup}.${context.file.ext}", ctx)
	if err != nil || got != "示例字幕组.mkv" {
		t.Fatalf("alias render = %q, %v", got, err)
	}
}

func TestMissingNamespaceValueFailsAsMissingFact(t *testing.T) {
	cases := []struct {
		name, source, reason string
		rt, anibt            templatefacts.Values
	}{
		// A release whose title_tags block is v1 has no rt.title.
		{"rt title from v1", "${rt.title}.${file.ext}", "naming_fact_missing_rt_title", templatefacts.Values{"Resolution": "1080p"}, nil},
		// A non-AniBT release has no anibt values.
		{"non-AniBT release", "${anibt.language}.${file.ext}", "naming_fact_missing_anibt_language", templatefacts.Values{"Title": "Show"}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := renderNamespaceTemplate(t, tc.source, namespaceContext(tc.rt, tc.anibt))
			var namingErr *Error
			if !errors.As(err, &namingErr) || namingErr.Code != ErrCodeNamingFactMissing || namingErr.ReasonCode() != tc.reason {
				t.Fatalf("render error = %v, want %s", err, tc.reason)
			}
		})
	}
}

func TestMissingNamespaceValueIsNullUnderExplicitFallback(t *testing.T) {
	ctx := namespaceContext(nil, nil)
	for source, want := range map[string]string{
		"${anibt.language ? `[${anibt.language}]` : ''}x.${file.ext}": "x.mkv",
		"${rt.codec ?? 'none'}.${file.ext}":                           "none.mkv",
		"${defaultValue('raw', rt.title)}.${file.ext}":                "raw.mkv",
		// No cross-namespace fallback exists: the template must say so itself.
		"${coalesce(rt.sub_lang, anibt.language, 'und')}.${file.ext}": "und.mkv",
	} {
		got, err := renderNamespaceTemplate(t, source, ctx)
		if err != nil || got != want {
			t.Fatalf("%s rendered %q, %v; want %q", source, got, err, want)
		}
	}
}

func TestUnexposedNamespaceFieldsAreUnknown(t *testing.T) {
	engine := NewEngine()
	// Default-off reltitle outputs and unknown AniBT dimensions are not fields.
	for _, source := range []string{"${rt.container}", "${rt.audio_lang}", "${anibt.group}", "${rt.Title}"} {
		if _, err := engine.Compile(KindVideoFilename, source); err == nil || !strings.Contains(err.Error(), "unknown context field") {
			t.Fatalf("%s compile error = %v, want unknown field", source, err)
		}
	}
}

func TestNamespaceCatalogFields(t *testing.T) {
	fields := map[string]CatalogField{}
	for _, field := range GenerateCatalogDocument().Fields {
		fields[field.Path] = field
	}
	for root, list := range map[string][]templatefacts.Field{templatefacts.RootRt: templatefacts.ReltitleFields, templatefacts.RootAnibt: templatefacts.AniBTTagFields} {
		for _, definition := range list {
			field, ok := fields[root+"."+definition.Name]
			if !ok || !field.Nullable || field.Type != "string" || field.SampleValue == "" || field.Description == "" {
				t.Fatalf("catalog field %s.%s = %+v, %v", root, definition.Key, field, ok)
			}
		}
	}
}

func TestMovieSampleRendersMissingAniBTAsMissing(t *testing.T) {
	ctx, ok := ResolveSampleContext("sample-movie")
	if !ok {
		t.Fatal("movie sample")
	}
	engine := NewEngine()
	compiled, err := engine.Compile(KindVideoFilename, "${anibt.resolution}.${file.ext}")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Render(compiled, ctx); err == nil || !strings.Contains(err.Error(), "anibt.resolution") {
		t.Fatalf("movie sample AniBT render error = %v", err)
	}
}
