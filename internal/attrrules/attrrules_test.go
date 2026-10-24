package attrrules

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testYAML = `version: 1
resolution: [2160p, 1080p]
quality: [web-dl]
language: [chs]
special_markers: [special]
group_brackets: ["[", "]"]
`

func TestParseCanonicalFingerprintIgnoresCommentsAndKeyOrder(t *testing.T) {
	one, err := Parse([]byte(testYAML), "rules.yaml")
	if err != nil {
		t.Fatal(err)
	}
	two, err := Parse([]byte("# comment\ngroup_brackets: ['[', ']']\nspecial_markers: [special]\nlanguage: [chs]\nquality: [web-dl]\nresolution: [2160p, 1080p]\nversion: 1\n"), "rules.yaml")
	if err != nil {
		t.Fatal(err)
	}
	first, err := Fingerprint(one)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Fingerprint(two)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("fingerprints differ: %s %s", first, second)
	}
}

func TestValidationReportsField(t *testing.T) {
	cases := []struct {
		name, data, field string
	}{
		{"version", "version: 2", "version"},
		{"quality", "version: 1\nresolution: [1080p]\nquality: []\nlanguage: [chs]\nspecial_markers: [special]\ngroup_brackets: ['[', ']']", "quality"},
		{"brackets", "version: 1\nresolution: [1080p]\nquality: [web-dl]\nlanguage: [chs]\nspecial_markers: [special]\ngroup_brackets: ['[']", "group_brackets"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.data), "/config/attr-rules.yaml")
			if err == nil || !strings.Contains(err.Error(), "/config/attr-rules.yaml") || !strings.Contains(err.Error(), tc.field) {
				t.Fatalf("error lacks actionable path/field: %v", err)
			}
		})
	}
}

func TestInitializeCopiesMissingDefaults(t *testing.T) {
	root := t.TempDir()
	defaults := filepath.Join(root, "defaults", "attr-rules.yaml")
	config := filepath.Join(root, "config", "attr-rules.yaml")
	if err := os.MkdirAll(filepath.Dir(defaults), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(defaults, []byte(testYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := Initialize(t.Context(), Store{}, config, defaults)
	if err == nil || !strings.Contains(err.Error(), "database is nil") {
		t.Fatalf("expected database error after copy, got %v", err)
	}
	got, readErr := os.ReadFile(config)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != testYAML {
		t.Fatalf("defaults were not copied")
	}
}

func TestInitializeRejectsInvalidExistingFile(t *testing.T) {
	root := t.TempDir()
	config := filepath.Join(root, "config", "attr-rules.yaml")
	if err := os.MkdirAll(filepath.Dir(config), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, []byte("version: 99\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := Initialize(t.Context(), Store{}, config, filepath.Join(root, "missing-defaults"))
	if err == nil || !strings.Contains(err.Error(), config) || !strings.Contains(err.Error(), "version") {
		t.Fatalf("invalid file error missing path/field: %v", err)
	}
}
