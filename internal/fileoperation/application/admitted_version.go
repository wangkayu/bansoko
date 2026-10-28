package application

import "bansoko/internal/batchplan"

// recordTargetVersion copies the version number the frozen batch plan gives a
// video (batchplan.File.TargetVersion: admission settled same-episode copies
// of a manual one-shot selection on one target under the versioned policy).
// The naming template alone renders every copy to one name; the number makes
// the re-render land on the target admission froze.
func recordTargetVersion(prep *prepareContext, selection fileSelection, file artifactFileContext) {
	if prep.plan == nil {
		return
	}
	path, ok := selection.manifestPath(file.relativePath)
	if !ok {
		return
	}
	entry, ok := prep.plan.File(path)
	if !ok || entry.TargetVersion < 2 {
		return
	}
	if prep.targetVersions == nil {
		prep.targetVersions = map[string]int{}
	}
	prep.targetVersions[file.fileRef] = entry.TargetVersion
}

// versionedRenderedTarget applies a recorded version number to a rendered target.
func versionedRenderedTarget(prep prepareContext, file artifactFileContext, rendered string) string {
	return batchplan.VersionedTargetPath(rendered, prep.targetVersions[file.fileRef])
}
