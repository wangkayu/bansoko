package bootsecrets

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func envMap(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func readStored(t *testing.T, dir string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(FilePath(dir))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func TestResolveGeneratesOnceAndReuses(t *testing.T) {
	dir := t.TempDir()
	first, err := Resolve(dir, envMap(nil), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Generated) != 3 {
		t.Fatalf("first start generated %v, want all three", first.Generated)
	}
	for name, value := range map[string]string{
		"session":    first.Values.SessionSecret,
		"encryption": first.Values.SecretEncryptionKey,
		"runtime":    first.Values.DatabaseRuntimePassword,
	} {
		if len(value) != 64 || strings.Trim(value, "0123456789abcdef") != "" {
			t.Fatalf("%s secret %q is not 64 hex characters", name, value)
		}
	}
	if first.Values.SessionSecret == first.Values.SecretEncryptionKey {
		t.Fatal("independent secrets must differ")
	}
	info, err := os.Stat(FilePath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("secrets file mode = %v, want 0600", info.Mode().Perm())
	}
	leftovers, _ := filepath.Glob(filepath.Join(dir, ".*.tmp"))
	if len(leftovers) != 0 {
		t.Fatalf("temporary files left behind: %v", leftovers)
	}

	second, err := Resolve(dir, envMap(nil), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Generated) != 0 || second.Values != first.Values {
		t.Fatalf("second start = %+v generated=%v, want the stored values unchanged", second.Values, second.Generated)
	}
	loaded, err := Load(dir, envMap(nil))
	if err != nil || loaded != first.Values {
		t.Fatalf("Load = %+v err=%v", loaded, err)
	}
}

func TestResolveEnvironmentOverrideWinsAndIsNotPersisted(t *testing.T) {
	dir := t.TempDir()
	session := "env-session-secret-0123456789abcdef0123"
	key := "env-encryption-key-0123456789abcdef01234"
	env := envMap(map[string]string{EnvSessionSecret: session, EnvSecretEncryptionKey: key})
	result, err := Resolve(dir, env, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Values.SessionSecret != session || result.Values.SecretEncryptionKey != key {
		t.Fatalf("environment overrides were not used: %+v", result.Values)
	}
	if len(result.Generated) != 1 || result.Generated[0] != fieldRuntimePassword {
		t.Fatalf("generated = %v, want only the runtime password", result.Generated)
	}
	raw, err := os.ReadFile(FilePath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(session)) || bytes.Contains(raw, []byte(key)) {
		t.Fatal("an environment override was written to the secrets file")
	}
	stored := readStored(t, dir)
	if _, ok := stored[fieldSessionSecret]; ok {
		t.Fatalf("overridden session secret has a stored value: %v", stored)
	}

	// Later, the override is removed: the missing values are generated then,
	// and the runtime password stays the same.
	later, err := Resolve(dir, envMap(nil), nil)
	if err != nil {
		t.Fatal(err)
	}
	if later.Values.DatabaseRuntimePassword != result.Values.DatabaseRuntimePassword {
		t.Fatal("runtime password changed between starts")
	}
	if len(later.Generated) != 2 {
		t.Fatalf("generated = %v, want the two previously overridden values", later.Generated)
	}

	// With a stored value present, the override still wins.
	override, err := Resolve(dir, env, nil)
	if err != nil || override.Values.SecretEncryptionKey != key || len(override.Generated) != 0 {
		t.Fatalf("override over stored value = %+v generated=%v err=%v", override.Values, override.Generated, err)
	}
}

func TestResolveFailsClosedOnCorruptOrUnreadableFile(t *testing.T) {
	valid := `{"version":1,"session_secret":"` + strings.Repeat("a", 64) + `"}`
	for name, content := range map[string]string{
		"empty":           "",
		"truncated":       `{"version":1,"session_secret":"abc`,
		"unknown field":   `{"version":1,"extra":"x"}`,
		"wrong version":   `{"version":2}`,
		"short value":     `{"version":1,"secret_encryption_key":"short"}`,
		"whitespace":      `{"version":1,"session_secret":"` + strings.Repeat("a", 40) + ` x"}`,
		"trailing object": valid + `{}`,
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(FilePath(dir), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := Resolve(dir, envMap(nil), nil)
			if !errors.Is(err, ErrSecretsFileUnusable) {
				t.Fatalf("Resolve error = %v, want ErrSecretsFileUnusable", err)
			}
			after, readErr := os.ReadFile(FilePath(dir))
			if readErr != nil || string(after) != content {
				t.Fatal("a corrupt secrets file was rewritten")
			}
			if _, err := Load(dir, envMap(nil)); !errors.Is(err, ErrSecretsFileUnusable) {
				t.Fatalf("Load error = %v, want ErrSecretsFileUnusable", err)
			}
		})
	}

	t.Run("unreadable", func(t *testing.T) {
		// A directory in the file's place is unreadable for every user,
		// including root, so the case does not depend on host privileges.
		dir := t.TempDir()
		if err := os.Mkdir(FilePath(dir), 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := Resolve(dir, envMap(nil), nil); !errors.Is(err, ErrSecretsFileUnusable) {
			t.Fatalf("Resolve error = %v, want ErrSecretsFileUnusable", err)
		}
		if _, err := Load(dir, envMap(nil)); !errors.Is(err, ErrSecretsFileUnusable) {
			t.Fatalf("Load error = %v, want ErrSecretsFileUnusable", err)
		}
	})
}

func TestResolveRequiresConfigDirectory(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	if _, err := Resolve(missing, envMap(nil), nil); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("missing config dir error = %v", err)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("Resolve created the config directory")
	}
}

func TestResolveRandomFailureWritesNothing(t *testing.T) {
	dir := t.TempDir()
	if _, err := Resolve(dir, envMap(nil), bytes.NewReader(make([]byte, 10))); err == nil {
		t.Fatal("short random source was accepted")
	}
	if _, err := os.Stat(FilePath(dir)); !os.IsNotExist(err) {
		t.Fatal("a partial secrets file was written")
	}
}

func TestReplaceEncryptionKey(t *testing.T) {
	dir := t.TempDir()
	first, err := Resolve(dir, envMap(nil), nil)
	if err != nil {
		t.Fatal(err)
	}
	newKey := strings.Repeat("b", 48)
	if err := ReplaceEncryptionKey(dir, newKey); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(dir, envMap(nil))
	if err != nil {
		t.Fatal(err)
	}
	if loaded.SecretEncryptionKey != newKey || loaded.SessionSecret != first.Values.SessionSecret ||
		loaded.DatabaseRuntimePassword != first.Values.DatabaseRuntimePassword {
		t.Fatalf("replace changed the wrong fields: %+v", loaded)
	}
	if err := ReplaceEncryptionKey(dir, "short"); err == nil {
		t.Fatal("short key accepted")
	}
	if err := ReplaceEncryptionKey(t.TempDir(), newKey); err == nil {
		t.Fatal("replace without an existing file succeeded")
	}
}

func TestRuntimeDatabaseURL(t *testing.T) {
	password := "p@ss:/word?#%&=+ " + strings.Repeat("x", 20)
	got, err := RuntimeDatabaseURL("postgres://owner:secret@db.example:6543/bansoko?sslmode=require&application_name=x", password)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	gotPassword, _ := parsed.User.Password()
	if parsed.User.Username() != RuntimeRoleName || gotPassword != password {
		t.Fatalf("credentials = %q/%q", parsed.User.Username(), gotPassword)
	}
	if parsed.Host != "db.example:6543" || parsed.Path != "/bansoko" || parsed.Scheme != "postgres" {
		t.Fatalf("location changed: %s", got)
	}
	if parsed.Query().Get("sslmode") != "require" || parsed.Query().Get("application_name") != "x" {
		t.Fatalf("query parameters lost: %s", got)
	}
	if strings.Contains(got, "secret") || strings.Contains(got, "owner") {
		t.Fatalf("owner credentials leaked into the runtime URL: %s", got)
	}
	role, pass, err := RuntimeCredentials(got)
	if err != nil || role != RuntimeRoleName || pass != password {
		t.Fatalf("RuntimeCredentials = %q %q %v", role, pass, err)
	}

	if alt, err := RuntimeDatabaseURL("postgresql://owner@[::1]/db", strings.Repeat("a", 64)); err != nil || !strings.HasPrefix(alt, "postgresql://bansoko_app:") || !strings.Contains(alt, "[::1]") {
		t.Fatalf("postgresql scheme/IPv6 = %q %v", alt, err)
	}

	owner := "postgres://owner:topsecret@db/bansoko"
	for name, dsn := range map[string]string{
		"empty":         "",
		"keyword form":  "host=db user=owner password=topsecret dbname=bansoko",
		"other scheme":  "mysql://owner:topsecret@db/bansoko",
		"no database":   "postgres://owner:topsecret@db",
		"no user":       "postgres://db/bansoko",
		"runtime role":  "postgres://bansoko_app:topsecret@db/bansoko",
		"group role":    "postgres://bansoko_runtime:topsecret@db/bansoko",
		"nested path":   "postgres://owner:topsecret@db/a/b",
		"no host":       "postgres:///bansoko",
		"short runtime": owner,
	} {
		pw := strings.Repeat("a", 64)
		if name == "short runtime" {
			pw = "short"
		}
		_, err := RuntimeDatabaseURL(dsn, pw)
		if err == nil {
			t.Fatalf("%s: accepted", name)
		}
		if strings.Contains(err.Error(), "topsecret") {
			t.Fatalf("%s: error echoed the DSN: %v", name, err)
		}
	}
}
