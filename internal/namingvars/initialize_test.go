package namingvars

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakePersistence stands in for the PostgreSQL adapter. Initialize and Store own
// validation, evaluation and activation; the adapter only stores immutable
// source versions and the active pointer. The fake lets those responsibilities
// be asserted without a database.
type fakePersistence struct {
	publishedSources []string
	publishedFP      []string
	versions         []Version
	currentErr       error
	publishErr       error
	available        bool
}

func newFakePersistence() *fakePersistence {
	return &fakePersistence{available: true}
}

func (f *fakePersistence) active() (int, bool) {
	for i, v := range f.versions {
		if v.Active {
			return i, true
		}
	}
	return 0, false
}

func (f *fakePersistence) activeRevision() int64 {
	if i, ok := f.active(); ok {
		return f.versions[i].Revision
	}
	return 0
}

func (f *fakePersistence) setActive(revision int64) VersionInfo {
	var info VersionInfo
	for i := range f.versions {
		f.versions[i].Active = f.versions[i].Revision == revision
		if f.versions[i].Active {
			now := time.Now().UTC()
			f.versions[i].ActivatedAt = &now
			info = f.versions[i].VersionInfo
		}
	}
	return info
}

func (f *fakePersistence) add(source, fingerprint, origin, note string) VersionInfo {
	f.versions = append(f.versions, Version{
		VersionInfo: VersionInfo{Revision: int64(len(f.versions) + 1), Digest: fingerprint, Origin: origin, Note: note, SourceBytes: len(source), CreatedAt: time.Now().UTC()},
		Source:      source,
	})
	return f.setActive(int64(len(f.versions)))
}

func (f *fakePersistence) Publish(_ context.Context, source, fingerprint string) error {
	if !f.available {
		return errors.New("naming vars database is nil")
	}
	if f.publishErr != nil {
		return f.publishErr
	}
	f.publishedSources = append(f.publishedSources, source)
	f.publishedFP = append(f.publishedFP, fingerprint)
	for _, v := range f.versions {
		if v.Digest == fingerprint {
			f.setActive(v.Revision)
			return nil
		}
	}
	f.add(source, fingerprint, OriginFile, "")
	return nil
}

func (f *fakePersistence) Current(context.Context) (string, string, error) {
	if !f.available {
		return "", "", errors.New("naming vars database is nil")
	}
	if f.currentErr != nil {
		return "", "", f.currentErr
	}
	if i, ok := f.active(); ok {
		return f.versions[i].Source, f.versions[i].Digest, nil
	}
	return "", "", nil
}

func (f *fakePersistence) ActiveVersion(context.Context) (Version, error) {
	if i, ok := f.active(); ok {
		return f.versions[i], nil
	}
	return Version{}, ErrVersionNotFound
}

func (f *fakePersistence) VersionByRevision(_ context.Context, revision int64) (Version, error) {
	for _, v := range f.versions {
		if v.Revision == revision {
			return v, nil
		}
	}
	return Version{}, ErrVersionNotFound
}

func (f *fakePersistence) ListVersions(context.Context, int) ([]VersionInfo, error) {
	out := []VersionInfo{}
	for i := len(f.versions) - 1; i >= 0; i-- {
		out = append(out, f.versions[i].VersionInfo)
	}
	return out, nil
}

func (f *fakePersistence) conflict(expected int64) error {
	if f.activeRevision() == expected {
		return nil
	}
	conflict := &ConflictError{}
	if i, ok := f.active(); ok {
		conflict.Current = f.versions[i].VersionInfo
	}
	return conflict
}

func (f *fakePersistence) SaveVersion(_ context.Context, p SaveParams) (VersionInfo, error) {
	if err := f.conflict(p.ExpectedRevision); err != nil {
		return VersionInfo{}, err
	}
	f.publishedSources = append(f.publishedSources, p.Source)
	f.publishedFP = append(f.publishedFP, p.Fingerprint)
	for _, v := range f.versions {
		if v.Digest == p.Fingerprint {
			return f.setActive(v.Revision), nil
		}
	}
	return f.add(p.Source, p.Fingerprint, p.Origin, p.Note), nil
}

func (f *fakePersistence) ActivateRevision(_ context.Context, expected, target int64) (VersionInfo, error) {
	if err := f.conflict(expected); err != nil {
		return VersionInfo{}, err
	}
	for _, v := range f.versions {
		if v.Revision == target {
			return f.setActive(target), nil
		}
	}
	return VersionInfo{}, ErrVersionNotFound
}

func (f *fakePersistence) Seed(_ context.Context, p SaveParams) (bool, error) {
	if _, ok := f.active(); ok {
		return false, nil
	}
	f.publishedSources = append(f.publishedSources, p.Source)
	f.publishedFP = append(f.publishedFP, p.Fingerprint)
	f.add(p.Source, p.Fingerprint, p.Origin, p.Note)
	return true, nil
}

const validVarsSource = "vars.anime = {ep: extract(input.release_title, /(?:^|\\s)(\\d{1,4})(?:\\D|$)/)};\n"

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestInitializeSeedsFromDefaultsWhenConfigIsMissing(t *testing.T) {
	dir := t.TempDir()
	defaults := filepath.Join(dir, "defaults", "naming-vars.js")
	writeFile(t, defaults, validVarsSource)
	config := filepath.Join(dir, "config", "nested", "naming-vars.js")
	persistence := newFakePersistence()

	result, err := InitializeDetailed(context.Background(), NewStore(persistence), config, defaults)
	if err != nil {
		t.Fatal(err)
	}
	// The file is a seed source only: initialization never writes it.
	if _, err := os.Stat(config); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("config must not be created from defaults: %v", err)
	}
	if !result.Seeded || result.FileDiverged {
		t.Fatalf("result = %+v, want a first-boot seed", result)
	}
	if result.Vars["anime"]["ep"] != "01" {
		t.Fatalf("initialized vars = %v, want the defaults program output", result.Vars)
	}
	wantFP, err := SourceFingerprint(validVarsSource)
	if err != nil {
		t.Fatal(err)
	}
	if result.Fingerprint != wantFP {
		t.Fatalf("fingerprint = %q, want %q", result.Fingerprint, wantFP)
	}
	if len(persistence.publishedSources) != 1 || persistence.publishedFP[0] != wantFP {
		t.Fatalf("published = %v / %v, want exactly one seed with the canonical fingerprint", persistence.publishedSources, persistence.publishedFP)
	}
	if persistence.versions[0].Origin != OriginFile {
		t.Fatalf("seed origin = %q, want %q", persistence.versions[0].Origin, OriginFile)
	}
	// Activation is part of initialization: a caller that forgot it would render
	// with an empty Vars object.
	active, activeFP, err := Current()
	if err != nil || activeFP != wantFP || active["anime"]["ep"] != "01" {
		t.Fatalf("active vars after Initialize = %v / %q / %v", active, activeFP, err)
	}
}

func TestInitializeSeedsFromTheOperatorFileWithoutRewritingIt(t *testing.T) {
	dir := t.TempDir()
	defaults := filepath.Join(dir, "naming-vars.js.defaults")
	writeFile(t, defaults, validVarsSource)
	custom := "vars.series = {name: \"mine\"};\n"
	config := filepath.Join(dir, "naming-vars.js")
	writeFile(t, config, custom)
	persistence := newFakePersistence()

	vars, _, err := Initialize(context.Background(), NewStore(persistence), config, defaults)
	if err != nil {
		t.Fatal(err)
	}

	kept, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	if string(kept) != custom {
		t.Fatalf("config was rewritten to %q, want the operator's own file untouched", kept)
	}
	if len(vars) != 1 || vars["series"]["name"] != "mine" {
		t.Fatalf("initialized vars = %v, want the operator's program", vars)
	}
	if len(persistence.publishedSources) != 1 || persistence.publishedSources[0] != custom {
		t.Fatalf("published %v, want the operator's source", persistence.publishedSources)
	}
}

func TestInitializeKeepsTheStoredProgramWhenTheFileDiffers(t *testing.T) {
	dir := t.TempDir()
	defaults := filepath.Join(dir, "defaults.js")
	writeFile(t, defaults, validVarsSource)
	config := filepath.Join(dir, "naming-vars.js")
	writeFile(t, config, validVarsSource)
	persistence := newFakePersistence()
	store := NewStore(persistence)
	if _, err := InitializeDetailed(context.Background(), store, config, defaults); err != nil {
		t.Fatal(err)
	}
	stored, err := SourceFingerprint(validVarsSource)
	if err != nil {
		t.Fatal(err)
	}

	// The operator edits the file and restarts: the stored version stays.
	edited := "vars.series = {name: \"edited\"};\n"
	writeFile(t, config, edited)
	published := len(persistence.publishedSources)
	result, err := InitializeDetailed(context.Background(), store, config, defaults)
	if err != nil {
		t.Fatal(err)
	}
	if result.Seeded || !result.FileDiverged {
		t.Fatalf("result = %+v, want a diverged file that was not seeded", result)
	}
	editedFP, _ := SourceFingerprint(edited)
	if result.FileDigest != editedFP || result.Fingerprint != stored {
		t.Fatalf("digests = file %q active %q, want file %q active %q", result.FileDigest, result.Fingerprint, editedFP, stored)
	}
	if len(persistence.publishedSources) != published || len(persistence.versions) != 1 {
		t.Fatalf("a restart wrote %d versions, want the stored one untouched", len(persistence.versions))
	}
	if result.Vars["anime"]["ep"] != "01" {
		t.Fatalf("active vars = %v, want the stored program", result.Vars)
	}
	// An identical file is not a divergence.
	writeFile(t, config, validVarsSource)
	result, err = InitializeDetailed(context.Background(), store, config, defaults)
	if err != nil || result.FileDiverged {
		t.Fatalf("identical file reported diverged: %+v %v", result, err)
	}
}

func TestInitializeRejectsNonRegularAndOversizedSources(t *testing.T) {
	dir := t.TempDir()
	defaults := filepath.Join(dir, "defaults.js")
	writeFile(t, defaults, validVarsSource)
	persistence := newFakePersistence()

	// A directory at the config path is not a source file: reading it must fail
	// rather than silently produce an empty program.
	configDir := filepath.Join(dir, "as-directory")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Initialize(context.Background(), NewStore(persistence), configDir, defaults); err == nil {
		t.Fatal("a directory at the config path must be rejected")
	}

	oversized := filepath.Join(dir, "oversized.js")
	writeFile(t, oversized, strings.Repeat("x", MaxSourceBytes+1))
	if _, _, err := Initialize(context.Background(), NewStore(persistence), oversized, defaults); err == nil {
		t.Fatal("an oversized config must be rejected")
	}
	if len(persistence.publishedSources) != 0 {
		t.Fatalf("a rejected source must never be published: %v", persistence.publishedSources)
	}
}

func TestInitializeReportsTheConfigPathForABrokenScript(t *testing.T) {
	dir := t.TempDir()
	defaults := filepath.Join(dir, "defaults.js")
	writeFile(t, defaults, validVarsSource)
	config := filepath.Join(dir, "broken.js")
	writeFile(t, config, "vars.anime = {ep: };\n")

	_, _, err := Initialize(context.Background(), NewStore(newFakePersistence()), config, defaults)
	if err == nil {
		t.Fatal("a broken program must fail initialization")
	}
	if !strings.Contains(err.Error(), config) {
		t.Fatalf("error %q must name the file the operator has to fix", err)
	}
}

func TestInitializeRequiresBothPaths(t *testing.T) {
	dir := t.TempDir()
	config := filepath.Join(dir, "naming-vars.js")
	writeFile(t, config, validVarsSource)
	for _, tc := range []struct{ config, defaults string }{
		{config: config, defaults: "  "},
		{config: "", defaults: config},
	} {
		if _, _, err := Initialize(context.Background(), NewStore(newFakePersistence()), tc.config, tc.defaults); err == nil {
			t.Fatalf("paths %q/%q must be rejected", tc.config, tc.defaults)
		}
	}
}

func TestStorePublishSourceRejectsFingerprintMismatch(t *testing.T) {
	persistence := newFakePersistence()
	store := NewStore(persistence)
	vars, err := Run(validVarsSource, DefaultInput, DefaultTimeout)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := SourceFingerprint(validVarsSource)
	if err != nil {
		t.Fatal(err)
	}

	if err := store.PublishSource(context.Background(), validVarsSource, vars, "sha256:deadbeef"); err == nil {
		t.Fatal("a mismatched fingerprint must be rejected: it would freeze the wrong identity")
	}
	if len(persistence.publishedSources) != 0 {
		t.Fatal("a rejected publish must not reach persistence")
	}
	if err := store.PublishSource(context.Background(), validVarsSource, vars, canonical); err != nil {
		t.Fatalf("canonical publish rejected: %v", err)
	}
	if len(persistence.publishedSources) != 1 {
		t.Fatalf("published %d times, want 1", len(persistence.publishedSources))
	}
}

func TestStoreCurrentRejectsStoredFingerprintDrift(t *testing.T) {
	persistence := newFakePersistence()
	persistence.versions = []Version{{VersionInfo: VersionInfo{Revision: 1, Digest: "sha256:not-the-canonical-one", Active: true}, Source: validVarsSource}}
	store := NewStore(persistence)

	if _, _, err := store.Current(context.Background()); err == nil {
		t.Fatal("a stored fingerprint that does not match the stored source must fail closed")
	}
	canonical, err := SourceFingerprint(validVarsSource)
	if err != nil {
		t.Fatal(err)
	}
	persistence.versions[0].Digest = canonical
	vars, fp, err := store.Current(context.Background())
	if err != nil || fp != canonical || vars["anime"]["ep"] != "01" {
		t.Fatalf("aligned read = %v / %q / %v", vars, fp, err)
	}
}

func TestStoreFailsClosedWithoutPersistence(t *testing.T) {
	store := NewStore(nil)
	if err := store.PublishSource(context.Background(), validVarsSource, Vars{"a": {"b": "c"}}, "sha256:x"); err == nil {
		t.Fatal("publishing without persistence must fail closed")
	}
	if _, _, err := store.Current(context.Background()); err == nil {
		t.Fatal("reading without persistence must fail closed")
	}
}

func TestStoreCurrentRejectsNonSourceBackedContent(t *testing.T) {
	persistence := newFakePersistence()
	persistence.versions = []Version{{VersionInfo: VersionInfo{Revision: 1, Digest: "sha256:x", Active: true}, Source: ""}}
	store := NewStore(persistence)
	if _, _, err := store.Current(context.Background()); err == nil {
		t.Fatal("empty stored source must not be accepted as an active version")
	}
}
