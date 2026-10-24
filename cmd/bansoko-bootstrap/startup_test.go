package main

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bansoko/internal/runtime/bootprepare"
	"bansoko/internal/runtime/bootsecrets"
	"bansoko/internal/runtime/deploymentidentity"
)

type envRecorder struct {
	values map[string]string
	set    map[string]string
}

func newEnvRecorder(values map[string]string) *envRecorder {
	return &envRecorder{values: values, set: map[string]string{}}
}

func (e *envRecorder) get(key string) string {
	if value, ok := e.set[key]; ok {
		return value
	}
	return e.values[key]
}

func (e *envRecorder) setenv(key, value string) error {
	e.set[key] = value
	return nil
}

func discardLog(string, ...any) {}

func TestPrepareDeploymentEnvironmentDerivesRuntimeAndReusesSecrets(t *testing.T) {
	configDir := t.TempDir()
	owner := "postgres://owner:owner-pass@db.internal:5433/bansoko?sslmode=disable"
	first := newEnvRecorder(map[string]string{
		bootsecrets.EnvDatabaseURL: owner,
		bootsecrets.EnvConfigDir:   configDir,
		// A user-supplied runtime connection is never honoured.
		bootsecrets.EnvRuntimeDatabaseURL: "postgres://someone:else@elsewhere/other",
	})
	var logged []string
	logger := func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) }
	if err := prepareDeploymentEnvironment(first.get, first.setenv, logger); err != nil {
		t.Fatal(err)
	}
	runtimeDSN := first.set[bootsecrets.EnvRuntimeDatabaseURL]
	parsed, err := url.Parse(runtimeDSN)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.User.Username() != bootsecrets.RuntimeRoleName || parsed.Host != "db.internal:5433" ||
		parsed.Path != "/bansoko" || parsed.Query().Get("sslmode") != "disable" {
		t.Fatalf("runtime DSN %q does not mirror the owner connection", runtimeDSN)
	}
	for _, key := range []string{bootsecrets.EnvSessionSecret, bootsecrets.EnvSecretEncryptionKey} {
		if len(first.set[key]) != 64 {
			t.Fatalf("%s was not exported with a generated value", key)
		}
	}
	if _, ok := first.set[bootsecrets.EnvDatabaseURL]; ok {
		t.Fatal("the owner connection must stay as given")
	}
	if len(logged) != 1 || !strings.Contains(logged[0], "generated") {
		t.Fatalf("generation log = %v", logged)
	}
	for _, line := range logged {
		for _, value := range first.set {
			if strings.Contains(line, value) {
				t.Fatal("a generated value was logged")
			}
		}
	}
	info, err := os.Stat(bootsecrets.FilePath(configDir))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("secrets file = %v err=%v, want mode 0600", info, err)
	}

	second := newEnvRecorder(map[string]string{bootsecrets.EnvDatabaseURL: owner, bootsecrets.EnvConfigDir: configDir})
	logged = nil
	if err := prepareDeploymentEnvironment(second.get, second.setenv, logger); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{bootsecrets.EnvRuntimeDatabaseURL, bootsecrets.EnvSessionSecret, bootsecrets.EnvSecretEncryptionKey} {
		if second.set[key] != first.set[key] {
			t.Fatalf("%s changed between starts", key)
		}
	}
	if len(logged) != 0 {
		t.Fatalf("a restart logged generation: %v", logged)
	}

	override := "operator-supplied-encryption-key-0123456789"
	third := newEnvRecorder(map[string]string{
		bootsecrets.EnvDatabaseURL: owner, bootsecrets.EnvConfigDir: configDir,
		bootsecrets.EnvSecretEncryptionKey: override,
	})
	if err := prepareDeploymentEnvironment(third.get, third.setenv, discardLog); err != nil {
		t.Fatal(err)
	}
	if third.set[bootsecrets.EnvSecretEncryptionKey] != override {
		t.Fatal("the environment override did not win")
	}
	raw, _ := os.ReadFile(bootsecrets.FilePath(configDir))
	if strings.Contains(string(raw), override) {
		t.Fatal("the environment override was persisted")
	}
}

func TestPrepareDeploymentEnvironmentFailsClosed(t *testing.T) {
	corruptDir := t.TempDir()
	if err := os.WriteFile(bootsecrets.FilePath(corruptDir), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, values := range map[string]map[string]string{
		"no database url":   {bootsecrets.EnvConfigDir: t.TempDir()},
		"missing config":    {bootsecrets.EnvDatabaseURL: "postgres://owner:pw@db/bansoko", bootsecrets.EnvConfigDir: filepath.Join(t.TempDir(), "absent")},
		"corrupt secrets":   {bootsecrets.EnvDatabaseURL: "postgres://owner:pw@db/bansoko", bootsecrets.EnvConfigDir: corruptDir},
		"keyword-form dsn":  {bootsecrets.EnvDatabaseURL: "host=db user=owner password=pw dbname=bansoko", bootsecrets.EnvConfigDir: t.TempDir()},
		"runtime role user": {bootsecrets.EnvDatabaseURL: "postgres://bansoko_app:pw@db/bansoko", bootsecrets.EnvConfigDir: t.TempDir()},
	} {
		t.Run(name, func(t *testing.T) {
			env := newEnvRecorder(values)
			err := prepareDeploymentEnvironment(env.get, env.setenv, discardLog)
			if err == nil {
				t.Fatal("accepted")
			}
			if len(env.set) != 0 {
				t.Fatalf("exported %v despite the failure", env.set)
			}
		})
	}
	if raw, _ := os.ReadFile(bootsecrets.FilePath(corruptDir)); string(raw) != "{not json" {
		t.Fatal("the corrupt secrets file was replaced")
	}
}

type launchRecorder struct {
	events     []string
	migrateEnv []string
	execEnv    []string
	migrateErr error
}

func installLaunchSeams(t *testing.T, rec *launchRecorder) {
	t.Helper()
	oldExec, oldMigrate, oldConsume, oldPrepare := execProcess, runStartupMigration, enterConsumeFlow, prepareEnvironment
	t.Cleanup(func() {
		execProcess, runStartupMigration, enterConsumeFlow, prepareEnvironment = oldExec, oldMigrate, oldConsume, oldPrepare
	})
	prepareEnvironment = func() error {
		rec.events = append(rec.events, "prepare")
		return nil
	}
	runStartupMigration = func(binary string, env []string) error {
		rec.events = append(rec.events, "migrate:"+binary)
		rec.migrateEnv = env
		return rec.migrateErr
	}
	execProcess = func(binary string, _ []string, env []string) error {
		rec.events = append(rec.events, "exec:"+binary)
		rec.execEnv = env
		return nil
	}
	enterConsumeFlow = func(string) int {
		rec.events = append(rec.events, "consume")
		return 0
	}
}

func envValue(env []string, key string) (string, bool) {
	for _, entry := range env {
		if k, v, ok := strings.Cut(entry, "="); ok && k == key {
			return v, true
		}
	}
	return "", false
}

func TestRunServerMigratesSelectedTreeBeforeServing(t *testing.T) {
	root := t.TempDir()
	t.Setenv(deploymentidentity.EnvUpdatesRoot, root)
	t.Setenv("BANSOKO_UPDATE_MODE", "manual")
	t.Setenv(bootsecrets.EnvRuntimeDatabaseURL, "postgres://bansoko_app:derived@db/bansoko")
	rec := &launchRecorder{}
	installLaunchSeams(t, rec)

	if code := run("server"); code != 0 {
		t.Fatalf("run = %d", code)
	}
	want := []string{"prepare", "migrate:" + baseMigratePath, "exec:" + baseServerPath}
	if strings.Join(rec.events, "|") != strings.Join(want, "|") {
		t.Fatalf("events = %v, want %v", rec.events, want)
	}
	if dir, _ := envValue(rec.migrateEnv, envSchemaDir); dir != baseSchemaDir {
		t.Fatalf("migration ran against schema dir %q, want the served tree %q", dir, baseSchemaDir)
	}
	if dsn, _ := envValue(rec.migrateEnv, bootsecrets.EnvRuntimeDatabaseURL); dsn == "" {
		t.Fatal("migration child did not inherit the runtime connection it provisions")
	}
	if dir, _ := envValue(rec.execEnv, envSchemaDir); dir != baseSchemaDir {
		t.Fatalf("server schema dir %q", dir)
	}
	if _, ok := envValue(rec.execEnv, deploymentidentity.EnvSource); ok {
		t.Fatal("a manual base launch must not inject deployment identity facts")
	}
}

func TestRunServerEmbeddedBaseInjectsIdentity(t *testing.T) {
	root := t.TempDir()
	t.Setenv(deploymentidentity.EnvUpdatesRoot, root)
	t.Setenv("BANSOKO_UPDATE_MODE", "embedded")
	t.Setenv(bootsecrets.EnvRuntimeDatabaseURL, "postgres://bansoko_app:derived@db/bansoko")
	rec := &launchRecorder{}
	installLaunchSeams(t, rec)

	if code := run("server"); code != 0 {
		t.Fatalf("run = %d", code)
	}
	if source, _ := envValue(rec.execEnv, deploymentidentity.EnvSource); source != deploymentidentity.SourceBase {
		t.Fatalf("embedded base launch identity source = %q, want base", source)
	}
}

func TestLaunchPlanOverlayMigratesWithOverlayBinary(t *testing.T) {
	publicKeyPath := filepath.Join(t.TempDir(), "public.key")
	if err := os.WriteFile(publicKeyPath, []byte("dd06de8c251e7cda67ddb1a5f674867df6fbf1d8712585b0a4c0ee9a16e8cc11\n"), 0o444); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BANSOKO_UPDATE_PUBLIC_KEY_PATH", publicKeyPath)
	root := t.TempDir()
	writeReleaseTree(t, root, "release-b2")
	seedReleaseProvenance(t, root, "release-b2")
	writePointer(t, root, "release-b2")
	chosen := planLaunch("server", root)
	if chosen.mode != modeOverlay {
		t.Fatalf("plan = %+v", chosen)
	}
	rec := &launchRecorder{}
	installLaunchSeams(t, rec)
	if code := launchPlan(chosen, root); code != 0 {
		t.Fatalf("launchPlan = %d", code)
	}
	if len(rec.events) != 2 || rec.events[0] != "migrate:"+chosen.migratePath || rec.events[1] != "exec:"+chosen.binaryPath {
		t.Fatalf("events = %v", rec.events)
	}
	if dir, _ := envValue(rec.migrateEnv, envSchemaDir); dir != chosen.schemaDir {
		t.Fatalf("overlay migration schema dir = %q, want %q", dir, chosen.schemaDir)
	}
	if source, _ := envValue(rec.migrateEnv, deploymentidentity.EnvSource); source != deploymentidentity.SourceOverlay {
		t.Fatalf("overlay migration identity source = %q", source)
	}
}

func TestLaunchPlanMigrationFailureNeverServes(t *testing.T) {
	rec := &launchRecorder{migrateErr: errors.New("exit status 1")}
	installLaunchSeams(t, rec)
	chosen := basePlan(plan{kind: "server"}, "test")
	if code := launchPlan(chosen, ""); code == 0 {
		t.Fatal("a failed startup migration reported success")
	}
	for _, event := range rec.events {
		if strings.HasPrefix(event, "exec:") {
			t.Fatalf("server started after a failed migration: %v", rec.events)
		}
	}

	rec2 := &launchRecorder{}
	installLaunchSeams(t, rec2)
	missing := basePlan(plan{kind: "server"}, "test")
	missing.migratePath = ""
	if code := launchPlan(missing, ""); code == 0 || len(rec2.events) != 0 {
		t.Fatalf("a server plan without a migration binary launched: code=%d events=%v", code, rec2.events)
	}
}

func TestLaunchPlanMigrateKindHasNoStartupMigration(t *testing.T) {
	rec := &launchRecorder{}
	installLaunchSeams(t, rec)
	if code := launchPlan(basePlan(plan{kind: "migrate"}, "test"), ""); code != 0 {
		t.Fatalf("launchPlan = %d", code)
	}
	if len(rec.events) != 1 || rec.events[0] != "exec:"+baseMigratePath {
		t.Fatalf("events = %v, want only the migrate exec", rec.events)
	}
}

// nonRootSystem reports a fixed unprivileged identity, so the test never
// changes the identity of the test process even when CI runs as root.
type nonRootSystem struct {
	bootprepare.OSPrivilegeSystem
}

func (nonRootSystem) Geteuid() int { return 4321 }
func (nonRootSystem) Getuid() int  { return 4321 }
func (nonRootSystem) Getgid() int  { return 4322 }

func TestEstablishProcessPrivilegesExportsEffectiveIDs(t *testing.T) {
	env := newEnvRecorder(map[string]string{
		bootsecrets.EnvConfigDir:          t.TempDir(),
		deploymentidentity.EnvUpdatesRoot: t.TempDir(),
	})
	if err := establishProcessPrivileges(env.get, env.setenv, nonRootSystem{}); err != nil {
		t.Fatal(err)
	}
	if env.set["PUID"] != "4321" || env.set["PGID"] != "4322" {
		t.Fatalf("exported ids = %v, want the effective non-root identity", env.set)
	}
	bad := newEnvRecorder(map[string]string{"PUID": "root"})
	if err := establishProcessPrivileges(bad.get, bad.setenv, nonRootSystem{}); err == nil || len(bad.set) != 0 {
		t.Fatalf("malformed PUID err=%v set=%v", err, bad.set)
	}
}

func TestRunPendingMarkerGoesToConsumeFlowWithoutStartupMigration(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, pendingMarkerName), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(deploymentidentity.EnvUpdatesRoot, root)
	t.Setenv("BANSOKO_UPDATE_MODE", "embedded")
	rec := &launchRecorder{}
	installLaunchSeams(t, rec)
	if code := run("server"); code != 0 {
		t.Fatalf("run = %d", code)
	}
	if strings.Join(rec.events, "|") != "prepare|consume" {
		t.Fatalf("events = %v, want the consume flow alone after environment preparation", rec.events)
	}
}
