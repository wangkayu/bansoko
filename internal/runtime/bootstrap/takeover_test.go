package bootstrap

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bansoko/internal/runtime/buildinfo"
	"bansoko/internal/runtime/deploymentprovenance"
	updatefence "bansoko/internal/update/fence"
	embeddedupdate "bansoko/internal/update/journal"
)

func takeoverTestRecord() embeddedupdate.Record {
	build := func(id string, sequence int64) buildinfo.BuildInfo {
		return buildinfo.BuildInfo{BuildID: id, Version: "1.0.0", Commit: strings.Repeat("c", 40), SchemaVersion: "001_baseline",
			APIVersion: "v2", BaseCompatibilityVersion: "base-v1",
			Order: buildinfo.Order{Series: buildinfo.CISeries, Lane: "stable", ReleaseSequence: sequence}}
	}
	a := embeddedupdate.BaseAnchor{Platform: "linux", Architecture: "amd64",
		ExecutableHash: strings.Repeat("b", 64), Build: build("base", 2)}
	return embeddedupdate.Record{OperationID: "takeover-op", Channel: updatefence.AdmittedByEmbedded,
		Step: embeddedupdate.StepPrepared, OperationKind: embeddedupdate.KindBaseTakeover,
		TargetReleaseID: a.Build.BuildID, TargetVersion: a.Build.Version, TargetSchemaVersion: a.Build.SchemaVersion,
		TargetLane: a.Build.Lane, TargetReleaseSequence: a.Build.ReleaseSequence, ManifestDigest: a.Fingerprint(),
		PreviousReleaseID: "overlay", CurrentReleaseID: "overlay", RollbackSourceCaptured: true,
		Takeover: &embeddedupdate.TakeoverEvidence{TargetSource: "base", PreviousSource: "overlay", Anchor: a,
			Previous: embeddedupdate.PreviousOverlay{ReleaseID: "overlay", ProducingOp: "overlay-op", Build: build("overlay", 1), PayloadDigest: "sha256:" + strings.Repeat("d", 64)}}}
}

func TestTakeoverIntentCannotBeMutatedThroughSharedPointer(t *testing.T) {
	j, err := embeddedupdate.Open(filepath.Join(t.TempDir(), journalFileName))
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	r, err := j.Create(takeoverTestRecord())
	if err != nil {
		t.Fatal(err)
	}
	_, err = j.AdvanceExpected(r.OperationID, r.Version, r.Step, embeddedupdate.StepAccepted, "", "", func(next *embeddedupdate.Record) {
		next.Takeover.Previous.ProducingOp = "foreign-op"
	})
	if !errors.Is(err, embeddedupdate.ErrInvalidTransition) {
		t.Fatalf("mutating takeover intent: got %v, want invalid transition", err)
	}
	saved, found, err := j.Load()
	if err != nil || !found || saved.Version != r.Version || *saved.Takeover != *r.Takeover {
		t.Fatalf("refused advance modified durable intent: found=%v, err=%v", found, err)
	}
}

func TestTakeoverCheckpoints(t *testing.T) {
	for _, tt := range []struct {
		name       string
		existing   bool
		failure    string
		want       embeddedupdate.Step
		wantEvents string
	}{
		{"success", false, "", embeddedupdate.StepSucceeded, "acquire backup migrate old-pointer-probe unlink base-probe close:succeeded retire"},
		{"fence insert replay", true, "", embeddedupdate.StepSucceeded, "backup migrate old-pointer-probe unlink base-probe close:succeeded retire"},
		{"backup refusal", false, "backup", embeddedupdate.StepFailed, "acquire backup close:failed"},
		{"migration failure", false, "migrate", embeddedupdate.StepRolledBack, "acquire backup migrate prepare-restore rename restore-pointer overlay-probe close:failed"},
		{"base probe failure", false, "base-probe", embeddedupdate.StepRolledBack, "acquire backup migrate old-pointer-probe unlink base-probe prepare-restore rename restore-pointer overlay-probe close:failed"},
		{"restore refusal", false, "rename", embeddedupdate.StepRollbackBlocked, "acquire backup migrate prepare-restore rename"},
		{"closure replay", false, "close:succeeded", embeddedupdate.StepSucceeded, "acquire backup migrate old-pointer-probe unlink base-probe close:succeeded base-probe close:succeeded retire"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			j, err := embeddedupdate.Open(filepath.Join(root, journalFileName))
			if err != nil {
				t.Fatal(err)
			}
			defer j.Close()
			r, err := j.Create(takeoverTestRecord())
			if err != nil {
				t.Fatal(err)
			}
			var events []string
			var row updatefence.Row
			found := tt.existing
			row = updatefence.Row{OperationID: r.OperationID, AdmittedBy: r.Channel, Sequence: r.TargetReleaseSequence, ManifestDigest: r.ManifestDigest, State: "active"}
			failure := tt.failure
			effect := func(name string) error {
				events = append(events, name)
				if failure == name || failure == "rename" && name == "migrate" {
					return errors.New("forced failure")
				}
				return nil
			}
			fx := &takeoverFX{
				VerifyEvidence: func(embeddedupdate.Record) error { return nil },
				LookupFence:    func(string) (updatefence.Row, bool, error) { return row, found, nil },
				AcquireFence:   func(embeddedupdate.Record) error { found = true; return effect("acquire") },
				CaptureBackup: func(embeddedupdate.Record) (string, string, error) {
					return strings.Repeat("e", 64), "install", effect("backup")
				},
				CheckMigration: func(embeddedupdate.Record) error { return nil },
				Migrate:        func(embeddedupdate.Record) error { return effect("migrate") },
				Probe: func(_ embeddedupdate.Record, source string, oldPointer bool) error {
					if oldPointer {
						return effect("old-pointer-probe")
					}
					return effect(source + "-probe")
				},
				DropCurrent:    func(string) error { return effect("unlink") },
				RestoreCurrent: func(string) error { return effect("restore-pointer") },
				VerifyOverlay:  func(embeddedupdate.Record) error { return nil },
				PrepareRecovery: func(embeddedupdate.Record) (embeddedupdate.DatabaseRecovery, error) {
					return embeddedupdate.DatabaseRecovery{Live: "db", Ready: "ready", Aside: "aside", LiveOID: 1, ReadyOID: 2, OwnerOID: 3}, effect("prepare-restore")
				},
				ReconcileRecovery: func(r embeddedupdate.Record) error {
					pinned, _, err := j.Load()
					if err != nil || pinned.DatabaseRecovery == nil || *pinned.DatabaseRecovery != *r.DatabaseRecovery {
						t.Fatal("rename attempted without durable database identities")
					}
					return effect("rename")
				},
				SettleExact: func(r embeddedupdate.Record, terminal string) error {
					if r.Step != embeddedupdate.StepFenceClosurePending || r.FenceClosureTarget == "" {
						t.Fatal("close attempted without durable intent")
					}
					return effect("close:" + terminal)
				},
				Retire: func(embeddedupdate.Record) error { return effect("retire") },
			}
			out := runTakeover(j, r, fx, root)
			if tt.failure == "close:succeeded" {
				saved, _, err := j.Load()
				if err != nil || saved.Step != embeddedupdate.StepFenceClosurePending || out.kind != outcomeParked {
					t.Fatalf("expected replayable closure, got %s, %v", saved.Step, err)
				}
				failure = ""
				out = runTakeover(j, saved, fx, root)
			}
			saved, _, err := j.Load()
			if err != nil || saved.Step != tt.want || (out.kind == outcomeParked) != (tt.want == embeddedupdate.StepRollbackBlocked) {
				t.Fatalf("got step %s, outcome %+v, err %v; want step %s", saved.Step, out, err, tt.want)
			}
			if got := strings.Join(events, " "); got != tt.wantEvents {
				t.Fatalf("effects = %q; want %q", got, tt.wantEvents)
			}
			if tt.want == embeddedupdate.StepFailed || tt.want == embeddedupdate.StepRolledBack {
				if err := j.ArchiveTerminalProven(); err != nil {
					t.Fatal(err)
				}
				for _, variant := range []string{"exact", "new-binary", "new-overlay"} {
					t.Run(variant, func(t *testing.T) {
						anchor, prev := r.Takeover.Anchor, r.Takeover.Previous
						switch variant {
						case "new-binary":
							anchor.ExecutableHash = strings.Repeat("f", 64)
						case "new-overlay":
							prev.ProducingOp = "replacement-op"
						}
						prior, err := takeoverOutcome(j, embeddedupdate.Record{}, false, anchor, prev)
						if err != nil || (prior.OperationID == r.OperationID) != (variant == "exact") {
							t.Fatalf("archived %s evidence returned %q: %v", variant, prior.OperationID, err)
						}
					})
				}
			}
		})
	}
}

func TestRetirementReplayAndRetention(t *testing.T) {
	root := t.TempDir()
	intent := takeoverTestRecord()
	releaseID := intent.PreviousReleaseID
	tree := writeReleaseTreeWithOrder(t, root, releaseID, intent.Takeover.Previous.Build.Order)
	seedReleaseProvenance(t, root, releaseID)
	proof, err := verifyOverlayIdentity(root, releaseID)
	if err != nil {
		t.Fatal(err)
	}
	intent.Takeover.Previous = previousOverlay(releaseID, proof)
	j, err := embeddedupdate.Open(filepath.Join(root, journalFileName))
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	r, err := j.Create(intent)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range []embeddedupdate.Step{
		embeddedupdate.StepAccepted, embeddedupdate.StepMaintenanceEntered, embeddedupdate.StepBackupVerified,
		embeddedupdate.StepPendingMigration, embeddedupdate.StepMigrationStarted, embeddedupdate.StepMigrationSucceeded,
		embeddedupdate.StepBaseReady, embeddedupdate.StepPromoted, embeddedupdate.StepFenceClosurePending, embeddedupdate.StepSucceeded,
	} {
		r, err = j.AdvanceExpected(r.OperationID, r.Version, r.Step, step, "", "", func(next *embeddedupdate.Record) {
			switch step {
			case embeddedupdate.StepBackupVerified:
				next.BackupID, next.BackupDigest, next.InstallID = "embedded-"+r.OperationID, strings.Repeat("e", 64), "install"
			case embeddedupdate.StepPromoted:
				next.CurrentReleaseID = ""
			case embeddedupdate.StepFenceClosurePending:
				next.FenceClosureTarget = embeddedupdate.StepSucceeded
			case embeddedupdate.StepSucceeded:
				next.FenceClosure = embeddedupdate.FenceClosureClosed
			}
		})
		if err != nil {
			t.Fatalf("advance to %s: %v", step, err)
		}
	}
	retired := filepath.Join(root, "retired", r.OperationID)
	if err := os.MkdirAll(releaseRoot(retired), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tree, relDirOf(retired, releaseID)); err != nil {
		t.Fatal(err)
	}
	if err := retireOverlay(root, r); err != nil {
		t.Fatalf("resume split retirement: %v", err)
	}
	if _, err := os.Stat(deploymentprovenance.Path(retired, releaseID)); err != nil {
		t.Fatal(err)
	}
	if err := j.ArchiveTerminalProven(); err != nil {
		t.Fatal(err)
	}
	complete := filepath.Join(retired, "complete")
	info, err := os.Stat(complete)
	if err != nil {
		t.Fatal(err)
	}
	expires := info.ModTime().Add(retiredRetention)
	for _, tt := range []struct {
		name    string
		now     time.Time
		live    embeddedupdate.Record
		pointer bool
		gone    bool
	}{
		{name: "completion not old enough", now: expires.Add(-time.Second)},
		{name: "active operation", now: expires, live: embeddedupdate.Record{Step: embeddedupdate.StepPrepared}},
		{name: "blocked operation", now: expires, live: embeddedupdate.Record{Step: embeddedupdate.StepRollbackBlocked}},
		{name: "live release reference", now: expires, live: embeddedupdate.Record{Step: embeddedupdate.StepSucceeded, TargetReleaseID: releaseID}},
		{name: "current pointer", now: expires, pointer: true},
		{name: "expired unreferenced", now: expires, gone: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if tt.pointer {
				if err := writeCurrentPointerAtomically(root, releaseID); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := os.Remove(filepath.Join(root, "current")); err != nil {
						t.Error(err)
					}
				})
			}
			if err := maintainRetirement(root, j, tt.live, tt.live.Step != "", tt.now); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(retired); tt.gone && !errors.Is(err, os.ErrNotExist) || !tt.gone && err != nil {
				t.Fatalf("retirement existence: gone=%v stat=%v", tt.gone, err)
			}
		})
	}
}

// The takeover intent is only valid when the previous overlay's own producing
// operation is proven closed in the database. Volume evidence alone is not a
// trust root: a self-consistent tree, provenance file and journal must still be
// backed by the exact settled fence row.
func TestTakeoverIntentRequiresTheClosedProducingFence(t *testing.T) {
	root := t.TempDir()
	releaseID := "fixture-overlay"
	writeReleaseTreeWithOrder(t, root, releaseID, buildinfo.Order{Series: buildinfo.CISeries, Lane: "stable", ReleaseSequence: 2})
	seedReleaseProvenance(t, root, releaseID)
	proof, err := verifyOverlayIdentity(root, releaseID)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := proof.Manifest.CanonicalDigest()
	if err != nil {
		t.Fatal(err)
	}
	exact := updatefence.Row{OperationID: proof.OperationID, AdmittedBy: updatefence.AdmittedByEmbedded,
		Sequence: proof.Manifest.ReleaseSequence, ManifestDigest: digest, State: "settled", Released: true}
	for _, tc := range []struct {
		name   string
		row    updatefence.Row
		found  bool
		lookup error
	}{
		{"exact", exact, true, nil},
		{"missing", updatefence.Row{}, false, nil},
		{"lookup failure", exact, false, errors.New("database unavailable")},
		{"still open", func() updatefence.Row { r := exact; r.Released = false; return r }(), true, nil},
		{"failed", func() updatefence.Row { r := exact; r.State = "failed"; return r }(), true, nil},
		{"foreign owner", func() updatefence.Row { r := exact; r.AdmittedBy = "overlay"; return r }(), true, nil},
		{"other operation", func() updatefence.Row { r := exact; r.OperationID = "other-op"; return r }(), true, nil},
		{"other sequence", func() updatefence.Row { r := exact; r.Sequence = 1; return r }(), true, nil},
		{"other digest", func() updatefence.Row { r := exact; r.ManifestDigest = strings.Repeat("a", 64); return r }(), true, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lookup := func(_ context.Context, operationID string) (updatefence.Row, bool, error) {
				if operationID != proof.OperationID {
					t.Fatalf("lookup operation = %q, want %q", operationID, proof.OperationID)
				}
				return tc.row, tc.found, tc.lookup
			}
			err := proveProducingFence(context.Background(), lookup, proof)
			if (err == nil) != (tc.name == "exact") {
				t.Fatalf("proveProducingFence = %v, want accepted=%v", err, tc.name == "exact")
			}
		})
	}
}
