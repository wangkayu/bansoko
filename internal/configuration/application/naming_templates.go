package application

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/apperrors"
	configurationdomain "bansoko/internal/configuration/domain"
	"bansoko/internal/events"
	"bansoko/internal/postprocessing/naming"
)

// The naming template library is a
// user-managed list of naming rules edited only in the global settings. One
// template is the default and IS the global naming: its body is the
// `naming_rules` row of global_config_sections (or the code default while that
// row is unsaved). naming_template_library stores the list, the default key and
// the bodies of the other templates, so the global naming has one source and
// every existing naming reader is unchanged.
//
// Rules:
//   - at least one template is kept; the default key names one of them;
//   - the default template cannot be deleted: another template is made the
//     default first (both in one save is fine — the check is on the result);
//   - a save that changes the default body writes the naming_rules section in
//     the same transaction (the default a new draft copies; no existing draft
//     or subscription reads it);
//   - a generic naming_rules section save bumps the library version, so a
//     library CAS also covers the default body.

const (
	namingTemplateMaxCount   = 50
	namingTemplateMaxNameLen = 64

	errNamingTemplateListEmpty      = "NAMING_TEMPLATE_LIST_EMPTY"
	errNamingTemplateListTooLong    = "NAMING_TEMPLATE_LIST_TOO_LONG"
	errNamingTemplateKeyInvalid     = "NAMING_TEMPLATE_KEY_INVALID"
	errNamingTemplateKeyDuplicate   = "NAMING_TEMPLATE_KEY_DUPLICATE"
	errNamingTemplateNameInvalid    = "NAMING_TEMPLATE_NAME_INVALID"
	errNamingTemplateNameDuplicate  = "NAMING_TEMPLATE_NAME_DUPLICATE"
	errNamingTemplateDefaultMissing = "NAMING_TEMPLATE_DEFAULT_MISSING"
)

var namingTemplateKeyPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// namingTemplateSeed is the shipped library: one template, the code-default
// naming. Migration 029 inserts the same document.
const namingTemplateSeedKey = "default"
const namingTemplateSeedName = "Default"

type storedNamingTemplate struct {
	Key   string          `json:"key"`
	Name  string          `json:"name"`
	Rules json.RawMessage `json:"rules,omitempty"`
}

type storedNamingTemplateLibrary struct {
	DefaultKey string                 `json:"default_key"`
	Templates  []storedNamingTemplate `json:"templates"`
}

type namingTemplateQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// GetNamingTemplateLibrary returns the library with every body resolved.
func (s *Service) GetNamingTemplateLibrary(ctx context.Context) (configurationdomain.NamingTemplateLibraryView, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return configurationdomain.NamingTemplateLibraryView{}, fmt.Errorf("begin read naming template library: %w", err)
	}
	defer tx.Rollback()
	library, version, err := readNamingTemplateLibrary(ctx, tx, false)
	if err != nil {
		return configurationdomain.NamingTemplateLibraryView{}, err
	}
	defaultRules, _, err := readGlobalNamingRules(ctx, tx, false)
	if err != nil {
		return configurationdomain.NamingTemplateLibraryView{}, err
	}
	return namingTemplateLibraryView(library, version, defaultRules)
}

// PutNamingTemplateLibrary replaces the whole library with expected-version CAS.
func (s *Service) PutNamingTemplateLibrary(ctx context.Context, expected int64, input configurationdomain.NamingTemplateLibraryInput) (configurationdomain.NamingTemplateLibraryView, error) {
	vars, err := namingVarsSnapshot(animedomain.SectionNamingRules)
	if err != nil {
		return configurationdomain.NamingTemplateLibraryView{}, err
	}
	library, defaultRules, err := normalizeNamingTemplateLibrary(input, vars)
	if err != nil {
		return configurationdomain.NamingTemplateLibraryView{}, err
	}
	encoded, err := json.Marshal(library)
	if err != nil {
		return configurationdomain.NamingTemplateLibraryView{}, fmt.Errorf("encode naming template library: %w", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return configurationdomain.NamingTemplateLibraryView{}, fmt.Errorf("begin put naming template library: %w", err)
	}
	defer tx.Rollback()
	// Same lock order as a naming_rules section save: the section advisory
	// lock (which serializes both writers), then the rows.
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "bansoko:global-config-section:"+animedomain.SectionNamingRules); err != nil {
		return configurationdomain.NamingTemplateLibraryView{}, err
	}
	_, current, err := readNamingTemplateLibrary(ctx, tx, true)
	if err != nil {
		return configurationdomain.NamingTemplateLibraryView{}, err
	}
	if current != expected {
		return configurationdomain.NamingTemplateLibraryView{}, &apperrors.VersionConflict{Owner: "naming_template_library", CurrentVersion: current}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO naming_template_library(singleton, value, version) VALUES (TRUE, $1, 1) ON CONFLICT (singleton) DO UPDATE SET value = EXCLUDED.value, version = naming_template_library.version + 1, updated_at = NOW()`, encoded); err != nil {
		return configurationdomain.NamingTemplateLibraryView{}, fmt.Errorf("save naming template library: %w", err)
	}
	// The default body is the global naming (the default a new draft copies).
	// Write it only when it differs.
	res, err := tx.ExecContext(ctx, `INSERT INTO global_config_sections(section_name, schema_version, value, version) VALUES ($1, $2, $3, 1) ON CONFLICT (section_name) DO UPDATE SET schema_version = EXCLUDED.schema_version, value = EXCLUDED.value, version = global_config_sections.version + 1, updated_at = NOW() WHERE global_config_sections.value IS DISTINCT FROM EXCLUDED.value`, animedomain.SectionNamingRules, animedomain.SectionSchemaVersions[animedomain.SectionNamingRules], []byte(defaultRules))
	if err != nil {
		return configurationdomain.NamingTemplateLibraryView{}, fmt.Errorf("save global naming from default template: %w", err)
	}
	changed, err := res.RowsAffected()
	if err != nil {
		return configurationdomain.NamingTemplateLibraryView{}, err
	}
	if err := tx.Commit(); err != nil {
		return configurationdomain.NamingTemplateLibraryView{}, err
	}
	if changed > 0 && s.eventHub != nil {
		s.eventHub.Publish(events.Event{Target: events.TargetSubscribed})
	}
	return s.GetNamingTemplateLibrary(ctx)
}

// readNamingTemplateLibrary reads the stored library, or the shipped seed with
// version 1 when the row is absent (a create is then CAS-checked against 1,
// like an unsaved global section).
func readNamingTemplateLibrary(ctx context.Context, q namingTemplateQueryer, forUpdate bool) (storedNamingTemplateLibrary, int64, error) {
	query := `SELECT value, version FROM naming_template_library WHERE singleton`
	if forUpdate {
		query += ` FOR UPDATE`
	}
	var raw []byte
	var version int64
	err := q.QueryRowContext(ctx, query).Scan(&raw, &version)
	if errors.Is(err, sql.ErrNoRows) {
		return storedNamingTemplateLibrary{DefaultKey: namingTemplateSeedKey, Templates: []storedNamingTemplate{{Key: namingTemplateSeedKey, Name: namingTemplateSeedName}}}, 1, nil
	}
	if err != nil {
		return storedNamingTemplateLibrary{}, 0, fmt.Errorf("read naming template library: %w", err)
	}
	var library storedNamingTemplateLibrary
	if err := json.Unmarshal(raw, &library); err != nil {
		return storedNamingTemplateLibrary{}, 0, fmt.Errorf("decode naming template library: %w", err)
	}
	return library, version, nil
}

// readGlobalNamingRules returns the saved global naming value or the code
// default, and whether a row backs it.
func readGlobalNamingRules(ctx context.Context, q namingTemplateQueryer, forUpdate bool) (json.RawMessage, bool, error) {
	query := `SELECT value FROM global_config_sections WHERE section_name = $1`
	if forUpdate {
		query += ` FOR UPDATE`
	}
	var raw []byte
	err := q.QueryRowContext(ctx, query, animedomain.SectionNamingRules).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		value, err := animedomain.DefaultSectionValue(animedomain.SectionNamingRules)
		if err != nil {
			return nil, false, err
		}
		encoded, err := json.Marshal(value)
		return encoded, false, err
	}
	if err != nil {
		return nil, false, fmt.Errorf("read global naming rules: %w", err)
	}
	return raw, true, nil
}

func namingTemplateLibraryView(library storedNamingTemplateLibrary, version int64, defaultRules json.RawMessage) (configurationdomain.NamingTemplateLibraryView, error) {
	view := configurationdomain.NamingTemplateLibraryView{DefaultKey: library.DefaultKey, Version: version, Templates: make([]configurationdomain.NamingTemplate, 0, len(library.Templates))}
	for _, item := range library.Templates {
		rules := item.Rules
		if item.Key == library.DefaultKey {
			rules = defaultRules
		}
		if len(rules) == 0 {
			return configurationdomain.NamingTemplateLibraryView{}, fmt.Errorf("naming template %q has no body", item.Key)
		}
		view.Templates = append(view.Templates, configurationdomain.NamingTemplate{Key: item.Key, Name: item.Name, Rules: append(json.RawMessage(nil), rules...)})
	}
	return view, nil
}

// normalizeNamingTemplateLibrary validates the submitted library and returns
// the stored document (non-default bodies only) and the default body.
func normalizeNamingTemplateLibrary(input configurationdomain.NamingTemplateLibraryInput, vars map[string]map[string]string) (storedNamingTemplateLibrary, json.RawMessage, error) {
	if len(input.Templates) == 0 {
		return storedNamingTemplateLibrary{}, nil, namingTemplateViolation(errNamingTemplateListEmpty, "templates", "keep at least one naming template")
	}
	if len(input.Templates) > namingTemplateMaxCount {
		return storedNamingTemplateLibrary{}, nil, namingTemplateViolation(errNamingTemplateListTooLong, "templates", fmt.Sprintf("at most %d naming templates", namingTemplateMaxCount))
	}
	keys := map[string]bool{}
	names := map[string]bool{}
	library := storedNamingTemplateLibrary{DefaultKey: input.DefaultKey, Templates: make([]storedNamingTemplate, 0, len(input.Templates))}
	var defaultRules json.RawMessage
	for index, item := range input.Templates {
		prefix := fmt.Sprintf("templates[%d]", index)
		if !namingTemplateKeyPattern.MatchString(item.Key) {
			return storedNamingTemplateLibrary{}, nil, namingTemplateViolation(errNamingTemplateKeyInvalid, prefix+".key", "key must be 1-64 lowercase letters, digits, '-' or '_'")
		}
		if keys[item.Key] {
			return storedNamingTemplateLibrary{}, nil, namingTemplateViolation(errNamingTemplateKeyDuplicate, prefix+".key", "duplicate template key "+item.Key)
		}
		keys[item.Key] = true
		name := strings.TrimSpace(item.Name)
		if name == "" || utf8.RuneCountInString(name) > namingTemplateMaxNameLen {
			return storedNamingTemplateLibrary{}, nil, namingTemplateViolation(errNamingTemplateNameInvalid, prefix+".name", fmt.Sprintf("name must be 1-%d characters", namingTemplateMaxNameLen))
		}
		if names[name] {
			return storedNamingTemplateLibrary{}, nil, namingTemplateViolation(errNamingTemplateNameDuplicate, prefix+".name", "duplicate template name")
		}
		names[name] = true
		rules, err := normalizeNamingTemplateRules(item.Rules, vars, prefix+".rules")
		if err != nil {
			return storedNamingTemplateLibrary{}, nil, err
		}
		stored := storedNamingTemplate{Key: item.Key, Name: name}
		if item.Key == input.DefaultKey {
			defaultRules = rules
		} else {
			stored.Rules = rules
		}
		library.Templates = append(library.Templates, stored)
	}
	if defaultRules == nil {
		return storedNamingTemplateLibrary{}, nil, namingTemplateViolation(errNamingTemplateDefaultMissing, "default_key", "the default template must be one of the templates")
	}
	return library, defaultRules, nil
}

// normalizeNamingTemplateRules runs the naming save validation and drops the
// legacy `preset` reference: a template is its concrete templates.
func normalizeNamingTemplateRules(raw json.RawMessage, vars map[string]map[string]string, field string) (json.RawMessage, error) {
	var value animedomain.NamingRules
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return nil, namingTemplateViolation("SECTION_VALUE_INVALID", field, "naming rules value must be typed naming rules")
	}
	value.Preset = nil
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	err = animedomain.ValidateSectionValueWithVars(animedomain.SectionNamingRules, encoded, vars)
	if err == nil {
		err = animedomain.ValidateNamingSectionForSave(encoded, vars)
	}
	if err != nil {
		var namingErr *naming.Error
		if errors.As(err, &namingErr) {
			return nil, namingTemplateViolation(namingErr.Code, field, namingErr.Message)
		}
		return nil, namingTemplateViolation("SECTION_VALUE_INVALID", field, err.Error())
	}
	return encoded, nil
}

func namingTemplateViolation(code, field, message string) error {
	return &apperrors.Validation{Code: code, Details: []apperrors.FieldViolation{{Field: stringPtr(field), Code: code, Message: message}}}
}
