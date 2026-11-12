package naming

import (
	"reflect"
	"strings"
	"testing"
)

func TestShortRootsRenderAndAnalyzeAsCanonicalFacts(t *testing.T) {
	engine := NewEngine()
	ctx := sampleTVContext()
	for source, want := range map[string]string{
		`${a.title}|${r.title}|${f.ext}|${m.resolution}|${t.size ?? 0}`: "进击的巨人|" + ctx.Release.OriginalTitle + "|mkv|1080p|0",
		`${a["title"]}|${f["ext"]}`:                                     "进击的巨人|mkv",
		`${pad(a.season, 2)}:${f.name.includes("07")}`:                  "01:true",
	} {
		compiled, err := engine.Compile(KindVideoFilename, source)
		if err != nil {
			t.Fatalf("%s compile: %v", source, err)
		}
		got, err := engine.Render(compiled, ctx)
		if err != nil || got != want {
			t.Fatalf("%s render = %q, %v; want %q", source, got, err, want)
		}
	}
	compiled, err := engine.Compile(KindVideoFilename, `${a.title}.${f.ext}`)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(compiled.RequiredFacts, []string{"anime.title", "file.ext"}) {
		t.Fatalf("short-root required facts = %v", compiled.RequiredFacts)
	}
}

func TestRetiredNamesAreRejected(t *testing.T) {
	engine := NewEngine()
	for _, source := range []string{
		`${ep}`,
		`${rt.subgroup}`,
		`${file.rt.group}`,
		`${f.rt.group}`,
		`${tmdb.original_title}`,
		`${release.lang_tag}`,
		`${file.sub.lang}`,
		`${file.sub.langs}`,
		`${ep.number_end}`,
		`${anibt.resolution}`,
		`${anibt.language}`,
	} {
		t.Run(source, func(t *testing.T) {
			_, err := engine.Compile(KindVideoFilename, source)
			if err == nil || !strings.Contains(err.Error(), "unknown context field") && !strings.Contains(err.Error(), "unknown template identifier") {
				t.Fatalf("compile error = %v, want a retired-name refusal", err)
			}
		})
	}
}
