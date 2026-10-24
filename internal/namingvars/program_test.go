package namingvars

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type historicalPersistence struct {
	*fakePersistence
	history map[string]string
}

func (p historicalPersistence) SourceByFingerprint(_ context.Context, fp string) (string, error) {
	s, ok := p.history[fp]
	if !ok {
		return "", ErrProgramUnavailable
	}
	return s, nil
}

func TestPinnedProgramRealInputsAndNoActivation(t *testing.T) {
	source := `vars.release = {title: input.release_title, path: input.files[0].path};`
	fp, _ := SourceFingerprint(source)
	if err := SetActive(Vars{"sentinel": {"value": "unchanged"}}, "sentinel"); err != nil {
		t.Fatal(err)
	}
	p := historicalPersistence{newFakePersistence(), map[string]string{fp: source}}
	p.add(`vars.newest = {value: "not historical"};`, "sha256:newest", OriginFile, "")
	program, err := NewStore(p).LoadProgram(context.Background(), ProgramRef{ProfileVersion(fp), fp})
	if err != nil {
		t.Fatal(err)
	}
	for _, title := range []string{"Release - 02", "Release - 17"} {
		got, err := program.Evaluate(Input{ReleaseTitle: title, Files: []File{{Path: title + ".mkv"}}}, time.Second)
		if err != nil || got["release"]["title"] != title || got["release"]["path"] != title+".mkv" {
			t.Fatalf("real input: %v %v", got, err)
		}
	}
	active, activeFP, _ := Current()
	if activeFP != "sentinel" || active["sentinel"]["value"] != "unchanged" {
		t.Fatal("evaluation switched active")
	}
	if _, err := NewStore(p).LoadProgram(context.Background(), ProgramRef{ProfileVersion("sha256:missing"), "sha256:missing"}); !errors.Is(err, ErrProgramUnavailable) {
		t.Fatalf("missing history: %v", err)
	}
	p.history[fp] = `vars.changed = {value:"drift"};`
	if _, err := NewStore(p).LoadProgram(context.Background(), ProgramRef{ProfileVersion(fp), fp}); err == nil {
		t.Fatal("digest drift accepted")
	}
	if _, err := NewStore(p).LoadProgram(context.Background(), ProgramRef{"wrong-version", fp}); err == nil {
		t.Fatal("version mismatch accepted")
	}
}

func evaluateSource(t *testing.T, source string, input Input) (Vars, error) {
	t.Helper()
	fp, _ := SourceFingerprint(source)
	p, err := NewProgram(ProgramRef{ProfileVersion(fp), fp}, source)
	if err != nil {
		return nil, err
	}
	// Loaded CI runners can take several milliseconds for one goja call; the
	// budget only has to stop the unbounded loops below.
	return p.Evaluate(input, 100*time.Millisecond)
}
func TestProgramNeverSubstitutesSampleAndPreservesStates(t *testing.T) {
	got, err := evaluateSource(t, `vars.fact = {title: input.release_title, zero: input.files[0].size, size: input.torrent.size, count: input.torrent.file_count, release: input.release.title ?? "none"};`, Input{Files: []File{{Size: 0}}})
	if err != nil || got["fact"]["title"] != "" || got["fact"]["zero"] != "0" || got["fact"]["size"] != "0" || got["fact"]["count"] != "1" || got["fact"]["release"] != "none" {
		t.Fatalf("states: %v %v", got, err)
	}
	got, err = evaluateSource(t, `vars.fact = {title: input.release_title};`, Input{})
	if err != nil || got["fact"]["title"] != "" {
		t.Fatalf("empty real input became sample: %v %v", got, err)
	}
	for _, value := range []string{"null", "undefined"} {
		if _, err := evaluateSource(t, `vars.fact = {value: `+value+`};`, Input{}); err == nil {
			t.Fatalf("%s silently coerced", value)
		}
	}
}
func TestProgramRegexErrorsDifferFromNoMatch(t *testing.T) {
	if _, err := evaluateSource(t, `vars.x = {v: extract(input.release_title, "[")};`, Input{ReleaseTitle: "Release"}); err == nil || !strings.Contains(err.Error(), "regex") {
		t.Fatalf("invalid regex swallowed: %v", err)
	}
	got, err := evaluateSource(t, `vars.x = {v: extract(input.release_title, "(absent)") || "explicit fallback"};`, Input{ReleaseTitle: "Release"})
	if err != nil || got["x"]["v"] != "explicit fallback" {
		t.Fatalf("fallback: %v %v", got, err)
	}
}
func TestProgramSafetyAndBudgets(t *testing.T) {
	for _, source := range []string{
		`vars.x = {v: Date.now()};`, `vars.x = {v: Math.random()};`,
		`vars.x = {v: globalThis["Date"].now()};`,
		`vars.x = {v: "".constructor.constructor("return 1")()};`,
		`vars.x = {v: "x".repeat(100000000)};`, `while (true) {}`,
		`vars.x = {v: require("fs")};`, `vars.x = {v: fetch("https://example.org")};`,
		`vars.x = {v: input.files[0].missing};`,
		`input.release_title = "forged"; vars.x = {v: input.release_title};`,
		`vars.x = {v: input.mediainfo || null};`,
	} {
		if _, err := evaluateSource(t, source, Input{Files: []File{{}}}); err == nil {
			t.Fatalf("unsafe accepted: %s", source)
		}
	}
	if _, err := evaluateSource(t, `vars.x = {v: input.release_title};`, Input{ReleaseTitle: strings.Repeat("x", MaxInputBytes+1)}); err == nil {
		t.Fatal("large input accepted")
	}
	if _, err := evaluateSource(t, `vars.x = {v: input.release_title};`, Input{ReleaseTitle: strings.Repeat("x", MaxOutputBytes+1)}); err == nil {
		t.Fatal("large output accepted")
	}
}

func TestProgramConcurrentEvaluationIsIsolated(t *testing.T) {
	source := `vars.release = {title: input.release_title};`
	fp, _ := SourceFingerprint(source)
	program, err := NewProgram(ProgramRef{ProfileVersion(fp), fp}, source)
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 16)
	for i := 0; i < 16; i++ {
		go func() {
			got, err := program.Evaluate(Input{ReleaseTitle: "isolated"}, DefaultTimeout)
			if err == nil && got["release"]["title"] != "isolated" {
				err = errors.New("VM state leaked")
			}
			results <- err
		}()
	}
	for i := 0; i < 16; i++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
}

// The release-level input is a pure function of the title and the files.
func TestProgramInputContracts(t *testing.T) {
	input := Input{ReleaseTitle: "[G] Show - 07", Files: []File{{Name: "a.mkv", Path: "d/a.mkv", Size: 30}, {Name: "a.ass", Path: "d/a.ass", Size: 12}}}
	got, err := evaluateSource(t, `vars.t = {title: input.release.title, size: input.torrent.size, count: input.torrent.file_count, media: typeof input.mediainfo};`, input)
	if err != nil || got["t"]["title"] != "[G] Show - 07" || got["t"]["size"] != "42" || got["t"]["count"] != "2" || got["t"]["media"] != "undefined" {
		t.Fatalf("program input: %v %v", got, err)
	}
	if empty := (Input{}).Payload(); empty["torrent"].(map[string]any)["size"] != nil || empty["release"].(map[string]any)["title"] != nil {
		t.Fatalf("absent facts must be null: %v", empty)
	}
	source := `vars.t = {title: input.release.title};`
	fp, _ := SourceFingerprint(source)
	program, err := NewProgram(ProgramRef{Version: ProfileBase + "@" + fp, Digest: fp}, source)
	if err != nil || program.RuntimeVersion() != ProfileBase {
		t.Fatalf("%s program: %v %v", ProfileBase, program.RuntimeVersion(), err)
	}
	if _, err := NewProgram(ProgramRef{Version: "naming-vars-v2@" + fp, Digest: fp}, source); err == nil {
		t.Fatal("unknown runtime accepted")
	}
}
