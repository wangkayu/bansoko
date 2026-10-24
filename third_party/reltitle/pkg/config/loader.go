package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"

	"gopkg.in/yaml.v3"
)

// LoadAll reads every config file from baseDir and validates the result.
//
// All files are required. Decoding is strict: an unknown key is an error, so a
// typo cannot silently disable a rule. Every problem found is reported, not just
// the first one.
func LoadAll(baseDir string) (*Config, error) {
	return loadAll(os.DirFS(baseDir), fmt.Sprintf("directory %q", baseDir))
}

// LoadAllFS is LoadAll over an arbitrary file system, e.g. the embedded default
// configuration in the top-level configs package.
func LoadAllFS(fsys fs.FS) (*Config, error) {
	return loadAll(fsys, "embedded config")
}

func loadAll(fsys fs.FS, source string) (*Config, error) {
	cfg := &Config{}
	files := []struct {
		name string
		out  any
	}{
		{"lexicon.yaml", &cfg.Lexicon},
		{"template_catalog.yaml", &cfg.TemplateCatalog},
		{"inference_rules.yaml", &cfg.Inference},
		{"grouplist.yaml", &cfg.GroupList},
		{"dict_canonical_values.yaml", &cfg.DictCanonicalValues},
		{"dict_token_synonyms.yaml", &cfg.DictTokenSynonyms},
		{"dict_alias_noise_words.yaml", &cfg.DictAliasNoiseWords},
		{"dict_note_promotion.yaml", &cfg.DictNotePromotion},
		{"group_route_overrides.yaml", &cfg.GroupRoute},
		{"route_families.yaml", &cfg.RouteFamilies},
		{"group_slot2_platform_rules.yaml", &cfg.GroupSlot2Platform},
		{"pack_profile_rules.yaml", &cfg.PackProfileRules},
		{"title_knowledge.yaml", &cfg.TitleKnowledge},
	}

	var errs []error
	for _, f := range files {
		if err := loadYAMLFile(fsys, f.name, f.out); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", f.name, err))
		}
	}
	if len(errs) > 0 {
		return nil, fmt.Errorf("%s: %w", source, errors.Join(errs...))
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("%s: invalid config: %w", source, err)
	}
	return cfg, nil
}

func loadYAMLFile(fsys fs.FS, name string, out any) error {
	data, err := fs.ReadFile(fsys, name)
	if err != nil {
		return err
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(out); err != nil {
		if errors.Is(err, io.EOF) {
			return errors.New("file is empty")
		}
		return err
	}
	return nil
}
