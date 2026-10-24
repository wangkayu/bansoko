// Package deploymentprovenance stores the deployment-owned facts that cannot
// be recovered from a release payload alone. In particular, the producing
// operation id is journal evidence rather than release-content evidence, so it
// is retained beside the immutable release tree and written crash-safely.
package deploymentprovenance

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	FormatVersion = 1
	Directory     = "provenance"
	maxFileBytes  = 16 << 10
)

var (
	sha256DigestRE = regexp.MustCompile(`^[0-9a-f]{64}$`)
	idRE           = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
)

// Record is the durable identity proof for one promoted-capable release.
// Unlike release-metadata.json, this file is deployment-owned and includes the
// update operation that produced the release tree.
type Record struct {
	SchemaVersion          int    `json:"schema_version"`
	ReleaseID              string `json:"release_id"`
	PayloadDigest          string `json:"payload_digest"`
	ProducingOperationID   string `json:"producing_operation_id"`
	ExpectedProcessBuildID string `json:"expected_process_build_id"`
}

func Path(updatesRoot, releaseID string) string {
	return filepath.Join(updatesRoot, Directory, releaseID+".json")
}

// Validate checks both the record shape and its binding to the independently
// verified release facts. Empty expected values skip that comparison, which is
// useful for callers that first validate the record shape.
func (r Record) Validate(expectedReleaseID, expectedPayloadDigest, expectedBuildID string) error {
	if r.SchemaVersion != FormatVersion {
		return fmt.Errorf("unsupported deployment provenance schema version %d", r.SchemaVersion)
	}
	if !idRE.MatchString(strings.TrimSpace(r.ReleaseID)) {
		return errors.New("deployment provenance requires a bounded release id")
	}
	if !sha256DigestRE.MatchString(strings.TrimSpace(r.PayloadDigest)) {
		return errors.New("deployment provenance requires a lowercase sha256 payload digest")
	}
	if !idRE.MatchString(strings.TrimSpace(r.ProducingOperationID)) {
		return errors.New("deployment provenance requires a bounded producing operation id")
	}
	if !idRE.MatchString(strings.TrimSpace(r.ExpectedProcessBuildID)) {
		return errors.New("deployment provenance requires a bounded expected process build id")
	}
	if expectedReleaseID != "" && r.ReleaseID != expectedReleaseID {
		return fmt.Errorf("deployment provenance release id %q differs from expected %q", r.ReleaseID, expectedReleaseID)
	}
	if expectedPayloadDigest != "" && r.PayloadDigest != expectedPayloadDigest {
		return fmt.Errorf("deployment provenance payload digest differs from expected release digest")
	}
	if expectedBuildID != "" && r.ExpectedProcessBuildID != expectedBuildID {
		return fmt.Errorf("deployment provenance build id %q differs from expected %q", r.ExpectedProcessBuildID, expectedBuildID)
	}
	return nil
}

// Write atomically publishes one provenance record. The temporary file is
// synced before rename and the containing directory is synced afterwards, so a
// crash cannot turn a complete release into an apparently valid partial file.
func Write(updatesRoot string, record Record) error {
	if err := record.Validate("", "", ""); err != nil {
		return err
	}
	dir := filepath.Join(updatesRoot, Directory)
	// The provenance directory is operation-owned deployment evidence inside the
	// updates volume: it is created symlink-safely, so a planted link cannot
	// redirect the write outside the volume.
	if err := ensureProvenanceDir(updatesRoot, dir); err != nil {
		return err
	}
	target := Path(updatesRoot, record.ReleaseID)
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return fmt.Errorf("encode deployment provenance: %w", err)
	}
	data = append(data, '\n')
	temporary, err := os.CreateTemp(dir, ".provenance-*.tmp")
	if err != nil {
		return fmt.Errorf("create deployment provenance temp: %w", err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("protect deployment provenance temp: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write deployment provenance temp: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync deployment provenance temp: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close deployment provenance temp: %w", err)
	}
	if err := os.Rename(temporaryName, target); err != nil {
		return fmt.Errorf("publish deployment provenance: %w", err)
	}
	dirHandle, dirErr := os.Open(dir)
	if dirErr != nil {
		return fmt.Errorf("open deployment provenance directory after publish: %w", dirErr)
	}
	if syncErr := dirHandle.Sync(); syncErr != nil {
		_ = dirHandle.Close()
		return fmt.Errorf("sync deployment provenance directory: %w", syncErr)
	}
	if closeErr := dirHandle.Close(); closeErr != nil {
		return fmt.Errorf("close deployment provenance directory: %w", closeErr)
	}
	return nil
}

// ensureProvenanceDir creates the provenance directory (and the updates root if
// it is missing) without ever traversing a symlink, and verifies that every
// component is a real directory inside the updates root. It is implemented here
// rather than imported from the updater so this package keeps its own small
// dependency set.
func ensureProvenanceDir(updatesRoot, dir string) error {
	root := filepath.Clean(updatesRoot)
	dir = filepath.Clean(dir)
	rel, err := filepath.Rel(root, dir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("deployment provenance directory %s is outside the updates root", dir)
	}
	current := root
	if _, statErr := os.Lstat(current); statErr != nil {
		if mkErr := os.Mkdir(current, 0o700); mkErr != nil {
			return fmt.Errorf("create updates root: %w", mkErr)
		}
	}
	if err := requireRealDir(root); err != nil {
		return fmt.Errorf("updates root is not usable: %w", err)
	}
	if rel == "." {
		return nil
	}
	for _, component := range strings.Split(filepath.ToSlash(rel), "/") {
		if component == "" || component == "." {
			continue
		}
		current = filepath.Join(current, component)
		_, statErr := os.Lstat(current)
		switch {
		case os.IsNotExist(statErr):
			if mkErr := os.Mkdir(current, 0o700); mkErr != nil {
				return fmt.Errorf("create deployment provenance directory: %w", mkErr)
			}
			if dirErr := requireRealDir(current); dirErr != nil {
				return dirErr
			}
		case statErr != nil:
			return statErr
		default:
			if dirErr := requireRealDir(current); dirErr != nil {
				return dirErr
			}
		}
	}
	return nil
}

// requireRealDir refuses anything that is not a real directory: a symlink (even
// one pointing at a directory) would let a provenance write escape the updates
// root.
func requireRealDir(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s is a symlink; refusing to write through it", path)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", path)
	}
	return nil
}

// WriteIfMatching is the idempotent publication primitive used after a
// release tree has been fully verified. A pre-existing equal record is valid;
// a different record for the same release is evidence of a deployment-volume
// conflict and is rejected.
func WriteIfMatching(updatesRoot string, record Record) error {
	if existing, err := Read(updatesRoot, record.ReleaseID); err == nil {
		if existing == record {
			return nil
		}
		return errors.New("deployment provenance already exists with different identity")
	} else if !errors.Is(err, os.ErrNotExist) {
		// Only a genuinely absent record may be replaced. Malformed or otherwise
		// unreadable evidence fails closed.
		return err
	}
	return Write(updatesRoot, record)
}

// Read returns a strictly decoded provenance record. The caller must verify
// the signed release tree before trusting the values returned here.
func Read(updatesRoot, releaseID string) (Record, error) {
	if !idRE.MatchString(strings.TrimSpace(releaseID)) {
		return Record{}, errors.New("deployment provenance release id is invalid")
	}
	path := Path(updatesRoot, releaseID)
	info, err := os.Lstat(path)
	if err != nil {
		return Record{}, fmt.Errorf("read deployment provenance: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return Record{}, errors.New("deployment provenance is not a regular file")
	}
	if info.Size() > maxFileBytes {
		return Record{}, errors.New("deployment provenance exceeds the bounded size")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Record{}, fmt.Errorf("read deployment provenance: %w", err)
	}
	var record Record
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return Record{}, fmt.Errorf("deployment provenance is invalid: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return Record{}, errors.New("deployment provenance contains trailing JSON data")
		}
		return Record{}, fmt.Errorf("decode trailing deployment provenance data: %w", err)
	}
	if err := record.Validate(releaseID, "", ""); err != nil {
		return Record{}, err
	}
	return record, nil
}
