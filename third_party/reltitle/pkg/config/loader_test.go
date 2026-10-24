package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const realConfigDir = "../../configs"

func loadReal(t *testing.T) *Config {
	t.Helper()
	cfg, err := LoadAll(realConfigDir)
	if err != nil {
		t.Fatalf("load real configs: %v", err)
	}
	return cfg
}

// copyConfigs copies the real config files into a temp dir that a test can break.
func copyConfigs(t *testing.T) string {
	t.Helper()
	dst := t.TempDir()
	entries, err := os.ReadDir(realConfigDir)
	if err != nil {
		t.Fatalf("read configs: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".yaml" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(realConfigDir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		if err := os.WriteFile(filepath.Join(dst, e.Name()), data, 0o644); err != nil {
			t.Fatalf("write %s: %v", e.Name(), err)
		}
	}
	return dst
}

func TestLoadAllRealConfigs(t *testing.T) {
	cfg := loadReal(t)
	if len(cfg.Lexicon.Entries) == 0 || len(cfg.TemplateCatalog.TVEpisodeTemplates) == 0 {
		t.Fatalf("real configs loaded but look empty")
	}
}

func TestLoadAllFileProblems(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(t *testing.T, dir string)
		want   string
	}{
		{
			name: "missing file",
			mutate: func(t *testing.T, dir string) {
				if err := os.Remove(filepath.Join(dir, "group_route_overrides.yaml")); err != nil {
					t.Fatal(err)
				}
			},
			want: "group_route_overrides.yaml",
		},
		{
			name: "malformed yaml",
			mutate: func(t *testing.T, dir string) {
				writeFile(t, dir, "route_families.yaml", "families: [")
			},
			want: "route_families.yaml",
		},
		{
			name: "unknown key",
			mutate: func(t *testing.T, dir string) {
				appendFile(t, dir, "pack_profile_rules.yaml", "\nrulez: []\n")
			},
			want: "rulez",
		},
		{
			name: "unknown condition key in an inference rule",
			mutate: func(t *testing.T, dir string) {
				path := filepath.Join(dir, "inference_rules.yaml")
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				s := strings.Replace(string(data), "has_movie_index: true", "has_epsiode: true", 1)
				if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			want: "has_epsiode",
		},
		{
			name: "empty file",
			mutate: func(t *testing.T, dir string) {
				writeFile(t, dir, "title_knowledge.yaml", "")
			},
			want: "title_knowledge.yaml: file is empty",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := copyConfigs(t)
			tc.mutate(t, dir)
			_, err := LoadAll(dir)
			if err == nil {
				t.Fatalf("LoadAll succeeded, want an error mentioning %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

func TestLoadAllReportsEveryFileProblem(t *testing.T) {
	dir := copyConfigs(t)
	writeFile(t, dir, "route_families.yaml", "families: [")
	if err := os.Remove(filepath.Join(dir, "pack_profile_rules.yaml")); err != nil {
		t.Fatal(err)
	}
	_, err := LoadAll(dir)
	if err == nil {
		t.Fatal("LoadAll succeeded")
	}
	for _, want := range []string{"route_families.yaml", "pack_profile_rules.yaml"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestValidateProblems(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(c *Config)
		want   string
	}{
		{"duplicate lexicon id", func(c *Config) {
			c.Lexicon.Entries = append(c.Lexicon.Entries, c.Lexicon.Entries[0])
		}, "duplicate entry id"},
		{"lexicon entry without tokens", func(c *Config) {
			c.Lexicon.Entries[0].Tokens = nil
			c.Lexicon.Entries[0].AnyOf = nil
		}, "neither tokens nor any_of"},
		{"lexicon unknown scope", func(c *Config) {
			c.Lexicon.Entries[0].Scope = "braket"
		}, `unknown scope "braket"`},
		{"lexicon confidence out of range", func(c *Config) {
			c.Lexicon.Entries[0].Confidence = 1.5
		}, "outside [0, 1]"},
		{"template duplicate id", func(c *Config) {
			c.TemplateCatalog.BatchTemplates = append(c.TemplateCatalog.BatchTemplates, c.TemplateCatalog.TVEpisodeTemplates[0])
		}, "duplicate template id"},
		{"template unknown content type", func(c *Config) {
			c.TemplateCatalog.TVEpisodeTemplates[0].ContentTypes = []string{"TV"}
		}, `unknown content type "TV"`},
		{"template unknown signature field", func(c *Config) {
			c.TemplateCatalog.TVEpisodeTemplates[0].Signature = "G T Q"
		}, `unknown signature field "Q"`},
		{"route family dangling route id", func(c *Config) {
			c.RouteFamilies.Families[0].Select.AllowedRouteIDs = []string{"no_such_route"}
		}, `matches no template route_id`},
		{"invalid group regex", func(c *Config) {
			c.GroupSlot2Platform.Rules[0].Match.GroupRegex = "(unclosed"
		}, "invalid regex"},
		{"invalid alias noise regex", func(c *Config) {
			c.DictAliasNoiseWords.AliasNoise.RegexPatterns = append(c.DictAliasNoiseWords.AliasNoise.RegexPatterns, "[")
		}, "invalid regex"},
		{"duplicate override id", func(c *Config) {
			c.GroupRoute.Overrides = append(c.GroupRoute.Overrides, c.GroupRoute.Overrides[0])
		}, "duplicate override id"},
		{"group pattern claimed twice", func(c *Config) {
			c.GroupList.Groups = append(c.GroupList.Groups, GroupDef{Name: "Other", Patterns: []string{c.GroupList.Groups[0].Patterns[0]}})
		}, "is claimed by both"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := loadReal(t)
			tc.mutate(cfg)
			err := cfg.Validate()
			if err == nil {
				t.Fatalf("Validate succeeded, want an error mentioning %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

func TestValidateReportsEveryProblem(t *testing.T) {
	cfg := loadReal(t)
	cfg.Lexicon.Entries[0].Confidence = 2
	cfg.TemplateCatalog.TVEpisodeTemplates[0].Signature = "Q"
	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate succeeded")
	}
	for _, want := range []string{"outside [0, 1]", `unknown signature field "Q"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

// An override may force a template id that is not in the catalog (a virtual id).
func TestValidateAllowsVirtualForceTemplateID(t *testing.T) {
	cfg := loadReal(t)
	cfg.GroupRoute.Overrides[0].Output.ForceTemplateID = "TF_NOT_IN_CATALOG_01"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate rejected a virtual force_template_id: %v", err)
	}
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func appendFile(t *testing.T, dir, name, content string) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(dir, name), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(content); err != nil {
		t.Fatal(err)
	}
}
