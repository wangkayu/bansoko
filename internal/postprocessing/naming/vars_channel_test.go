package naming

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"bansoko/internal/namingvars"
)

// uninitializedChildEnv marks the child run of
// TestWithCurrentVarsFailsClosedBeforeInitialization. The Vars snapshot is
// process-wide, so the only order-independent way to observe the uninitialized
// state is a process in which nothing initialized it.
const uninitializedChildEnv = "BANSOKO_NAMING_VARS_UNINITIALIZED_CHILD"

func TestWithCurrentVarsFailsClosedBeforeInitialization(t *testing.T) {
	if os.Getenv(uninitializedChildEnv) != "1" {
		// -test.run selects this test only, so the child never activates a snapshot.
		child := exec.Command(os.Args[0], "-test.run=^TestWithCurrentVarsFailsClosedBeforeInitialization$")
		child.Env = append(os.Environ(), uninitializedChildEnv+"=1")
		if output, err := child.CombinedOutput(); err != nil {
			t.Fatalf("uninitialized child run failed: %v\n%s", err, output)
		}
		return
	}
	ctx, err := WithCurrentVars(sampleContext(t, "sample-tv"))
	if err == nil {
		t.Fatal("injecting the active vars before initialization must fail closed, not silently inject nothing")
	}
	if !strings.Contains(err.Error(), "not been initialized") {
		t.Fatalf("error %q must name the uninitialized channel", err)
	}
	if ctx.Vars != nil {
		t.Fatalf("a failed injection must return no Vars, got %v", ctx.Vars)
	}
}

func activateVars(t *testing.T, vars namingvars.Vars) {
	t.Helper()
	if err := namingvars.SetActive(vars, "sha256:test-"+strings.Join(keysOf(vars), ",")); err != nil {
		t.Fatal(err)
	}
}

func keysOf(vars namingvars.Vars) []string {
	names := make([]string, 0, len(vars))
	for name := range vars {
		names = append(names, name)
	}
	return names
}

// compileWithActiveVars compiles a template the way a configuration save does:
// the engine holds no global snapshot, so the caller that owns the lifecycle
// injects the loaded Vars explicitly.
func compileWithActiveVars(t *testing.T, engine *Engine, kind TemplateKind, source string) error {
	t.Helper()
	active, _, err := namingvars.Current()
	if err != nil {
		t.Fatalf("read the active naming vars snapshot: %v", err)
	}
	_, err = engine.CompileWithVars(kind, source, active)
	return err
}

func TestDeclaredVarsRenderThroughTheTemplateChannel(t *testing.T) {
	snapshot := namingvars.Vars{"anime": {"ep": "07", "group": "S01"}}
	activateVars(t, snapshot)
	engine := NewEngine()
	compiled, err := engine.CompileWithVars(KindDirectory, "/probe/${sanitize(anime.title)}/${vars.anime.group}", snapshot)
	if err != nil {
		t.Fatal(err)
	}
	ctx, err := WithVars(sampleContext(t, "sample-tv"), namingvars.Vars{"anime": {"ep": "07", "group": "S01"}})
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := engine.Render(compiled, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(rendered, "/probe/") || !strings.HasSuffix(rendered, "/S01") {
		t.Fatalf("rendered = %q, want the declared group value appended", rendered)
	}
}

// A template that reads a naming variable the declaration does not define cannot
// depend on the values current at save time, so it must be rejected when it is
// saved rather than when the first artifact is produced.
func TestTemplatesReadingUndeclaredVarsFailAtCompileTime(t *testing.T) {
	activateVars(t, namingvars.Vars{"anime": {"ep": "07"}})
	engine := NewEngine()
	for name, source := range map[string]string{
		"undeclared key":   "${vars.anime.season}",
		"undeclared group": "${vars.series.name}",
	} {
		t.Run(name, func(t *testing.T) {
			if err := compileWithActiveVars(t, engine, KindDirectory, source); err == nil {
				t.Fatal("an undeclared naming variable must fail template compilation")
			} else if !assertNamingVarsUnknown(err) {
				t.Fatalf("error = %v, want NAMING_VARS_UNKNOWN", err)
			}
		})
	}
}

func TestNamingVarsASTValidationIgnoresTextAndChecksDeadBranches(t *testing.T) {
	activateVars(t, namingvars.Vars{"loaded": {"key": "ok"}})
	engine := NewEngine()
	for _, source := range []string{"${'vars.custom.label'}", "${`vars.custom.label`}", "${vars.loaded.key /* vars.missing.key */}"} {
		if err := compileWithActiveVars(t, engine, KindDirectory, source); err != nil {
			t.Fatalf("literal/comment text was treated as a Vars reference (%s): %v", source, err)
		}
	}
	for _, source := range []string{"${false ? vars.missing.key : 'ok'}", "${vars.missing.key}", "${vars['loaded']['missing']}", "${vars[group].key}"} {
		err := compileWithActiveVars(t, engine, KindDirectory, source)
		if !assertNamingVarsUnknown(err) {
			t.Errorf("%s error = %v, want NAMING_VARS_UNKNOWN", source, err)
		}
	}
}

func assertNamingVarsUnknown(err error) bool {
	var namingErr *Error
	return errors.As(err, &namingErr) && namingErr.Code == ErrCodeNamingVarsUnknown
}

func TestCatalogIsFullLeafWhitelistForNewTemplates(t *testing.T) {
	engine := NewEngine()
	for _, source := range []string{
		"${tmdb.nope}",
		"${ep.nope}",
		"${bgm.name}",
		"${release.published_at}",
		"${media.nope}",
	} {
		_, err := engine.Compile(KindDirectory, source)
		var namingErr *Error
		if !errors.As(err, &namingErr) || namingErr.Code != ErrCodeTemplateCompileError || !strings.Contains(err.Error(), "unknown context field") {
			t.Errorf("legacy/unadvertised field %s error = %v, want explicit compile rejection", source, err)
		}
	}
	// Naming cannot read output facts at all: out is not a naming root.
	if _, err := engine.Compile(KindDirectory, "${out.path}"); err == nil || !strings.Contains(err.Error(), "unknown template identifier out") {
		t.Errorf("out.path error = %v, want unknown identifier", err)
	}
	for _, source := range []string{
		"${tmdb.season}",
		"${ep.display}",
		"${anime['title']}",
		"${'Anime.BGM.Title'}",
		"${`literal ${'Anime.BGM.Title'}`}",
	} {
		if _, err := engine.Compile(KindDirectory, source); err != nil {
			t.Errorf("valid or literal path %s rejected: %v", source, err)
		}
	}
}

// The trial render at compile time must report what the runtime would report: an
// undeclared identifier is a reference error naming the identifier, not a generic
// rejection of the whole template.
func TestUnknownIdentifierIsReportedByName(t *testing.T) {
	engine := NewEngine()
	_, err := engine.Compile(KindDirectory, "${undefinedVar}")
	if err == nil {
		t.Fatal("an undeclared identifier must fail compilation")
	}
	if !strings.Contains(err.Error(), "undefinedVar") {
		t.Fatalf("error %q must name the undeclared identifier", err)
	}
	if strings.Contains(err.Error(), "definitions are not allowed") {
		t.Fatalf("error %q must not claim template definitions are banned", err)
	}
}

// A text scan cannot enumerate unknown context fields, so a computed access used to
// bypass the whitelist and render the literal string "undefined" into a path. The
// index is not a static name, so the refusal names the reason rather than a field.
func TestComputedContextAccessIsRejected(t *testing.T) {
	engine := NewEngine()
	_, err := engine.Compile(KindDirectory, "${anime['Bo'+'gus']}")
	if err == nil {
		t.Fatal("a computed access to an unknown context field must fail compilation")
	}
	var namingErr *Error
	if !errors.As(err, &namingErr) || namingErr.Code != ErrCodeTemplateCompileError || !strings.Contains(err.Error(), "dynamic context field access") {
		t.Fatalf("error = %v, want the typed dynamic-access refusal", err)
	}
}

// Words that merely look like forbidden constructs are not forbidden: the old
// substring scan rejected legitimate text and unknown helpers alike.
func TestOrdinaryTemplateTextIsNotRejected(t *testing.T) {
	engine := NewEngine()
	for name, source := range map[string]string{
		"literal word template": "template-${anime.id}",
		"literal word define":   "define-${anime.id}",
		"unknown helper":        "${frobnicate(anime.title)}",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := engine.Compile(KindDirectory, source)
			switch name {
			case "unknown helper":
				if err == nil {
					t.Fatal("an unknown helper must fail compilation")
				}
			default:
				if err != nil {
					t.Fatalf("ordinary text must compile: %v", err)
				}
			}
		})
	}
}

// Injected vars are a per-render input: a caller that mutates its own map after
// injection must not be able to change what a render sees, at either level.
func TestWithVarsDefensivelyCopiesNestedMaps(t *testing.T) {
	input := namingvars.Vars{"anime": {"ep": "07"}}
	ctx, err := WithVars(sampleContext(t, "sample-tv"), input)
	if err != nil {
		t.Fatal(err)
	}
	input["anime"]["ep"] = "99"
	input["added"] = map[string]string{"ep": "99"}
	delete(input, "anime")
	if got := ctx.Vars["anime"]["ep"]; got != "07" {
		t.Fatalf("inner map is shared with the caller: ep = %q", got)
	}
	if _, ok := ctx.Vars["added"]; ok || len(ctx.Vars) != 1 {
		t.Fatalf("outer map is shared with the caller: %v", ctx.Vars)
	}
}

// Injection is the only place Vars enter a context, so it owns validation: a nil
// collection, a reserved group, and an invalid name must all be rejected rather
// than reach a template.
func TestWithVarsRejectsInvalidCollections(t *testing.T) {
	cases := map[string]namingvars.Vars{
		"nil":            nil,
		"reserved group": {"input": {"title": "forged"}},
		"invalid group":  {"Anime": {"title": "forged"}},
		"invalid key":    {"anime": {"Title": "forged"}},
	}
	for name, vars := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, err := WithVars(sampleContext(t, "sample-tv"), vars)
			if err == nil {
				t.Fatalf("invalid vars were injected: %v", ctx.Vars)
			}
			if !strings.Contains(err.Error(), "inject naming vars") {
				t.Fatalf("error %q must name the injection boundary", err)
			}
		})
	}
}

// Vars are presentation strings. A forged presentation value must not reach any
// resolver-owned identity field: the same frozen facts resolve identically with
// and without an injected Vars collection.
func TestVarsCannotChangeResolverOwnedIdentity(t *testing.T) {
	registry := DefaultRegistry()
	facts := sampleContext(t, "sample-tv")
	plain := registry.ResolveAll(ResolverInput{Facts: facts}).Context

	forged, err := WithVars(facts, namingvars.Vars{
		"anime":   {"title": "Forged", "id": "forged-id"},
		"parsed":  {"episode": "99", "display": "99"},
		"derived": {"effectivetitle": "Forged"},
	})
	if err != nil {
		t.Fatal(err)
	}
	withForged := registry.ResolveAll(ResolverInput{Facts: forged}).Context

	if withForged.Parsed.Episode != plain.Parsed.Episode {
		t.Fatalf("resolved episode changed with injected vars: %+v vs %+v", withForged.Parsed.Episode, plain.Parsed.Episode)
	}
	if withForged.Anime.Title != plain.Anime.Title || withForged.Derived.EffectiveTitle != plain.Derived.EffectiveTitle {
		t.Fatalf("resolved title changed with injected vars: %q vs %q", withForged.Derived.EffectiveTitle, plain.Derived.EffectiveTitle)
	}
	if withForged.Derived.EffectiveMediaKind != plain.Derived.EffectiveMediaKind {
		t.Fatalf("resolved media kind changed with injected vars: %q vs %q", withForged.Derived.EffectiveMediaKind, plain.Derived.EffectiveMediaKind)
	}
}

// startupPersistence is the minimal adapter the startup path needs: one stored
// source version. The production adapter is PostgreSQL; initialize only requires
// that publication and read-back agree.
type startupPersistence struct {
	source      string
	fingerprint string
}

func (p *startupPersistence) Publish(_ context.Context, source, fingerprint string) error {
	p.source, p.fingerprint = source, fingerprint
	return nil
}

func (p *startupPersistence) Current(context.Context) (string, string, error) {
	if p.source == "" {
		return "", "", errors.New("no active naming vars version")
	}
	return p.source, p.fingerprint, nil
}

// The versioned half: one stored version, active when set.
func (p *startupPersistence) ActiveVersion(context.Context) (namingvars.Version, error) {
	if p.source == "" {
		return namingvars.Version{}, namingvars.ErrVersionNotFound
	}
	return namingvars.Version{VersionInfo: namingvars.VersionInfo{Revision: 1, Digest: p.fingerprint, Active: true}, Source: p.source}, nil
}

func (p *startupPersistence) VersionByRevision(ctx context.Context, _ int64) (namingvars.Version, error) {
	return p.ActiveVersion(ctx)
}

func (p *startupPersistence) ListVersions(context.Context, int) ([]namingvars.VersionInfo, error) {
	return nil, nil
}

func (p *startupPersistence) SaveVersion(_ context.Context, params namingvars.SaveParams) (namingvars.VersionInfo, error) {
	p.source, p.fingerprint = params.Source, params.Fingerprint
	return namingvars.VersionInfo{Revision: 1, Digest: params.Fingerprint, Active: true}, nil
}

func (p *startupPersistence) ActivateRevision(context.Context, int64, int64) (namingvars.VersionInfo, error) {
	return namingvars.VersionInfo{}, namingvars.ErrVersionNotFound
}

func (p *startupPersistence) Seed(_ context.Context, params namingvars.SaveParams) (bool, error) {
	if p.source != "" {
		return false, nil
	}
	p.source, p.fingerprint = params.Source, params.Fingerprint
	return true, nil
}

// The startup path and the render path must agree: whatever init published is what
// a render injects, without any intermediate save step.
func TestInitializeThenRenderUsesThePublishedSnapshot(t *testing.T) {
	dir := t.TempDir()
	defaults := filepath.Join(dir, "defaults.js")
	// A group the shipped defaults do not declare: reusing vars.anime could hide a
	// registration gap behind an already-populated name.
	if err := os.WriteFile(defaults, []byte("vars.custom = {label: 'Fansub-X'};"), 0o644); err != nil {
		t.Fatal(err)
	}
	persistence := &startupPersistence{}
	if _, _, err := namingvars.Initialize(context.Background(), namingvars.NewStore(persistence), filepath.Join(dir, "naming-vars.js"), defaults); err != nil {
		t.Fatal(err)
	}

	engine := NewEngine()
	compiled, err := engine.Compile(KindVideoFilename, "${vars.custom.label}.mkv")
	if err != nil {
		t.Fatal(err)
	}
	ctx, err := WithCurrentVars(sampleContext(t, "sample-tv"))
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := engine.Render(compiled, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rendered != "Fansub-X.mkv" {
		t.Fatalf("rendered = %q, want the published snapshot", rendered)
	}
	// A source change published afterwards must take effect on the next render
	// without restarting the process.
	changed := "vars.custom = {label: 'Fansub-Y'};"
	// The literal output of `changed`; fixtures never run the time-limited evaluator.
	vars := namingvars.Vars{"custom": {"label": "Fansub-Y"}}
	fingerprint, err := namingvars.SourceFingerprint(changed)
	if err != nil {
		t.Fatal(err)
	}
	if err := namingvars.NewStore(persistence).PublishSource(context.Background(), changed, vars, fingerprint); err != nil {
		t.Fatal(err)
	}
	if err := namingvars.SetActive(vars, fingerprint); err != nil {
		t.Fatal(err)
	}
	ctx, err = WithCurrentVars(sampleContext(t, "sample-tv"))
	if err != nil {
		t.Fatal(err)
	}
	rendered, err = engine.Render(compiled, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rendered != "Fansub-Y.mkv" {
		t.Fatalf("rendered = %q, want the republished snapshot", rendered)
	}
}
