package bootstrap

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"bansoko/internal/runtime/deploymentidentity"
	"bansoko/internal/runtime/deploymentprovenance"
	embeddedupdate "bansoko/internal/update/journal"
)

const retiredRetention = 7 * 24 * time.Hour

// retireOverlay replays two same-filesystem renames. A split tree/provenance
// pair after a crash is verified in place before the remaining rename.
func retireOverlay(root string, r embeddedupdate.Record) error {
	if r.Takeover == nil || r.Step != embeddedupdate.StepSucceeded || r.FenceClosure != embeddedupdate.FenceClosureClosed {
		return errors.New("retirement requires a successful takeover with a closed fence")
	}
	if p := deploymentidentity.ReadCurrentPointer(root); p.Corrupt || p.Token == r.PreviousReleaseID {
		return errors.New("retirement cannot move the current release")
	}
	if !embeddedupdate.ValidID(r.OperationID) || !validReleaseToken(r.PreviousReleaseID) {
		return errors.New("invalid retirement path")
	}
	retired := filepath.Join(root, "retired", r.OperationID)
	for _, dir := range []string{filepath.Join(root, "retired"), retired, releaseRoot(retired), filepath.Join(retired, deploymentprovenance.Directory)} {
		if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		if err := requireRetirementDirectory(dir); err != nil {
			return err
		}
		if err := syncDirectory(filepath.Dir(dir)); err != nil {
			return err
		}
	}
	releaseID := r.PreviousReleaseID
	sourceTree, destTree := relDirOf(root, releaseID), relDirOf(retired, releaseID)
	tree, err := retirementLocation(sourceTree, destTree, true)
	if err != nil {
		return err
	}
	sourceProof, destProof := deploymentprovenance.Path(root, releaseID), deploymentprovenance.Path(retired, releaseID)
	proof, err := retirementLocation(sourceProof, destProof, false)
	if err != nil {
		return err
	}
	meta, manifest, err := verifyReleaseTreeIdentity(tree)
	pinned := r.Takeover.Previous
	if err != nil || meta.ReleaseID != releaseID || meta.BuildID != pinned.Build.BuildID ||
		"sha256:"+strings.TrimPrefix(manifest.EmbeddedUpdate.PayloadDigest, "sha256:") != pinned.PayloadDigest {
		return errors.New("retirement tree does not match the pinned release")
	}
	proofRoot := root
	if proof == destProof {
		proofRoot = retired
	}
	provenance, err := deploymentprovenance.Read(proofRoot, releaseID)
	if err != nil || provenance.Validate(releaseID, manifest.EmbeddedUpdate.PayloadDigest, meta.BuildID) != nil ||
		provenance.ProducingOperationID != pinned.ProducingOp {
		return errors.New("retirement provenance does not match the pinned release")
	}
	for _, pair := range [][3]string{{tree, sourceTree, destTree}, {proof, sourceProof, destProof}} {
		if pair[0] == pair[1] {
			if err := os.Rename(pair[1], pair[2]); err != nil {
				return err
			}
		}
		for _, path := range pair[1:] {
			if err := syncDirectory(filepath.Dir(path)); err != nil {
				return err
			}
		}
	}
	complete := filepath.Join(retired, "complete")
	if info, err := os.Lstat(complete); err == nil && info.Mode().IsRegular() {
		return syncDirectory(retired)
	} else if !errors.Is(err, os.ErrNotExist) {
		return errors.New("invalid retirement completion file")
	}
	return writeAtomicFile(retired, complete, []byte(r.ManifestDigest+"\n"), 0o600)
}

func requireRetirementDirectory(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() {
		return errors.New("retirement requires a real directory")
	}
	return nil
}

func retirementLocation(src, dst string, directory bool) (string, error) {
	location := ""
	for _, path := range []string{src, dst} {
		if err := requireRetirementDirectory(filepath.Dir(path)); err != nil {
			return "", err
		}
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || location != "" || (directory && !info.IsDir()) || (!directory && !info.Mode().IsRegular()) {
			return "", errors.New("retirement evidence has conflicting paths or types")
		}
		location = path
	}
	if location == "" {
		return "", errors.New("retirement evidence is missing")
	}
	return location, nil
}

// maintainRetirement runs under the journal lock on server startup. Only
// closed, archived successes may expire; a live journal or marker with
// unresolved references prevents collection altogether.
func maintainRetirement(root string, j *embeddedupdate.FileJournal, live embeddedupdate.Record, found bool, now time.Time) error {
	if found && (live.Active() || live.Step == embeddedupdate.StepRollbackBlocked) {
		return nil
	}
	if _, err := os.Lstat(filepath.Join(root, pendingMarkerName)); !errors.Is(err, os.ErrNotExist) {
		return nil
	}
	pointer := deploymentidentity.ReadCurrentPointer(root)
	if pointer.Corrupt {
		return nil
	}
	if found && live.Takeover != nil && live.Step == embeddedupdate.StepSucceeded && !pointer.Presented {
		if err := retireOverlay(root, live); err != nil {
			return err
		}
	}
	dir := filepath.Join(root, "retired")
	if _, err := os.Lstat(dir); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err := requireRetirementDirectory(dir); err != nil {
		return err
	}
	archive, err := j.ArchivedRecords()
	if err != nil {
		return err
	}
	for _, r := range archive {
		if r.Takeover == nil || r.Step != embeddedupdate.StepSucceeded || r.FenceClosure != embeddedupdate.FenceClosureClosed ||
			r.OperationID == live.OperationID || r.PreviousReleaseID == pointer.Token ||
			r.PreviousReleaseID == live.PreviousReleaseID || r.PreviousReleaseID == live.TargetReleaseID || r.PreviousReleaseID == live.CurrentReleaseID ||
			!embeddedupdate.ValidID(r.OperationID) || !validReleaseToken(r.PreviousReleaseID) {
			continue
		}
		path := filepath.Join(dir, r.OperationID)
		if err := requireRetirementDirectory(path); err != nil {
			continue
		}
		info, err := os.Lstat(filepath.Join(path, "complete"))
		if errors.Is(err, os.ErrNotExist) {
			if err := retireOverlay(root, r); err != nil {
				logf("archived overlay retirement deferred: %v", err)
			}
			continue
		}
		if err != nil || !info.Mode().IsRegular() || now.Before(r.UpdatedAt.Add(retiredRetention)) || now.Before(info.ModTime().Add(retiredRetention)) {
			continue
		}
		proof, err := verifyOverlayIdentity(path, r.PreviousReleaseID)
		if err != nil || previousOverlay(r.PreviousReleaseID, proof) != r.Takeover.Previous {
			continue
		}
		if err := os.RemoveAll(path); err != nil {
			return err
		}
		if err := syncDirectory(dir); err != nil {
			return err
		}
	}
	return nil
}

func reconcileRetirement(root string) error {
	if _, err := os.Lstat(root); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	j, err := embeddedupdate.Open(filepath.Join(root, journalFileName))
	if err != nil {
		return err
	}
	defer j.Close()
	r, found, err := j.Load()
	if err != nil {
		return err
	}
	return maintainRetirement(root, j, r, found, time.Now())
}
