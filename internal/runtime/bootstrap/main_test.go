package bootstrap

import (
	"archive/tar"
	"compress/gzip"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	hardeningdomain "bansoko/internal/hardening/domain"
	"bansoko/internal/runtime/config"
	"bansoko/internal/runtime/deploymentidentity"
	"bansoko/internal/runtime/deploymentprovenance"
	updateapplication "bansoko/internal/update/application"
	embeddedupdate "bansoko/internal/update/journal"
	"bansoko/internal/update/packagemaker"
)

var (
	testReleaseKeyOnce sync.Once
	testReleasePublic  ed25519.PublicKey
	testReleasePrivate ed25519.PrivateKey
)

// writeReleaseTree materializes a minimal payload layout whose
// release-metadata.json hashes match the written bytes exactly, mirroring the
// deterministic packager output shape consumed by bootstrap verification.
func writeReleaseTree(t *testing.T, root, releaseID string) string {
	t.Helper()
	testReleaseKeyOnce.Do(func() {
		var err error
		testReleasePublic, testReleasePrivate, err = hardeningdomain.GenerateSigningKey()
		if err != nil {
			t.Fatalf("generate release test key: %v", err)
		}
	})
	keyPath := filepath.Join(root, "public.key")
	if err := os.WriteFile(keyPath, []byte(hex.EncodeToString(testReleasePublic)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BANSOKO_UPDATE_PUBLIC_KEY_PATH", keyPath)
	releaseDir := filepath.Join(root, releasesDirName, releaseID)
	files := map[string][]byte{
		packagemaker.ServerEntryPath:                   []byte("#!/bin/elf-fake-server\n"),
		packagemaker.MigrationsPrefix + "001_init.sql": []byte("CREATE TABLE noop();\n"),
		packagemaker.FrontendPrefix + "index.html":     []byte("<html></html>\n"),
		"deployment/defaults/attr-rules.yaml":          []byte("version: 1\nresolution:\n  - 1080p\n"),
		"deployment/defaults/naming-vars.js":           []byte("vars.anime = {ep: '1'};\n"),
	}
	sourceRoot := t.TempDir()
	for path, content := range files {
		fullPath := filepath.Join(sourceRoot, filepath.FromSlash(path))
		if mkdirErr := os.MkdirAll(filepath.Dir(fullPath), 0o755); mkdirErr != nil {
			t.Fatal(mkdirErr)
		}
		mode := os.FileMode(0o644)
		if strings.HasSuffix(path, packagemaker.ServerEntryPath) {
			mode = 0o755
		}
		if writeErr := os.WriteFile(fullPath, content, mode); writeErr != nil {
			t.Fatal(writeErr)
		}
	}

	meta := packagemaker.Metadata{
		FormatVersion:       hardeningdomain.EmbeddedUpdateFormatV1,
		ReleaseID:           releaseID,
		Version:             "0.9.0-rc.10",
		BuildID:             "b2-test",
		TargetSchemaVersion: "schema-test",
		Platform:            "linux",
		Architecture:        "amd64",
	}
	payloadPath := filepath.Join(t.TempDir(), "embedded-update-v1.tar.gz")
	result, buildErr := packagemaker.BuildEmbeddedPayload(packagemaker.Options{
		ServerBinaryPath: filepath.Join(sourceRoot, packagemaker.ServerEntryPath),
		MigrationsDir:    filepath.Join(sourceRoot, "db/migrations"), FrontendDistDir: filepath.Join(sourceRoot, "frontend/dist"),
		DefaultsDir: filepath.Join(sourceRoot, "deployment/defaults"), Metadata: meta,
		OutputPath: payloadPath, SourceDateEpoch: 1756000000,
	})
	if buildErr != nil {
		t.Fatal(buildErr)
	}
	if err := os.MkdirAll(releaseDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := extractPayloadFixture(payloadPath, releaseDir); err != nil {
		t.Fatal(err)
	}
	archiveMeta, err := os.ReadFile(filepath.Join(releaseDir, packagemaker.MetaEntryPath))
	if err != nil {
		t.Fatal(err)
	}
	var unpackedMeta packagemaker.Metadata
	if err := json.Unmarshal(archiveMeta, &unpackedMeta); err != nil || len(unpackedMeta.Files) == 0 {
		t.Fatalf("payload metadata was not materialized: err=%v raw=%q", err, archiveMeta)
	}
	payloadSignature, err := hardeningdomain.SignArtifactDigest(testReleasePrivate, result.Digest)
	if err != nil {
		t.Fatal(err)
	}
	manifest := hardeningdomain.ReleaseManifest{
		SchemaVersion: "release-manifest-1", ReleaseSequence: 10, ReleaseID: releaseID, Version: meta.Version,
		Channel: hardeningdomain.ReleaseChannelStable, SourceCommit: "test-source", APIContractVersion: "v2",
		TargetMigrationVersion: meta.TargetSchemaVersion, RequiredDowntime: true, BackupRequired: true,
		Rollback: hardeningdomain.RestoreRequired, ArtifactDigest: strings.Repeat("a", 64), Immutable: true,
		Artifacts: []hardeningdomain.ArtifactRef{{Platform: "linux", Architecture: "amd64", Digest: strings.Repeat("b", 64), Size: 1, Signature: "test"}},
		SBOMRef:   "sha256:" + strings.Repeat("c", 64), ProvenanceRef: "sha256:" + strings.Repeat("d", 64),
		EmbeddedUpdate: &hardeningdomain.EmbeddedUpdateSegment{
			FormatVersion: hardeningdomain.EmbeddedUpdateFormatV1, Platform: "linux", Architecture: "amd64", PayloadDigest: result.Digest, PayloadSize: result.Size,
			PayloadSignature: payloadSignature, BootstrapMinVersion: "embedded-bootstrap-v1", MinImageVersion: "b1", TargetReleaseID: releaseID, TargetVersion: meta.Version,
			TargetSchemaVersion: meta.TargetSchemaVersion, ReleaseSequence: 10,
		},
	}
	if err := manifest.Sign(testReleasePrivate); err != nil {
		t.Fatal(err)
	}
	manifestBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(releaseDir, updateapplication.SignedManifestEntryPath), append(manifestBytes, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(releaseDir, updateapplication.PayloadArchiveEntryPath), mustReadFile(t, payloadPath), 0o600); err != nil {
		t.Fatal(err)
	}
	// Deployment provenance is deliberately NOT written here: the bootstrap
	// publishes it when it promotes the release, so a test that only materializes
	// a tree must not make the release look already promoted.
	return releaseDir
}

// seedReleaseProvenance writes a durable provenance record for a release that a
// test needs to look already promoted (steady-serve planning and identity reads
// do not run a promotion).
func seedReleaseProvenance(t *testing.T, root, releaseID string) string {
	t.Helper()
	releaseDir := filepath.Join(root, releasesDirName, releaseID)
	meta, manifest, err := verifyReleaseTreeIdentity(releaseDir)
	if err != nil {
		t.Fatalf("verify release before seeding provenance: %v", err)
	}
	if manifest.EmbeddedUpdate == nil {
		t.Fatalf("seeded release %s has no embedded update segment", releaseID)
	}
	if err := deploymentprovenance.Write(root, deploymentprovenance.Record{
		SchemaVersion:          deploymentprovenance.FormatVersion,
		ReleaseID:              releaseID,
		PayloadDigest:          manifest.EmbeddedUpdate.PayloadDigest,
		ProducingOperationID:   "fixture-" + releaseID,
		ExpectedProcessBuildID: meta.BuildID,
	}); err != nil {
		t.Fatalf("seed release provenance: %v", err)
	}
	return deploymentprovenance.Path(root, releaseID)
}

func extractPayloadFixture(payloadPath, releaseDir string) error {
	file, err := os.Open(payloadPath)
	if err != nil {
		return err
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer gz.Close()
	reader := tar.NewReader(gz)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if header.Typeflag != tar.TypeReg {
			continue
		}
		path := filepath.Join(releaseDir, filepath.FromSlash(header.Name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		data, err := io.ReadAll(reader)
		if err != nil {
			return err
		}
		mode := os.FileMode(0o644)
		if header.Name == packagemaker.ServerEntryPath {
			mode = 0o755
		}
		if err := os.WriteFile(path, data, mode); err != nil {
			return err
		}
	}
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func writePointer(t *testing.T, root, token string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, currentPointerName), []byte(token+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func journalWithSteps(t *testing.T, updatesRoot string, opID string, steps ...embeddedupdate.Step) {
	t.Helper()
	entrypointJournal, err := embeddedupdate.Open(filepath.Join(updatesRoot, journalFileName))
	if err != nil {
		t.Fatal(err)
	}
	defer entrypointJournal.Close()
	record := embeddedupdate.Record{
		OperationID:     opID,
		Channel:         "embedded",
		TargetReleaseID: "release-b2",
	}
	stored, createErr := entrypointJournal.Create(record)
	if createErr != nil {
		t.Fatal(createErr)
	}
	version := stored.Version
	for _, step := range steps {
		advanced, advanceErr := entrypointJournal.Advance(stored.OperationID, version, step, "", "",
			func(r *embeddedupdate.Record) {
				switch step {
				case embeddedupdate.StepFenceClosurePending:
					r.FenceClosureTarget = embeddedupdate.StepSucceeded
				case embeddedupdate.StepSucceeded, embeddedupdate.StepRolledBack:
					// The terminal checkpoint records the proven close.
					r.FenceClosure = embeddedupdate.FenceClosureClosed
				}
			})
		if advanceErr != nil {
			t.Fatal(advanceErr)
		}
		version = advanced.Version
	}
}

func TestUpdateModeDefaultsAndValidationAgreeWithBootstrap(t *testing.T) {
	t.Setenv("BANSOKO_UPDATE_MODE", "")
	if got := config.UpdateModeFromEnv(os.Getenv); got != config.DefaultUpdateMode {
		t.Fatalf("config default mode = %q, want %q", got, config.DefaultUpdateMode)
	}
	if got := updateMode(); got != config.DefaultUpdateMode {
		t.Fatalf("bootstrap default mode = %q, want %q", got, config.DefaultUpdateMode)
	}
	for _, mode := range []string{"embedded", "manual"} {
		t.Setenv("BANSOKO_UPDATE_MODE", mode)
		if got := config.UpdateModeFromEnv(os.Getenv); got != mode || updateMode() != mode || !config.IsValidUpdateMode(mode) {
			t.Fatalf("mode %q was not shared by config/bootstrap", mode)
		}
	}
	for _, mode := range []string{"", "invalid", " Embedded ", "supervised"} {
		if mode == "" {
			continue
		}
		t.Setenv("BANSOKO_UPDATE_MODE", mode)
		if config.IsValidUpdateMode(config.UpdateModeFromEnv(os.Getenv)) || config.IsValidUpdateMode(updateMode()) {
			t.Fatalf("invalid mode %q was accepted", mode)
		}
	}
}

func TestPlanLaunchWithoutUpdatesVolumeFallsBackToBase(t *testing.T) {
	t.Setenv("BANSOKO_UPDATE_MODE", "manual")
	missingRoot := filepath.Join(t.TempDir(), "does-not-exist")

	got := planLaunch("server", missingRoot)
	if got.mode != modeBase || got.binaryPath != baseServerPath ||
		got.schemaDir != baseSchemaDir || got.frontendDir != baseFrontendDir || got.migratePath != baseServerPath {
		t.Fatalf("server plan = %+v, want base with base paths", got)
	}

	migrated := planLaunch("migrate", missingRoot)
	if migrated.mode != modeBase || migrated.binaryPath != baseServerPath ||
		migrated.schemaDir != baseSchemaDir || migrated.frontendDir != "" {
		t.Fatalf("migrate plan = %+v, want base migrate without frontend dir", migrated)
	}
}

func TestPlanLaunchCorruptJournalParksMaintenance(t *testing.T) {
	t.Setenv("BANSOKO_UPDATE_MODE", "embedded")
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, journalFileName), []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}

	got := planLaunch("server", root)
	if got.mode != modeMaintenance {
		t.Fatalf("corrupt journal must park maintenance, got %+v", got)
	}
	entries, readErr := os.ReadDir(root)
	if readErr != nil {
		t.Fatal(readErr)
	}
	quarantined := false
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "journal.corrupt-") {
			quarantined = true
		}
	}
	if !quarantined {
		t.Fatal("corrupt journal was not quarantined")
	}
}

func TestPlanLaunchActiveEmbeddedJournalRequiresEmbeddedMode(t *testing.T) {
	for _, mode := range []string{"manual"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("BANSOKO_UPDATE_MODE", mode)
			root := t.TempDir()
			journalWithSteps(t, root, "mode-gate-"+mode, embeddedupdate.StepFetched)
			got := planLaunch("server", root)
			if got.mode != modeMaintenance || got.maintenanceOp != "mode-gate-"+mode {
				t.Fatalf("mode %q plan = %+v, want maintenance for active embedded journal", mode, got)
			}
			if migrate := planLaunch("migrate", root); migrate.mode != modeMaintenance {
				t.Fatalf("migrate mode %q plan = %+v, want maintenance", mode, migrate)
			}
		})
	}
}

func TestPlanLaunchPendingOperationParksMaintenance(t *testing.T) {
	t.Setenv("BANSOKO_UPDATE_MODE", "embedded")
	for _, tc := range []struct {
		name  string
		steps []embeddedupdate.Step
		// consume marks steps that belong to the B4 server-side handoff
		// family: the server entry consumes them, while any other kind keeps
		// parking maintenance.
		consume bool
	}{
		{"pending_migration", []embeddedupdate.Step{
			embeddedupdate.StepFetched, embeddedupdate.StepStaged,
			embeddedupdate.StepMaintenanceEntered, embeddedupdate.StepBackupVerified,
			embeddedupdate.StepPendingMigration,
		}, true},
		{"promoted", []embeddedupdate.Step{
			embeddedupdate.StepFetched, embeddedupdate.StepStaged,
			embeddedupdate.StepMaintenanceEntered, embeddedupdate.StepBackupVerified,
			embeddedupdate.StepPendingMigration, embeddedupdate.StepMigrationSucceeded,
			embeddedupdate.StepPromoted,
		}, true},
		{"mid-staging", []embeddedupdate.Step{embeddedupdate.StepFetched}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			journalWithSteps(t, root, "op-"+tc.name, tc.steps...)

			got := planLaunch("server", root)
			wantMode := modeConsume
			if !tc.consume {
				wantMode = modeMaintenance
			}
			if got.mode != wantMode || got.maintenanceOp != "op-"+tc.name {
				t.Fatalf("plan = %+v, want mode %v with op id", got, wantMode)
			}
			if tc.consume && planLaunch("migrate", root).mode != modeMaintenance {
				t.Fatalf("migrate job must never consume the handoff")
			}
			wantStep := string(tc.steps[len(tc.steps)-1])
			if got.maintenanceStep != wantStep {
				t.Fatalf("carried step = %q, want %q", got.maintenanceStep, wantStep)
			}
		})
	}
}

func TestPlanLaunchFenceClosureHandsOffToSurvivingRelease(t *testing.T) {
	t.Setenv("BANSOKO_UPDATE_MODE", "embedded")
	prefix := []embeddedupdate.Step{
		embeddedupdate.StepFetched, embeddedupdate.StepStaged,
		embeddedupdate.StepMaintenanceEntered, embeddedupdate.StepBackupVerified,
		embeddedupdate.StepPendingMigration,
	}
	for _, test := range []struct {
		name        string
		middle      []embeddedupdate.Step
		terminal    embeddedupdate.Step
		previous    string
		wantHandoff string
	}{
		{name: "success keeps target", middle: []embeddedupdate.Step{
			embeddedupdate.StepMigrationSucceeded, embeddedupdate.StepPromoted,
		}, terminal: embeddedupdate.StepSucceeded, previous: "release-a1", wantHandoff: "release-b2"},
		{name: "rollback uses previous", middle: []embeddedupdate.Step{
			embeddedupdate.StepMigrationSucceeded, embeddedupdate.StepPromoted,
			embeddedupdate.StepReadinessFailed, embeddedupdate.StepRollbackStarted, embeddedupdate.StepDatabaseRestored,
		}, terminal: embeddedupdate.StepRolledBack, previous: "release-a1", wantHandoff: "release-a1"},
		{name: "rollback to base has no overlay handoff", middle: []embeddedupdate.Step{
			embeddedupdate.StepMigrationSucceeded, embeddedupdate.StepPromoted,
			embeddedupdate.StepReadinessFailed, embeddedupdate.StepRollbackStarted, embeddedupdate.StepDatabaseRestored,
		}, terminal: embeddedupdate.StepRolledBack},
		{name: "pre-migration abort uses previous", middle: []embeddedupdate.Step{embeddedupdate.StepFailed}, terminal: embeddedupdate.StepFailed, previous: "release-a1", wantHandoff: "release-a1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			journal, err := embeddedupdate.Open(filepath.Join(root, journalFileName))
			if err != nil {
				t.Fatal(err)
			}
			record, err := journal.Create(embeddedupdate.Record{
				OperationID: "op-fence-handoff", Channel: "embedded",
				TargetReleaseID: "release-b2", PreviousReleaseID: test.previous,
			})
			if err != nil {
				t.Fatal(err)
			}
			steps := append(append(append([]embeddedupdate.Step(nil), prefix...), test.middle...), embeddedupdate.StepFenceClosurePending)
			for _, step := range steps {
				record, err = journal.Advance(record.OperationID, record.Version, step, "", "", func(r *embeddedupdate.Record) {
					if step == embeddedupdate.StepFenceClosurePending {
						r.FenceClosureTarget = test.terminal
					}
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := journal.Close(); err != nil {
				t.Fatal(err)
			}
			chosen := planLaunch("server", root)
			if chosen.mode != modeConsume || chosen.handoffReleaseID != test.wantHandoff {
				t.Fatalf("plan = %+v, want handoff %q", chosen, test.wantHandoff)
			}
		})
	}
}

func TestPlanLaunchAllowsNonEmbeddedUpdateModesWithoutActiveJournal(t *testing.T) {
	for _, mode := range []string{"manual"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("BANSOKO_UPDATE_MODE", mode)
			got := planLaunch("server", t.TempDir())
			if got.mode != modeBase {
				t.Fatalf("mode %q plan = %+v, want base without active journal", mode, got)
			}
		})
	}
}

func TestPlanLaunchTerminalStateRunsVerifiedOverlay(t *testing.T) {
	publicKeyPath := filepath.Join(t.TempDir(), "public.key")
	if err := os.WriteFile(publicKeyPath, []byte("dd06de8c251e7cda67ddb1a5f674867df6fbf1d8712585b0a4c0ee9a16e8cc11\n"), 0o444); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BANSOKO_UPDATE_PUBLIC_KEY_PATH", publicKeyPath)
	for _, tc := range []struct {
		name  string
		steps []embeddedupdate.Step
	}{
		{"succeeded", []embeddedupdate.Step{
			embeddedupdate.StepFetched, embeddedupdate.StepStaged,
			embeddedupdate.StepMaintenanceEntered, embeddedupdate.StepBackupVerified,
			embeddedupdate.StepPendingMigration, embeddedupdate.StepMigrationSucceeded,
			embeddedupdate.StepPromoted, embeddedupdate.StepSucceeded,
		}},
		{"no record at all", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeReleaseTree(t, root, "release-b2")
			seedReleaseProvenance(t, root, "release-b2")
			writePointer(t, root, "release-b2")
			if tc.steps != nil {
				journalWithSteps(t, root, "op-"+tc.name, tc.steps...)
			}

			serverPlan := planLaunch("server", root)
			if serverPlan.mode != modeOverlay || serverPlan.releaseID != "release-b2" {
				t.Fatalf("server plan = %+v, want overlay release-b2", serverPlan)
			}
			wantSchema := filepath.Join(root, releasesDirName, "release-b2", "db/migrations")
			wantFrontend := filepath.Join(root, releasesDirName, "release-b2", "frontend/dist")
			wantMigrate := filepath.Join(root, releasesDirName, "release-b2", packagemaker.ServerEntryPath)
			if serverPlan.schemaDir != wantSchema || serverPlan.frontendDir != wantFrontend || serverPlan.migratePath != wantMigrate {
				t.Fatalf("overlay env dirs wrong: %+v", serverPlan)
			}

			migratePlan := planLaunch("migrate", root)
			if migratePlan.mode != modeOverlay ||
				migratePlan.binaryPath != filepath.Join(root, releasesDirName, "release-b2", packagemaker.ServerEntryPath) {
				t.Fatalf("migrate plan = %+v, want overlay migrate binary", migratePlan)
			}
		})
	}
}

func TestPlanLaunchFallbackToBaseOnOverlayDefects(t *testing.T) {
	t.Run("missing pointer", func(t *testing.T) {
		root := t.TempDir()
		writeReleaseTree(t, root, "release-b2")

		got := planLaunch("server", root)
		if got.mode != modeBase {
			t.Fatalf("plan = %+v, want base fallback", got)
		}
	})

	t.Run("pointer traversal token rejected", func(t *testing.T) {
		root := t.TempDir()
		writeReleaseTree(t, root, "release-b2")
		writePointer(t, root, "../evil")

		got := planLaunch("server", root)
		if got.mode != modeMaintenance {
			t.Fatalf("traversal pointer must fail closed in maintenance, got %+v", got)
		}
	})

	t.Run("tampered payload hash", func(t *testing.T) {
		root := t.TempDir()
		releaseDir := writeReleaseTree(t, root, "release-b2")
		writePointer(t, root, "release-b2")
		target := filepath.Join(releaseDir, packagemaker.FrontendPrefix, "index.html")
		if tamperErr := os.WriteFile(target, []byte("<html>tampered</html>\n"), 0o644); tamperErr != nil {
			t.Fatal(tamperErr)
		}

		got := planLaunch("server", root)
		if got.mode != modeMaintenance {
			t.Fatalf("hash mismatch must fail closed in maintenance, got %+v", got)
		}
	})

	t.Run("unknown unsupported metadata format", func(t *testing.T) {
		root := t.TempDir()
		releaseDir := writeReleaseTree(t, root, "release-b2")
		writePointer(t, root, "release-b2")
		metaPath := filepath.Join(releaseDir, packagemaker.MetaEntryPath)
		metaRaw, readErr := os.ReadFile(metaPath)
		if readErr != nil {
			t.Fatal(readErr)
		}
		var meta map[string]any
		if unmarshalErr := json.Unmarshal(metaRaw, &meta); unmarshalErr != nil {
			t.Fatal(unmarshalErr)
		}
		meta["format_version"] = "embedded-update-v999"
		nextMeta, marshalErr := json.MarshalIndent(meta, "", "  ")
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if writeErr := os.WriteFile(metaPath, nextMeta, 0o644); writeErr != nil {
			t.Fatal(writeErr)
		}

		got := planLaunch("server", root)
		if got.mode != modeMaintenance {
			t.Fatalf("unsupported format must fail closed in maintenance, got %+v", got)
		}
	})
}

func TestEnvWithOverridesReplacesInsteadOfDuplicates(t *testing.T) {
	t.Setenv("BANSOKO_SCHEMA_DIR", "/stale")
	env := envWithOverrides(map[string]string{"BANSOKO_SCHEMA_DIR": "/new"})

	count, hit := 0, ""
	for _, entry := range env {
		key, value, _ := cutEnv(entry)
		if key == "BANSOKO_SCHEMA_DIR" {
			count++
			hit = value
		}
	}
	if count != 1 || hit != "/new" {
		t.Fatalf("override produced %d entries with value %q, want exactly one /new", count, hit)
	}
}

func cutEnv(entry string) (key, value string, found bool) {
	for i := 0; i < len(entry); i++ {
		if entry[i] == '=' {
			return entry[:i], entry[i+1:], true
		}
	}
	return entry, "", false
}

func TestSafeRelPathAndReleaseTokens(t *testing.T) {
	for _, bad := range []string{"../escape", "/abs", "back\\slash", "", "a/b/../c"} {
		if safeRelPath(bad) {
			t.Fatalf("%q must be rejected", bad)
		}
	}
	for _, bad := range []string{"", ".", "..", "../evil", "a/b"} {
		if validReleaseToken(bad) {
			t.Fatalf("%q must be rejected as release token", bad)
		}
	}
	for _, good := range []string{"release-20260827-01", "embedded-update-v1"} {
		if !safeRelPath(good) || !validReleaseToken(good) {
			t.Fatalf("%q must be accepted", good)
		}
	}
}

func TestDeploymentIdentityEnvClearsOverlayFactsOnBase(t *testing.T) {
	t.Setenv(deploymentidentity.EnvSelectedReleaseID, "stale-release")
	t.Setenv(deploymentidentity.EnvPayloadDigest, "stale-digest")
	t.Setenv(deploymentidentity.EnvBaseImageDigest, "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")

	env := deploymentIdentityEnv(deploymentidentity.SourceBase, "", "", "")
	if env[deploymentidentity.EnvSource] != deploymentidentity.SourceBase {
		t.Errorf("source = %q", env[deploymentidentity.EnvSource])
	}
	if env[deploymentidentity.EnvSelectedReleaseID] != "" || env[deploymentidentity.EnvPayloadDigest] != "" {
		t.Error("base identity must clear overlay facts explicitly")
	}
	if env[deploymentidentity.EnvBaseImageDigest] != "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" {
		t.Error("base image digest must be passed through")
	}
}

func TestDeploymentIdentityEnvUsesBaseDigestAndClearsExpectedBuild(t *testing.T) {
	baseDigest := "sha256:abcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcd"
	t.Setenv(deploymentidentity.EnvBaseImageDigest, baseDigest)
	t.Setenv(deploymentidentity.EnvExpectedProcessBuild, "stale-build")
	env := deploymentIdentityEnv(deploymentidentity.SourceBase, "", "", "")
	if env[deploymentidentity.EnvBaseImageDigest] != baseDigest {
		t.Fatalf("base digest = %q, want %q", env[deploymentidentity.EnvBaseImageDigest], baseDigest)
	}
	if env[deploymentidentity.EnvExpectedProcessBuild] != "" {
		t.Fatalf("base expected build = %q, want empty", env[deploymentidentity.EnvExpectedProcessBuild])
	}
}

func TestDeploymentIdentityEnvCarriesTheOwnBootstrapVersion(t *testing.T) {
	// The updater compares this variable against a signed floor, so the wrapper
	// must always publish its own constant: an inherited value would let a
	// deployment environment claim a bootstrap the base image does not contain.
	t.Setenv(deploymentidentity.EnvBootstrapVersion, "embedded-bootstrap-v99")
	if !regexp.MustCompile(`^embedded-bootstrap-v[0-9]+$`).MatchString(embeddedBootstrapVersion) {
		t.Fatalf("bootstrap version %q does not match the enforced embedded-bootstrap-v<N> grammar", embeddedBootstrapVersion)
	}
	for _, env := range []map[string]string{
		deploymentIdentityEnv(deploymentidentity.SourceBase, "", "", ""),
		deploymentIdentityEnvWithBuild(deploymentidentity.SourceOverlay, "release-1", strings.Repeat("a", 64), "operation-1", "build-1"),
	} {
		if env[deploymentidentity.EnvBootstrapVersion] != embeddedBootstrapVersion {
			t.Fatalf("injected bootstrap version = %q, want the wrapper constant %q", env[deploymentidentity.EnvBootstrapVersion], embeddedBootstrapVersion)
		}
	}
}

func TestOverlayIdentityFromJournalOnlyForPromotedRelease(t *testing.T) {
	digest := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	root := t.TempDir()
	flowJournal, err := embeddedupdate.Open(filepath.Join(root, journalFileName))
	if err != nil {
		t.Fatal(err)
	}
	record := embeddedupdate.Record{
		OperationID:     "update-1",
		Channel:         "embedded",
		TargetReleaseID: "release-b2",
		PayloadDigest:   digest,
	}
	stored, createErr := flowJournal.Create(record)
	if createErr != nil {
		t.Fatal(createErr)
	}
	version := stored.Version
	for _, step := range []embeddedupdate.Step{embeddedupdate.StepAccepted, embeddedupdate.StepFetched,
		embeddedupdate.StepStaged, embeddedupdate.StepMaintenanceEntered, embeddedupdate.StepBackupVerified,
		embeddedupdate.StepPendingMigration, embeddedupdate.StepMigrationSucceeded, embeddedupdate.StepPromoted,
		embeddedupdate.StepSucceeded} {
		advanced, advanceErr := flowJournal.Advance(stored.OperationID, version, step, "", "", func(r *embeddedupdate.Record) {
			r.CurrentReleaseID = "release-b2"
			if step == embeddedupdate.StepSucceeded {
				r.FenceClosure = embeddedupdate.FenceClosureClosed
			}
		})
		if advanceErr != nil {
			t.Fatalf("advance to %s: %v", step, advanceErr)
		}
		version = advanced.Version
	}
	if closeErr := flowJournal.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}

	gotDigest, gotOperationID := overlayIdentityFromJournal(root, "release-b2")
	if gotDigest != digest || gotOperationID != "update-1" {
		t.Fatalf("promoted release identity = (%q, %q)", gotDigest, gotOperationID)
	}
	// A different release (e.g. a rollback fallback to previous) must never
	// borrow the rejected target's payload digest.
	otherDigest, otherOperationID := overlayIdentityFromJournal(root, "release-previous")
	if otherDigest != "" || otherOperationID != "" {
		t.Fatalf("foreign release borrowed identity: (%q, %q)", otherDigest, otherOperationID)
	}
	// Missing journal stays silent.
	if emptyDigest, emptyOp := overlayIdentityFromJournal(t.TempDir(), "release-b2"); emptyDigest != "" || emptyOp != "" {
		t.Fatal("missing journal must yield empty identity")
	}
}

func TestOverlayIdentityUsesDurableProvenanceAfterJournalArchive(t *testing.T) {
	root := t.TempDir()
	writeReleaseTree(t, root, "release-b2")
	seedReleaseProvenance(t, root, "release-b2")
	writePointer(t, root, "release-b2")
	flowJournal, err := embeddedupdate.Open(filepath.Join(root, journalFileName))
	if err != nil {
		t.Fatal(err)
	}
	created, err := flowJournal.Create(embeddedupdate.Record{OperationID: "op-archive", Channel: "embedded", TargetReleaseID: "release-b2"})
	if err != nil {
		t.Fatal(err)
	}
	failed, err := flowJournal.Advance(created.OperationID, created.Version, embeddedupdate.StepFailed, "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := flowJournal.AdvanceExpected(failed.OperationID, failed.Version, embeddedupdate.StepFailed,
		embeddedupdate.StepFailed, "", "owner-scoped fence closure verified",
		func(r *embeddedupdate.Record) { r.FenceClosure = embeddedupdate.FenceClosureClosed }); err != nil {
		t.Fatal(err)
	}
	if err := flowJournal.ArchiveTerminalProven(); err != nil {
		t.Fatal(err)
	}
	if err := flowJournal.Close(); err != nil {
		t.Fatal(err)
	}
	proof, err := verifyOverlayIdentity(root, "release-b2")
	if err != nil {
		t.Fatal(err)
	}
	if proof.PayloadDigest == "" || proof.OperationID != "fixture-release-b2" || proof.ExpectedProcessBuildID != "b2-test" {
		t.Fatalf("proof = %+v, want durable provenance facts", proof)
	}
}

func TestPlanLaunchRequiresOverlayProvenance(t *testing.T) {
	root := t.TempDir()
	releaseDir := writeReleaseTree(t, root, "release-b2")
	seedReleaseProvenance(t, root, "release-b2")
	if err := os.Remove(filepath.Join(root, deploymentprovenance.Directory, "release-b2.json")); err != nil {
		t.Fatal(err)
	}
	writePointer(t, root, "release-b2")
	if got := planLaunch("server", root); got.mode != modeMaintenance {
		t.Fatalf("missing provenance plan = %+v, want maintenance", got)
	}
	if _, err := os.Stat(releaseDir); err != nil {
		t.Fatal(err)
	}
}

func TestBootstrapPublicKeyOverrideIsPinned(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing-public.key")
	t.Setenv("BANSOKO_UPDATE_PUBLIC_KEY_PATH", missing)
	if _, err := loadBootstrapPublicKey(); err == nil {
		t.Fatal("an unreadable override fell back to the baked development key")
	}
	invalid := filepath.Join(t.TempDir(), "invalid-public.key")
	if err := os.WriteFile(invalid, []byte("not-a-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BANSOKO_UPDATE_PUBLIC_KEY_PATH", invalid)
	if _, err := loadBootstrapPublicKey(); err == nil {
		t.Fatal("an invalid override fell back to the baked development key")
	}
}

func TestUpdatesRootUsesConfiguredRootOrDefault(t *testing.T) {
	t.Setenv(deploymentidentity.EnvUpdatesRoot, " /config/updates ")
	if root := deploymentidentity.UpdatesRoot(os.Getenv); root != "/config/updates" {
		t.Fatalf("configured updates root = %q, want /config/updates", root)
	}
	t.Setenv(deploymentidentity.EnvUpdatesRoot, "")
	if root := deploymentidentity.UpdatesRoot(os.Getenv); root != deploymentidentity.DefaultUpdatesRoot {
		t.Fatalf("default updates root = %q, want %q", root, deploymentidentity.DefaultUpdatesRoot)
	}
}

func TestPlanLaunchSelectsPendingMarkerOnlyForEmbeddedServer(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, pendingMarkerName), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BANSOKO_UPDATE_MODE", "embedded")
	if got := planLaunch("server", root); got.mode != modePendingMarker {
		t.Fatalf("server plan=%+v, want pending marker consumption", got)
	}
	if got := planLaunch("migrate", root); got.mode != modeMaintenance {
		t.Fatalf("migrate plan=%+v, want fail-closed maintenance", got)
	}
}

func TestDiscardPreMarkerWorkParksActiveJournal(t *testing.T) {
	root := t.TempDir()
	journalWithSteps(t, root, "prehandoff-op", embeddedupdate.StepAccepted, embeddedupdate.StepFetched)
	if err := discardPreMarkerWork(root); err != nil {
		t.Fatalf("pre-marker cleanup with an active journal: %v", err)
	}
	if _, found, err := embeddedupdate.Snapshot(filepath.Join(root, journalFileName)); err != nil || !found {
		t.Fatalf("journal vanished after pre-marker cleanup: found=%v err=%v", found, err)
	}
	if got := planLaunch("server", root); got.mode != modeMaintenance {
		t.Fatalf("active pre-handoff journal plan=%+v, want fail-closed maintenance", got)
	}
}

func TestDiscardPreMarkerWorkLeavesMarkerEpochAlone(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, pendingMarkerName), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	journalWithSteps(t, root, "marker-epoch-op", embeddedupdate.StepAccepted, embeddedupdate.StepFetched)
	if err := discardPreMarkerWork(root); err != nil {
		t.Fatalf("a pending marker must leave the record to the consume flow: %v", err)
	}
	if _, found, err := embeddedupdate.Snapshot(filepath.Join(root, journalFileName)); err != nil || !found {
		t.Fatalf("journal vanished in the marker epoch: found=%v err=%v", found, err)
	}
}

func TestDiscardUnreferencedWorkPreservesOnlyCurrentAndPrevious(t *testing.T) {
	root := t.TempDir()
	for _, id := range []string{"current-release", "previous-release", "orphan-release"} {
		if err := os.MkdirAll(filepath.Join(root, releasesDirName, id), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, "staging", "operation"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "downloads", "operation"), 0o700); err != nil {
		t.Fatal(err)
	}
	writePointer(t, root, "current-release")
	j, err := embeddedupdate.Open(filepath.Join(root, journalFileName))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.Create(embeddedupdate.Record{
		OperationID: "cleanup-op", Channel: "embedded", TargetReleaseID: "target-release",
		Step: embeddedupdate.StepRollbackStarted, PreviousReleaseID: "previous-release",
	}); err != nil {
		t.Fatal(err)
	}
	_ = j.Close()
	if err := discardUnreferencedWork(root); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"current-release", "previous-release"} {
		if _, err := os.Stat(filepath.Join(root, releasesDirName, id)); err != nil {
			t.Fatalf("preserved %s: %v", id, err)
		}
	}
	for _, path := range []string{
		filepath.Join(root, releasesDirName, "orphan-release"),
		filepath.Join(root, "staging"), filepath.Join(root, "downloads"),
	} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("orphan path %s still exists: %v", path, err)
		}
	}
}

func TestDiscardPreMarkerWorkRetriesSuccessfulOperationCleanup(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{
		filepath.Join(root, releasesDirName, "current-release"),
		filepath.Join(root, releasesDirName, "previous-release"),
		filepath.Join(root, "downloads", "successful-op"),
		filepath.Join(root, backupsDirName),
	} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	writePointer(t, root, "current-release")
	for _, path := range []string{
		backupsDumpPath(root, "bk-success"), backupsDumpPath(root, "bk-success") + ".sha256",
	} {
		if err := os.WriteFile(path, []byte("artifact"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	j, err := embeddedupdate.Open(filepath.Join(root, journalFileName))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.Create(embeddedupdate.Record{
		OperationID: "successful-op", Channel: "embedded", Step: embeddedupdate.StepSucceeded,
		TargetReleaseID: "current-release", CurrentReleaseID: "current-release",
		PreviousReleaseID: "previous-release", BackupID: "bk-success",
	}); err != nil {
		t.Fatal(err)
	}
	_ = j.Close()

	if err := discardPreMarkerWork(root); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		filepath.Join(root, releasesDirName, "previous-release"),
		filepath.Join(root, "downloads"),
		backupsDumpPath(root, "bk-success"), backupsDumpPath(root, "bk-success") + ".sha256",
	} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("successful startup cleanup left %s: %v", path, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, releasesDirName, "current-release")); err != nil {
		t.Fatalf("startup cleanup removed current release: %v", err)
	}
}

func TestDiscardUnreferencedWorkFailsClosedOnCorruptPointer(t *testing.T) {
	root := t.TempDir()
	orphan := filepath.Join(root, releasesDirName, "orphan")
	if err := os.MkdirAll(orphan, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, currentPointerName), []byte("../invalid"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := discardUnreferencedWork(root); err == nil {
		t.Fatal("cleanup accepted corrupt current pointer")
	}
	if _, err := os.Stat(orphan); err != nil {
		t.Fatalf("cleanup removed release despite corrupt pointer: %v", err)
	}
}
