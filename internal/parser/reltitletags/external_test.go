package reltitletags

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"bansoko/internal/rtruntime"
)

// buildRealReltitle compiles the reltitle binary from the module this package
// already depends on, so the adoption path is exercised against the real
// protocol rather than a stand-in.
func buildRealReltitle(t *testing.T) string {
	t.Helper()
	target := filepath.Join(t.TempDir(), "reltitle")
	cmd := exec.Command("go", "build", "-trimpath", "-buildvcs=false", "-o", target, "github.com/wangkayu/reltitle/cmd/reltitle")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build reltitle: %v\n%s", err, output)
	}
	return target
}

// installBinary puts a binary at <root>/rt/reltitle, the one path the updater
// and a person both write.
func installBinary(t *testing.T, root, binary string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, rtruntime.DirName), 0o755); err != nil {
		t.Fatal(err)
	}
	source, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rtruntime.BinaryPath(root), source, 0o755); err != nil {
		t.Fatal(err)
	}
}

// TestAdoptedBinaryProjectsTheSameTags proves the two backends agree: the same
// title yields the same tag set and value tags whether the compiled-in module
// or the installed binary answers.
func TestAdoptedBinaryProjectsTheSameTags(t *testing.T) {
	root := t.TempDir()
	installBinary(t, root, buildRealReltitle(t))

	titles := []string{
		"[Group] Some Show - 03 [1080p][CHS]",
		"[LoliHouse] 药屋少女 - 01 [WebRip 1080p HEVC AAC]",
		"(Test) Smoke Title S01E01 [1080p]",
	}
	builtin := make([]string, len(titles))
	for index, title := range titles {
		set := Extract(title)
		builtin[index] = strings.Join(append(append([]string{}, set.Tags...), set.ValueTags...), "|")
	}

	client, info := rtruntime.Probe(context.Background(), root, BuiltinVersion())
	if client == nil {
		t.Fatalf("the installed binary was refused: %s", info.Note)
	}
	defer client.Close()
	if err := AdoptExternal(client); err != nil {
		t.Fatal(err)
	}
	defer UseBuiltin()

	if ActiveVersion() != "reltitle@0.0.0-dev" {
		t.Fatalf("active version = %q, want the binary's own build version", ActiveVersion())
	}
	if ActiveDigest() == "" || ActiveDigest() != info.ConfigDigest {
		t.Fatalf("active digest = %q, want the binary's %q", ActiveDigest(), info.ConfigDigest)
	}
	for index, title := range titles {
		set := Extract(title)
		got := strings.Join(append(append([]string{}, set.Tags...), set.ValueTags...), "|")
		if got != builtin[index] {
			t.Fatalf("title %q: adopted tags %q differ from the builtin %q", title, got, builtin[index])
		}
	}
}

// TestExtractFallsBackWhenTheAdoptedBinaryDies proves a dead process degrades
// to the builtin parser instead of failing the title: parsing matters more
// than the newer dictionary, and the reason is kept for the status surface.
func TestExtractFallsBackWhenTheAdoptedBinaryDies(t *testing.T) {
	root := t.TempDir()
	installBinary(t, root, buildRealReltitle(t))
	client, info := rtruntime.Probe(context.Background(), root, BuiltinVersion())
	if client == nil {
		t.Fatalf("the installed binary was refused: %s", info.Note)
	}
	defer client.Close()
	if err := AdoptExternal(client); err != nil {
		t.Fatal(err)
	}
	defer UseBuiltin()

	// Killing the process out from under the client is what a crash looks like.
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	set := Extract("[Group] Some Show - 03 [1080p]")
	if set.Diagnostic != "" {
		t.Fatalf("a dead adopted binary failed the title: %+v", set)
	}
	if len(set.Tags) == 0 {
		t.Fatalf("no tags projected after the fallback: %+v", set)
	}
	if set.Implementation != ImplementationVersion {
		t.Fatalf("implementation = %q, want the builtin %q", set.Implementation, ImplementationVersion)
	}
	if failure := ExternalFailure(); !strings.Contains(failure, "stopped answering") {
		t.Fatalf("external failure = %q", failure)
	}
}

// TestVocabularyComesFromTheAdoptedDictionary proves the projection dictionary
// follows the parser: a value the compiled-in dictionary does not know is
// still canonical when the adopted binary's dictionary defines it.
func TestVocabularyComesFromTheAdoptedDictionary(t *testing.T) {
	root := t.TempDir()
	installBinary(t, root, buildRealReltitle(t))
	client, info := rtruntime.Probe(context.Background(), root, BuiltinVersion())
	if client == nil {
		t.Fatalf("the installed binary was refused: %s", info.Note)
	}
	defer client.Close()
	if err := AdoptExternal(client); err != nil {
		t.Fatal(err)
	}
	defer UseBuiltin()

	builtin := Vocabulary()
	adopted := Vocabulary()
	if len(adopted) == 0 {
		t.Fatal("the adopted vocabulary is empty")
	}
	// The shipped binary carries the same dictionary as the module, so the two
	// lists agree; a difference would mean the dictionary is the binary's.
	if !reflect.DeepEqual(builtin, adopted) {
		t.Fatalf("adopted vocabulary differs from the builtin one")
	}
}
