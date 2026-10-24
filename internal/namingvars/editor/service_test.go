package editor

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"bansoko/internal/apperrors"
	"bansoko/internal/namingvars"
)

// memory is an in-memory VersionedPersistence.
type memory struct{ versions []namingvars.Version }

func (m *memory) active() int {
	for i, v := range m.versions {
		if v.Active {
			return i
		}
	}
	return -1
}
func (m *memory) activeRevision() int64 {
	if i := m.active(); i >= 0 {
		return m.versions[i].Revision
	}
	return 0
}
func (m *memory) setActive(revision int64) namingvars.VersionInfo {
	var info namingvars.VersionInfo
	for i := range m.versions {
		m.versions[i].Active = m.versions[i].Revision == revision
		if m.versions[i].Active {
			info = m.versions[i].VersionInfo
		}
	}
	return info
}
func (m *memory) cas(expected int64) error {
	if m.activeRevision() == expected {
		return nil
	}
	conflict := &namingvars.ConflictError{}
	if i := m.active(); i >= 0 {
		conflict.Current = m.versions[i].VersionInfo
	}
	return conflict
}
func (m *memory) add(p namingvars.SaveParams) namingvars.VersionInfo {
	m.versions = append(m.versions, namingvars.Version{
		VersionInfo: namingvars.VersionInfo{Revision: int64(len(m.versions) + 1), Digest: p.Fingerprint, Origin: p.Origin, Note: p.Note, SourceBytes: len(p.Source), CreatedAt: time.Now().UTC()},
		Source:      p.Source,
	})
	return m.setActive(int64(len(m.versions)))
}
func (m *memory) Publish(context.Context, string, string) error { return errors.New("unused") }
func (m *memory) Current(context.Context) (string, string, error) {
	return "", "", errors.New("unused")
}
func (m *memory) ActiveVersion(context.Context) (namingvars.Version, error) {
	if i := m.active(); i >= 0 {
		return m.versions[i], nil
	}
	return namingvars.Version{}, namingvars.ErrVersionNotFound
}
func (m *memory) VersionByRevision(_ context.Context, revision int64) (namingvars.Version, error) {
	for _, v := range m.versions {
		if v.Revision == revision {
			return v, nil
		}
	}
	return namingvars.Version{}, namingvars.ErrVersionNotFound
}
func (m *memory) ListVersions(context.Context, int) ([]namingvars.VersionInfo, error) {
	out := []namingvars.VersionInfo{}
	for i := len(m.versions) - 1; i >= 0; i-- {
		out = append(out, m.versions[i].VersionInfo)
	}
	return out, nil
}
func (m *memory) SaveVersion(_ context.Context, p namingvars.SaveParams) (namingvars.VersionInfo, error) {
	if err := m.cas(p.ExpectedRevision); err != nil {
		return namingvars.VersionInfo{}, err
	}
	for _, v := range m.versions {
		if v.Digest == p.Fingerprint {
			return m.setActive(v.Revision), nil
		}
	}
	return m.add(p), nil
}
func (m *memory) ActivateRevision(_ context.Context, expected, target int64) (namingvars.VersionInfo, error) {
	if err := m.cas(expected); err != nil {
		return namingvars.VersionInfo{}, err
	}
	for _, v := range m.versions {
		if v.Revision == target {
			return m.setActive(target), nil
		}
	}
	return namingvars.VersionInfo{}, namingvars.ErrVersionNotFound
}
func (m *memory) Seed(_ context.Context, p namingvars.SaveParams) (bool, error) {
	if m.active() >= 0 {
		return false, nil
	}
	m.add(p)
	return true, nil
}

const seedSource = "vars.anime = {ep: extract(input.release_title, /(?:^|\\s)(\\d{1,4})(?:\\D|$)/) ?? \"\"};\n"

func newEditor(t *testing.T, releases ReleaseEvidenceLoader) (*Service, *memory) {
	t.Helper()
	store := &memory{}
	fp, err := namingvars.SourceFingerprint(seedSource)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Seed(context.Background(), namingvars.SaveParams{Source: seedSource, Fingerprint: fp, Origin: namingvars.OriginFile}); err != nil {
		t.Fatal(err)
	}
	return NewServiceWith(namingvars.NewStore(store), releases), store
}

func TestDryRunMatchesTheFrozenResultOfTheSameEvidence(t *testing.T) {
	service, _ := newEditor(t, nil)
	source := "vars.show = {title: input.anime.title ?? \"\", ep: input.ep.number ?? \"\", group: input.rt.subgroup ?? \"\", n: `${input.torrent.file_count}`};\n"
	result, err := service.DryRun(context.Background(), source, []DryRunTarget{
		{Kind: TargetSample, Sample: namingvars.SampleTV},
		{Kind: TargetSample, Sample: namingvars.SampleBare},
		{Kind: TargetCustom, Title: "[G] Typed - 07", Files: []CustomFile{{Path: "a/Typed - 07.mkv", Size: 9}}},
	})
	if err != nil || len(result.Diagnostics) != 0 || len(result.Targets) != 3 {
		t.Fatalf("dry run = %+v / %v", result, err)
	}
	// Equality with what acceptance freezes: same builder, same program.
	fp, _ := namingvars.SourceFingerprint(source)
	program, err := namingvars.NewProgram(namingvars.ProgramRef{Version: namingvars.ProfileVersion(fp), Digest: fp}, source)
	if err != nil {
		t.Fatal(err)
	}
	for i, ref := range []string{namingvars.SampleTV, namingvars.SampleBare} {
		sample, _ := namingvars.SampleByRef(ref)
		frozen, err := program.Freeze(namingvars.BuildInput(sample.Evidence))
		if err != nil {
			t.Fatal(err)
		}
		got := result.Targets[i]
		if got.Status != StatusOK || got.Output["show"]["ep"] != frozen.Output["show"]["ep"] || got.Output["show"]["title"] != frozen.Output["show"]["title"] || got.Output["show"]["n"] != frozen.Output["show"]["n"] {
			t.Fatalf("%s: dry run %v differs from frozen %v", ref, got.Output, frozen.Output)
		}
	}
	// Missing values are missing, not guessed: the bare release has no episode.
	if bare := result.Targets[1].Output["show"]; bare["ep"] != "" || bare["title"] != "" || bare["group"] != "" {
		t.Fatalf("title-only release output = %v, want the explicit fallbacks only", bare)
	}
	if full := result.Targets[0].Output["show"]; full["ep"] != "1" || full["group"] == "" {
		t.Fatalf("full sample output = %v", full)
	}
	if custom := result.Targets[2]; custom.Ref != "custom" || custom.FileCount != 1 || custom.Output["show"]["n"] != "1" {
		t.Fatalf("typed-in release = %+v", custom)
	}
}

func TestDryRunReportsEvaluationFailuresWithSpansAndStaticOnesWithoutTargets(t *testing.T) {
	service, _ := newEditor(t, nil)
	// No fallback: the bare sample has no anime title.
	source := "vars.a = {b: 'ok'};\nvars.show = {title: input.anime.title};\n"
	result, err := service.DryRun(context.Background(), source, []DryRunTarget{{Kind: TargetSample, Sample: namingvars.SampleBare}, {Kind: TargetSample, Sample: namingvars.SampleTV}})
	if err != nil {
		t.Fatal(err)
	}
	bare, full := result.Targets[0], result.Targets[1]
	if bare.Status != StatusError || bare.Diagnostic == nil || bare.Diagnostic.Code != namingvars.DiagOutputMissing || !strings.HasPrefix(source[bare.Diagnostic.Start:], "title") {
		t.Fatalf("bare = %+v", bare)
	}
	if full.Status != StatusOK {
		t.Fatalf("full = %+v", full)
	}

	static, err := service.DryRun(context.Background(), "vars.a = {b: eval('1')};", []DryRunTarget{{Kind: TargetSample, Sample: namingvars.SampleTV}})
	if err != nil || len(static.Diagnostics) == 0 || len(static.Targets) != 0 || static.Digest != "" {
		t.Fatalf("static failure = %+v / %v", static, err)
	}
}

func TestDryRunRejectsMalformedRequestsAndReportsUnavailableReleases(t *testing.T) {
	service, store := newEditor(t, stubReleases{})
	for name, target := range map[string]DryRunTarget{
		"unknown sample": {Kind: TargetSample, Sample: "nope"},
		"blank title":    {Kind: TargetCustom, Title: "  "},
		"negative size":  {Kind: TargetCustom, Title: "T", Files: []CustomFile{{Path: "a", Size: -1}}},
		"unknown kind":   {Kind: "other"},
		"no release id":  {Kind: TargetRelease},
	} {
		_, err := service.DryRun(context.Background(), seedSource, []DryRunTarget{target})
		var validation *apperrors.Validation
		if !errors.As(err, &validation) {
			t.Fatalf("%s: err = %v, want typed validation", name, err)
		}
	}
	if _, err := service.DryRun(context.Background(), seedSource, make([]DryRunTarget, MaxTargets+1)); err == nil {
		t.Fatal("too many targets must be refused")
	}
	result, err := service.DryRun(context.Background(), seedSource, []DryRunTarget{{Kind: TargetRelease, ReleaseID: "gone"}})
	if err != nil || result.Targets[0].Status != StatusUnavailable || result.Targets[0].Reason != ReasonReleaseNotFound {
		t.Fatalf("missing release = %+v / %v", result, err)
	}
	// A dry run never writes.
	if len(store.versions) != 1 {
		t.Fatalf("a dry run created versions: %d", len(store.versions))
	}
}

type stubReleases struct{}

func (stubReleases) LoadReleaseEvidence(context.Context, string) (namingvars.ReleaseEvidence, string, error) {
	return namingvars.ReleaseEvidence{}, "", &UnavailableError{Reason: ReasonReleaseNotFound}
}
func (stubReleases) ListReleases(context.Context, int) ([]ReleaseOption, error) { return nil, nil }

func TestSaveActivateAndErrorsAreTyped(t *testing.T) {
	service, store := newEditor(t, nil)
	saved, err := service.Save(context.Background(), "vars.series = {name: \"two\"};", "two", 1)
	if err != nil || saved.Revision != 2 || !saved.Active {
		t.Fatalf("save = %+v / %v", saved, err)
	}
	_, err = service.Save(context.Background(), "vars.series = {name: \"three\"};", "", 1)
	var conflict *apperrors.VersionConflict
	if !errors.As(err, &conflict) || conflict.CurrentVersion != 2 {
		t.Fatalf("stale save = %v, want a version conflict at 2", err)
	}
	_, err = service.Save(context.Background(), "vars.a = {b: };", "", 2)
	var validation *apperrors.Validation
	if !errors.As(err, &validation) || validation.Code != "NAMING_VARS_INVALID" || validation.Details[0].Start == nil || validation.Details[0].End == nil {
		t.Fatalf("invalid save = %v, want located details", err)
	}
	rolled, err := service.Activate(context.Background(), 1, 2)
	if err != nil || rolled.Revision != 1 || store.activeRevision() != 1 {
		t.Fatalf("rollback = %+v / %v", rolled, err)
	}
	_, err = service.Activate(context.Background(), 99, 1)
	var missing *apperrors.NotFound
	if !errors.As(err, &missing) {
		t.Fatalf("unknown revision = %v", err)
	}
	state, err := service.GetState(context.Background())
	if err != nil || state.Active.Revision != 1 || len(state.Versions) != 2 || state.Versions[0].Revision != 2 {
		t.Fatalf("state = %+v / %v", state, err)
	}
	if len(state.Facts) == 0 || len(state.Samples) != 3 || state.Limits.MaxSourceBytes != namingvars.MaxSourceBytes {
		t.Fatalf("reference data = %d facts, %d samples, %+v", len(state.Facts), len(state.Samples), state.Limits)
	}
}

func TestImportExportRoundTripAndRefusals(t *testing.T) {
	service, store := newEditor(t, nil)
	document, err := service.Export(context.Background())
	if err != nil || document.Format != DocumentFormat || document.FormatVersion != 1 || document.Source != seedSource || document.Revision != 1 {
		t.Fatalf("export = %+v / %v", document, err)
	}

	other := "vars.series = {name: \"imported\"};"
	otherFP, _ := namingvars.SourceFingerprint(other)
	good := document
	good.Source, good.Digest, good.Note = other, otherFP, "from elsewhere"
	imported, err := service.Import(context.Background(), good, 1)
	if err != nil || imported.Revision != 2 || imported.Origin != namingvars.OriginImport || imported.Note != "from elsewhere" {
		t.Fatalf("import = %+v / %v", imported, err)
	}

	before := len(store.versions)
	active := store.activeRevision()
	for name, mutate := range map[string]func(*Document){
		"format":         func(d *Document) { d.Format = "other" },
		"format version": func(d *Document) { d.FormatVersion = 2 },
		"runtime":        func(d *Document) { d.Runtime = "naming-vars-v2" },
		"digest":         func(d *Document) { d.Digest = "sha256:wrong" },
		"invalid source": func(d *Document) {
			d.Source = "vars.a = {b: };"
			d.Digest, _ = namingvars.SourceFingerprint(d.Source)
		},
	} {
		bad := good
		mutate(&bad)
		if _, err := service.Import(context.Background(), bad, 2); err == nil {
			t.Fatalf("%s: import accepted", name)
		}
	}
	if _, err := service.Import(context.Background(), good, 1); err == nil {
		t.Fatal("a stale import must conflict")
	}
	if len(store.versions) != before || store.activeRevision() != active {
		t.Fatalf("refused imports changed state: %d versions, active %d", len(store.versions), store.activeRevision())
	}
	var unsupported *apperrors.Validation
	bad := good
	bad.FormatVersion = 9
	if _, err := service.Import(context.Background(), bad, 2); !errors.As(err, &unsupported) || unsupported.Code != "NAMING_VARS_IMPORT_UNSUPPORTED" {
		t.Fatalf("unsupported version = %v", err)
	}
}

func TestExportCarriesNothingButTheProgram(t *testing.T) {
	service, _ := newEditor(t, nil)
	document, err := service.Export(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// The document type has exactly the program text and its identity.
	if document.Source == "" || strings.Contains(strings.ToLower(document.Note), "secret") {
		t.Fatalf("document = %+v", document)
	}
}
