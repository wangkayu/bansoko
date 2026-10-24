package parser

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

func repoFile(t *testing.T, rel string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func catalogNames() []string {
	names := []string{}
	for _, kind := range Kinds() {
		names = append(names, string(kind))
	}
	sort.Strings(names)
	return names
}

func equalSets(a, b []string) bool {
	sort.Strings(a)
	sort.Strings(b)
	return strings.Join(a, ",") == strings.Join(b, ",")
}

func TestKindCatalogMatchesRegistry(t *testing.T) {
	registered := []string{}
	for _, descriptor := range DefaultRegistry().List() {
		registered = append(registered, descriptor.ID)
	}
	if !equalSets(registered, catalogNames()) {
		t.Fatalf("registry %v and catalog %v differ", registered, catalogNames())
	}
	for _, unknown := range []string{"", "traditional", "Regex", "regex ", "llm"} {
		if IsRegistered(ParserKind(unknown)) {
			t.Errorf("kind %q must not be registered", unknown)
		}
	}
}

// The OpenAPI instance enums and the global-policy CHECK migration mirror the
// catalog; this fails when any of them drifts.
func TestKindCatalogMirrors(t *testing.T) {
	spec := repoFile(t, "api/openapi.yaml")
	enum := regexp.MustCompile(`(?m)^\s+parser_kind:\n\s+type: string\n\s+enum: \[([^\]]*)\]`)
	matches := enum.FindAllStringSubmatch(spec, -1)
	if len(matches) == 0 {
		t.Fatal("no parser_kind enum found in openapi.yaml")
	}
	for _, match := range matches {
		values := []string{}
		for _, value := range strings.Split(match[1], ",") {
			values = append(values, strings.TrimSpace(value))
		}
		if !equalSets(values, catalogNames()) {
			t.Errorf("openapi parser_kind enum %v differs from catalog %v", values, catalogNames())
		}
	}
	migration := repoFile(t, "db/migrations/020_parser_kind_value_checks.sql")
	quoted := regexp.MustCompile(`IN \(([^)]*)\)`)
	found := quoted.FindAllStringSubmatch(migration, -1)
	if len(found) != 1 {
		t.Fatalf("expected one value CHECK in the migration, found %d", len(found))
	}
	for _, match := range found {
		values := []string{}
		for _, value := range strings.Split(match[1], ",") {
			values = append(values, strings.Trim(strings.TrimSpace(value), "'"))
		}
		if !equalSets(values, catalogNames()) {
			t.Errorf("migration CHECK %v differs from catalog %v", values, catalogNames())
		}
	}
}
