package attrrules

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

const schemaVersion = 1
const parserProfileBase = "release-facts-v1"

type Rules struct {
	Version        int      `yaml:"version" json:"version"`
	Resolution     []string `yaml:"resolution" json:"resolution"`
	Quality        []string `yaml:"quality" json:"quality"`
	Language       []string `yaml:"language" json:"language"`
	SpecialMarkers []string `yaml:"special_markers" json:"special_markers"`
	GroupBrackets  []string `yaml:"group_brackets" json:"group_brackets"`
}

func (r Rules) Validate() error {
	if r.Version != schemaVersion {
		return fmt.Errorf("field version: unsupported schema version %d (supported: %d)", r.Version, schemaVersion)
	}
	fields := []struct {
		name   string
		values []string
	}{
		{name: "resolution", values: r.Resolution},
		{name: "quality", values: r.Quality},
		{name: "language", values: r.Language},
		{name: "special_markers", values: r.SpecialMarkers},
	}
	for _, field := range fields {
		if len(field.values) == 0 {
			return fmt.Errorf("field %s: must not be empty", field.name)
		}
		for i, value := range field.values {
			if strings.TrimSpace(value) == "" {
				return fmt.Errorf("field %s[%d]: must not be empty", field.name, i)
			}
			if len(value) > 128 {
				return fmt.Errorf("field %s[%d]: entry exceeds 128 characters", field.name, i)
			}
		}
	}
	if len(r.GroupBrackets) != 2 {
		return errors.New("field group_brackets: must contain exactly two non-empty delimiters")
	}
	for i, value := range r.GroupBrackets {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("field group_brackets[%d]: must not be empty", i)
		}
		if len(value) > 128 {
			return fmt.Errorf("field group_brackets[%d]: entry exceeds 128 characters", i)
		}
	}
	return nil
}
func Parse(data []byte, path string) (Rules, error) {
	var r Rules
	if err := yaml.Unmarshal(data, &r); err != nil {
		return Rules{}, fmt.Errorf("%s: YAML parse failed: %w", path, err)
	}
	if err := r.Validate(); err != nil {
		return Rules{}, fmt.Errorf("%s: %w", path, err)
	}
	return r, nil
}
func Canonical(r Rules) ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(r)
}
func Fingerprint(r Rules) (string, error) {
	b, err := Canonical(r)
	if err != nil {
		return "", err
	}
	d := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(d[:]), nil
}
func ProfileVersion(fp string) string { return parserProfileBase + "@" + fp }

func StartupErrorDetail(err error, configPath string) string {
	field := ""
	message := err.Error()
	if index := strings.Index(message, "field "); index >= 0 {
		field = message[index+len("field "):]
		if end := strings.IndexByte(field, ':'); end >= 0 {
			field = field[:end]
		}
		field = strings.TrimSpace(field)
	}
	if field != "" {
		return fmt.Sprintf("config path %q field %s", configPath, field)
	}
	return fmt.Sprintf("config path %q", configPath)
}

var (
	mu                sync.RWMutex
	active            Rules
	activeFingerprint string
	configured        bool
)

func SetActive(r Rules, fp string) error {
	if err := r.Validate(); err != nil {
		return err
	}
	if fp == "" {
		return errors.New("attr rules fingerprint is empty")
	}
	mu.Lock()
	active, activeFingerprint, configured = r, fp, true
	mu.Unlock()
	return nil
}
func Current() (Rules, string, error) {
	mu.RLock()
	defer mu.RUnlock()
	if !configured {
		return Rules{}, "", errors.New("attr rules have not been initialized")
	}
	return active, activeFingerprint, nil
}
func CurrentProfileVersion() (string, error) {
	_, fp, err := Current()
	if err != nil {
		return "", err
	}
	return ProfileVersion(fp), nil
}

type Persistence interface {
	Publish(context.Context, Rules, string) error
	Current(context.Context) (Rules, string, error)
}

// Store is the application-facing persistence facade. It owns validation and
// active in-memory state while the concrete persistence adapter owns SQL and
// transaction lifecycle.
type Store struct {
	persistence Persistence
}

// NewStore wires an application store to its persistence port.
func NewStore(persistence Persistence) Store {
	return Store{persistence: persistence}
}

func (s Store) unavailable() error {
	if s.persistence == nil {
		return errors.New("attr rules database is nil")
	}
	return nil
}

func (s Store) Publish(ctx context.Context, r Rules, fp string) error {
	if err := s.unavailable(); err != nil {
		return err
	}
	if err := r.Validate(); err != nil {
		return err
	}
	canonicalFingerprint, err := Fingerprint(r)
	if err != nil {
		return fmt.Errorf("fingerprint attr rules: %w", err)
	}
	if fp != canonicalFingerprint {
		return fmt.Errorf("attr rules fingerprint mismatch: supplied %q, canonical %q", fp, canonicalFingerprint)
	}
	if err := s.persistence.Publish(ctx, r, canonicalFingerprint); err != nil {
		return err
	}
	return SetActive(r, canonicalFingerprint)
}

func (s Store) Current(ctx context.Context) (Rules, string, error) {
	if err := s.unavailable(); err != nil {
		return Rules{}, "", err
	}
	r, fp, err := s.persistence.Current(ctx)
	if err != nil {
		return Rules{}, "", err
	}
	if err := r.Validate(); err != nil {
		return Rules{}, "", fmt.Errorf("validate active attr rules: %w", err)
	}
	canonicalFingerprint, err := Fingerprint(r)
	if err != nil {
		return Rules{}, "", fmt.Errorf("fingerprint active attr rules: %w", err)
	}
	if fp != canonicalFingerprint {
		return Rules{}, "", fmt.Errorf("active attr rules fingerprint mismatch: stored %q, canonical %q", fp, canonicalFingerprint)
	}
	if err := SetActive(r, fp); err != nil {
		return Rules{}, "", err
	}
	return r, fp, nil
}

func Initialize(ctx context.Context, store Store, configPath, defaultsPath string) (Rules, string, error) {
	if strings.TrimSpace(configPath) == "" || strings.TrimSpace(defaultsPath) == "" {
		return Rules{}, "", errors.New("attr rules config and defaults paths are required")
	}
	if _, err := os.Stat(configPath); errors.Is(err, os.ErrNotExist) {
		b, e := os.ReadFile(defaultsPath)
		if e != nil {
			return Rules{}, "", fmt.Errorf("copy attr rules defaults %s to %s: %w", defaultsPath, configPath, e)
		}
		if e = os.MkdirAll(filepath.Dir(configPath), 0755); e != nil {
			return Rules{}, "", fmt.Errorf("create attr rules config directory: %w", e)
		}
		if e = os.WriteFile(configPath, b, 0644); e != nil {
			return Rules{}, "", fmt.Errorf("copy attr rules defaults %s to %s: %w", defaultsPath, configPath, e)
		}
	} else if err != nil {
		return Rules{}, "", fmt.Errorf("stat attr rules file %s: %w", configPath, err)
	}
	b, err := os.ReadFile(configPath)
	if err != nil {
		return Rules{}, "", fmt.Errorf("read attr rules %s: %w", configPath, err)
	}
	r, err := Parse(b, configPath)
	if err != nil {
		return Rules{}, "", err
	}
	fp, err := Fingerprint(r)
	if err != nil {
		return Rules{}, "", fmt.Errorf("fingerprint attr rules %s: %w", configPath, err)
	}
	if err = store.Publish(ctx, r, fp); err != nil {
		return Rules{}, "", err
	}
	return r, fp, nil
}
