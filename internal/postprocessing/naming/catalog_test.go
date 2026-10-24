package naming

import (
	"regexp"
	"strings"
	"testing"

	"bansoko/internal/templatefacts"
)

// The generated catalog is the template editor's help. A helper name that the
// language cannot parse is worse than a missing entry: the user copies an
// example that can never compile. `default` is a JavaScript reserved word, so a
// helper with that name is unreachable however it is registered.
var identifierPattern = regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$]*$`)

func TestCatalogFunctionNamesAreCallableIdentifiers(t *testing.T) {
	reserved := map[string]bool{
		"await": true, "break": true, "case": true, "catch": true, "class": true,
		"const": true, "continue": true, "debugger": true, "default": true,
		"delete": true, "do": true, "else": true, "enum": true, "export": true,
		"extends": true, "false": true, "finally": true, "for": true,
		"function": true, "if": true, "import": true, "in": true, "instanceof": true,
		"new": true, "null": true, "return": true, "super": true, "switch": true,
		"this": true, "throw": true, "true": true, "try": true, "typeof": true,
		"var": true, "void": true, "while": true, "with": true, "yield": true,
	}
	functions := GenerateCatalogDocument().Functions
	if len(functions) == 0 {
		t.Fatal("catalog advertises no functions")
	}
	for _, function := range functions {
		if !identifierPattern.MatchString(function.Name) || reserved[function.Name] {
			t.Errorf("catalog advertises %q, which no template can call", function.Name)
		}
	}
}

func TestRuntimeHelpersMatchCatalog(t *testing.T) {
	runtime := jsHelpers()
	catalog := GenerateCatalogDocument().Functions
	catalogNames := make(map[string]bool, len(catalog))
	for _, fn := range catalog {
		catalogNames[fn.Name] = true
	}
	deprecated := map[string]bool{}
	for _, definition := range helperDefinitions {
		deprecated[definition.Name] = definition.deprecated
	}
	for name := range runtime {
		if !catalogNames[name] && !deprecated[name] {
			t.Errorf("runtime helper %q is missing from the catalog", name)
		}
		if catalogNames[name] && deprecated[name] {
			t.Errorf("deprecated helper %q is advertised in the catalog", name)
		}
	}
	for name := range catalogNames {
		if _, ok := runtime[name]; !ok {
			t.Errorf("catalog helper %q is not registered at runtime", name)
		}
	}
}

func TestCatalogFieldsCarryPhaseAndSample(t *testing.T) {
	for _, field := range GenerateCatalogDocument().Fields {
		if field.Phase != string(templatefacts.PhaseRelease) && field.Phase != string(templatefacts.PhaseExecution) {
			t.Errorf("%s: phase %q", field.Path, field.Phase)
		}
		if field.SampleValue == "" {
			t.Errorf("%s: no sample", field.Path)
		}
	}
}

func TestCatalogFieldsExistInProductionSampleContexts(t *testing.T) {
	doc := GenerateCatalogDocument()
	profile := languageProfileOf()
	for _, field := range doc.Fields {
		found := false
		for _, sample := range doc.Samples {
			ctx, ok := ResolveSampleContext(sample.Ref)
			if !ok {
				t.Fatalf("sample %q cannot be resolved", sample.Ref)
			}
			// Walk the template-visible object, so a field is checked under the
			// name templates use rather than its Go struct name.
			facts := renderFacts(ctx, 0)
			object, err := profile.object(&facts, false)
			if err != nil {
				t.Fatalf("sample %q context: %v", sample.Ref, err)
			}
			var current interface{} = object
			for _, part := range strings.Split(field.Path, ".") {
				node, isObject := current.(map[string]interface{})
				if !isObject {
					current = nil
					break
				}
				value, present := node[part]
				if !present {
					current = nil
					break
				}
				current = value
				if value == nil {
					// A present-but-null leaf still exists in the object.
					current = struct{}{}
				}
			}
			if current != nil {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("advertised field %q is absent from production contexts", field.Path)
		}
	}
}

// Catalog examples are placeholder frames (owner ruling 2026-10-24): they
// teach a function's shape with var1/var2 tokens the user substitutes. A
// preset fact pick in an example invites verbatim copying — the exact
// failure the frame convention exists to prevent — so no example may name a
// real fact namespace.
func TestCatalogExamplesArePlaceholderFrames(t *testing.T) {
	factNamespaces := []string{
		"anime.", "ep.", "media.", "sub.", "file.", "release.", "rt.",
		"tmdb.", "bgm.", "torrent.", "parsed.", "derived.", "src.",
		"anibt.", "vars.", "info.",
	}
	for _, function := range GenerateCatalogDocument().Functions {
		if function.Example == "" {
			t.Errorf("%s: catalog example is empty", function.Name)
			continue
		}
		for _, namespace := range factNamespaces {
			if strings.Contains(function.Example, namespace) {
				t.Errorf("%s: example %q presets a %s* fact; examples are var1/var2 placeholder frames", function.Name, function.Example, namespace)
			}
		}
	}
}
