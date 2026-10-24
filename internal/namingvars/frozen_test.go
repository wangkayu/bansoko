package namingvars

import (
	"strings"
	"testing"
	"time"
)

func TestFrozenEvaluationRealReleaseInventoryAndExplicitFailure(t *testing.T) {
	source := `vars.actual = {title: input.release_title, first: input.files[0].path, second_size: input.files[1].size};`
	fp, _ := SourceFingerprint(source)
	program, err := NewProgram(ProgramRef{Version: ProfileVersion(fp), Digest: fp}, source)
	if err != nil {
		t.Fatal(err)
	}
	for _, title := range []string{"Release A 01", "Release B 02"} {
		input := Input{ReleaseTitle: title, Files: []File{{Name: "01.mkv", Path: "season/01.mkv", Size: 31}, {Name: "02.ass", Path: "subs/02.ass", Size: 7}}}
		got := program.FreezeEvaluation(input)
		if got.Status != "ok" || got.Output["actual"]["title"] != title || got.Output["actual"]["first"] != "season/01.mkv" || got.Output["actual"]["second_size"] != "7" {
			t.Fatalf("frozen=%+v", got)
		}
		input.Files[0].Path = "mutated"
		if got.Input.Files[0].Path != "season/01.mkv" {
			t.Fatal("frozen input aliases caller")
		}
	}
	source = `vars.actual = {missing: input.release.absent};`
	fp, _ = SourceFingerprint(source)
	program, err = NewProgram(ProgramRef{Version: ProfileVersion(fp), Digest: fp}, source)
	if err != nil {
		t.Fatal(err)
	}
	got := program.FreezeEvaluation(Input{ReleaseTitle: "Release", Files: []File{}})
	if got.Status != "error" || got.Output != nil || !strings.Contains(got.Diagnostic, "explicit fallback") {
		t.Fatalf("silent fallback: %+v", got)
	}
}

// A time-limit interrupt is transient: Freeze returns it without a frozen
// record, while a deterministic failure is still frozen as Status "error".
// The 1ms cap is injected; the loop is stopped by the interrupt, not a sleep.
func TestFreezeDoesNotFreezeTimeLimitInterrupt(t *testing.T) {
	source := `while (true) {}`
	fp, _ := SourceFingerprint(source)
	program, err := NewProgram(ProgramRef{Version: ProfileVersion(fp), Digest: fp}, source)
	if err != nil {
		t.Fatal(err)
	}
	frozen, err := program.freeze(Input{ReleaseTitle: "Release"}, time.Millisecond)
	if !IsInterrupted(err) || frozen.Status == "ok" || frozen.Output != nil {
		t.Fatalf("interrupt must be returned, not frozen: frozen=%+v err=%v", frozen, err)
	}
	source = `vars.actual = {missing: extract(input.release_title, "(never)")};`
	fp, _ = SourceFingerprint(source)
	program, err = NewProgram(ProgramRef{Version: ProfileVersion(fp), Digest: fp}, source)
	if err != nil {
		t.Fatal(err)
	}
	frozen, err = program.Freeze(Input{ReleaseTitle: "Release"})
	if err != nil || frozen.Status != "error" || frozen.Diagnostic == "" {
		t.Fatalf("deterministic failure must be frozen: frozen=%+v err=%v", frozen, err)
	}
}
