package configdir

import (
	"os"
	"path/filepath"
	"testing"
)

func emptyEnv(string) string { return "" }

func TestResolveFlagAndEnvPrecedence(t *testing.T) {
	if got := Resolve("/flag/dir", func(string) string { return "/env/dir" }); got != "/flag/dir" {
		t.Fatalf("flag precedence = %q, want /flag/dir", got)
	}
	if got := Resolve("", func(string) string { return "/env/dir" }); got != "/env/dir" {
		t.Fatalf("env precedence = %q, want /env/dir", got)
	}
	if got := Resolve("  ", func(key string) string {
		if key == EnvConfigDir {
			return "  "
		}
		return ""
	}); got == "" || got == "  " {
		t.Fatalf("blank flag and env must fall through, got %q", got)
	}
}

func TestResolveFallsBackToUserConfigDir(t *testing.T) {
	// /config is absent or unwritable on development hosts, so resolution
	// lands in the OS user configuration directory.
	if WritableDir(ContainerConfigDir) {
		t.Skip("host has a writable /config; container fallback is active")
	}
	got := Resolve("", emptyEnv)
	userDir, err := os.UserConfigDir()
	if err != nil {
		t.Skipf("no user config dir on this platform: %v", err)
	}
	want := filepath.Join(userDir, "bansoko")
	if got != want {
		t.Fatalf("Resolve = %q, want %q", got, want)
	}
}

func TestWritableDirProbe(t *testing.T) {
	dir := t.TempDir()
	if !WritableDir(dir) {
		t.Fatalf("temp dir should be writable")
	}
	if WritableDir(filepath.Join(dir, "missing")) {
		t.Fatalf("missing dir must not be writable")
	}
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if WritableDir(file) {
		t.Fatalf("regular file must not be writable-dir")
	}
}

func TestFlagValueSpellings(t *testing.T) {
	if got := FlagValue([]string{"serve", "--config-dir", "/data/bansoko"}); got != "/data/bansoko" {
		t.Fatalf("space spelling = %q", got)
	}
	if got := FlagValue([]string{"--config-dir=/data/bansoko"}); got != "/data/bansoko" {
		t.Fatalf("equals spelling = %q", got)
	}
	if got := FlagValue([]string{"--listen", ":8080"}); got != "" {
		t.Fatalf("absent flag = %q, want empty", got)
	}
	if got := FlagValue([]string{"--config-dir"}); got != "" {
		t.Fatalf("dangling flag = %q, want empty", got)
	}
}

func TestEnsureDirCreatesAndKeeps(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "bansoko")
	if err := EnsureDir(dir); err != nil {
		t.Fatalf("EnsureDir create: %v", err)
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Fatalf("EnsureDir result: %v", err)
	}
	if err := EnsureDir(dir); err != nil {
		t.Fatalf("EnsureDir existing: %v", err)
	}
	if err := EnsureDir(""); err == nil {
		t.Fatalf("EnsureDir(\"\") must fail")
	}
}
