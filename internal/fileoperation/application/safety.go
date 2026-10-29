package application

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"unicode"

	librarydomain "bansoko/internal/library/domain"
	"bansoko/internal/postprocessing/naming"
	"bansoko/internal/redact"
)

const (
	ownerStatePending      = "pending"
	ownerStateRunning      = "running"
	ownerStateCompleted    = "completed"
	ownerStateWaitingRetry = "waiting_retry"
	ownerStateFailed       = "failed"
	rendererVersion        = "fileoperation-render-v1"
	invalidPlaceholderDir  = ".bansoko-invalid"
)

var (
	ErrActiveOperationConflict = errorString("an active file operation already exists for this artifact file")
)

func canonicalRootForPlan(root string) (string, error) {
	if strings.TrimSpace(root) == "" || !filepath.IsAbs(root) {
		return "", fmt.Errorf("%w: plan root must be absolute", ErrInvalidTarget)
	}
	root = filepath.Clean(root)
	info, err := os.Lstat(root)
	if err != nil {
		return "", fmt.Errorf("inspect plan root: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", fmt.Errorf("%w: plan root must be an existing non-symlink directory", ErrInvalidTarget)
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("resolve plan root: %w", err)
	}
	resolvedInfo, err := os.Lstat(resolved)
	if err != nil || resolvedInfo.Mode()&os.ModeSymlink != 0 || !resolvedInfo.IsDir() {
		return "", fmt.Errorf("%w: resolved plan root must be a directory", ErrInvalidTarget)
	}
	return filepath.Clean(resolved), nil
}

// stableReadRoot scopes descriptor observation to the filesystem root. Targets
// are absolute and the deployment owns the read/write boundary, so the
// observation namespace is the whole filesystem; there is no library root to
// bound it.
func stableReadRoot() string { return string(filepath.Separator) }

func placeholderRoot(_ string) string {
	if root, err := canonicalRootForPlan(os.TempDir()); err == nil {
		return root
	}
	return filepath.Clean(os.TempDir())
}

func validSourcePath(value string) bool {
	if strings.TrimSpace(value) == "" || len(value) > maxPathLength || !filepath.IsAbs(value) {
		return false
	}
	if strings.IndexByte(value, 0) >= 0 || filepath.Clean(value) != value {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// renderedTarget composes the final absolute target from the rendered
// directory and filename. The rendered directory must itself be absolute: the
// naming template carries its own deployment path, so there is no library
// anchor to resolve. The deployment owns the read/write boundary and the app
// does not authorize locations.
func renderedTarget(directory, filename string) (string, error) {
	if strings.TrimSpace(filename) == "" {
		return "", fmt.Errorf("%w: rendered filename is empty", ErrInvalidTarget)
	}
	if err := validateTargetComponent(directory, true); err != nil {
		return "", err
	}
	if err := validateTargetComponent(filename, false); err != nil {
		return "", err
	}
	dir := filepath.FromSlash(directory)
	if !filepath.IsAbs(dir) {
		return "", fmt.Errorf("%w: rendered directory must be absolute", ErrInvalidTarget)
	}
	return filepath.Join(dir, filename), nil
}

// validateTargetComponent enforces target hygiene on one rendered segment: no
// NUL, no control characters, no traversal, no home prefix. Absolute
// directories are allowed; filenames never carry a separator.
func validateTargetComponent(value string, directory bool) error {
	if value == "" && directory {
		return nil
	}
	if value == "" || strings.IndexByte(value, 0) >= 0 {
		return fmt.Errorf("%w: empty or NUL path component", ErrInvalidTarget)
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return fmt.Errorf("%w: path contains control characters", ErrInvalidTarget)
		}
	}
	if strings.HasPrefix(value, "~") {
		return fmt.Errorf("%w: path must not use a home-directory prefix", ErrInvalidTarget)
	}
	clean := filepath.Clean(filepath.FromSlash(value))
	if clean == "." && !directory || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%w: path contains traversal segments", ErrInvalidTarget)
	}
	if !directory && strings.ContainsAny(value, `/\\`) {
		return fmt.Errorf("%w: filename contains a directory separator", ErrInvalidTarget)
	}
	return nil
}

// validateTargetPath enforces hygiene on the final absolute target: absolute,
// canonical, NUL/control free, within the path length bound, and naming a
// file below the filesystem root. It authorizes no location.
func validateTargetPath(target string) error {
	if !filepath.IsAbs(target) || strings.IndexByte(target, 0) >= 0 || len(target) > maxPathLength {
		return fmt.Errorf("%w: target must be a well-formed absolute path", ErrInvalidTarget)
	}
	for _, r := range target {
		if unicode.IsControl(r) {
			return fmt.Errorf("%w: target contains control characters", ErrInvalidTarget)
		}
	}
	if filepath.Clean(target) != target {
		return fmt.Errorf("%w: target must be canonical", ErrInvalidTarget)
	}
	if target == string(filepath.Separator) {
		return fmt.Errorf("%w: target must name a file below the filesystem root", ErrInvalidTarget)
	}
	return nil
}

func isJSONObject(raw []byte) bool {
	if len(raw) == 0 {
		return false
	}
	var object map[string]any
	return json.Unmarshal(raw, &object) == nil && object != nil
}

// fingerprintFact is the decoded artifact file fingerprint document
// {"algorithm":"metadata","value":"meta:size=<n>[;dev=<d>;ino=<i>]",
// "size_bytes":<n>}: the path-level identity the inspection recorded. The
// content is never hashed.
type fingerprintFact struct {
	Algorithm string
	Value     string
	SizeBytes int64
	HasSize   bool
}

// fingerprintAlgorithmMetadata names the only artifact fingerprint form.
const fingerprintAlgorithmMetadata = "metadata"

func decodeFingerprint(raw []byte) (fingerprintFact, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		if err == nil {
			err = errors.New("fingerprint must be a JSON object")
		}
		return fingerprintFact{}, err
	}

	var fact fingerprintFact
	if err := json.Unmarshal(fields["algorithm"], &fact.Algorithm); err != nil || fact.Algorithm != fingerprintAlgorithmMetadata {
		return fingerprintFact{}, errors.New("fingerprint algorithm must be metadata")
	}
	if err := json.Unmarshal(fields["value"], &fact.Value); err != nil || len(fact.Value) > maxFingerprintLength {
		return fingerprintFact{}, errors.New("fingerprint value is invalid")
	}
	parsed, ok := librarydomain.ParseMetadataFingerprint(fact.Value)
	if !ok {
		return fingerprintFact{}, errors.New("fingerprint value is invalid")
	}
	var sizeValue *int64
	if err := json.Unmarshal(fields["size_bytes"], &sizeValue); err != nil || sizeValue == nil || *sizeValue < 0 || *sizeValue != parsed.SizeBytes {
		return fingerprintFact{}, errors.New("fingerprint size_bytes is invalid")
	}
	fact.SizeBytes = *sizeValue
	fact.HasSize = true
	return fact, nil
}

func fingerprintFactUsesSize(fact fingerprintFact) bool {
	return fact.HasSize
}

func marshalObject(value any) (json.RawMessage, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return raw, nil
}

func jsonEqual(left, right []byte) bool {
	var leftValue, rightValue any
	if json.Unmarshal(left, &leftValue) != nil || json.Unmarshal(right, &rightValue) != nil {
		return false
	}
	return reflect.DeepEqual(leftValue, rightValue)
}

func fingerprintValue(raw []byte) string {
	var value struct {
		Value string `json:"value"`
	}
	if json.Unmarshal(raw, &value) == nil {
		return strings.TrimSpace(value.Value)
	}
	return ""
}

// validDigestHex reports a 64-hex digest (the overwrite plan digest).
func validDigestHex(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) != 64 || strings.ContainsAny(value, "\r\n\x00") {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

// validObservationFingerprint reports a path-level metadata fingerprint.
func validObservationFingerprint(value string) bool {
	if len(value) > maxFingerprintLength {
		return false
	}
	_, ok := librarydomain.ParseMetadataFingerprint(value)
	return ok
}

// sameFileFingerprint is the path-level same-file rule over two metadata
// fingerprints: equal size, and equal inode when both recorded one.
func sameFileFingerprint(left, right string) bool {
	return librarydomain.MetadataFingerprintsMatch(left, right)
}

func stableBoundedKey(value string) string {
	if len(value) <= 512 {
		return value
	}
	digest := sha256.Sum256([]byte(value))
	return "file_execute:" + hex.EncodeToString(digest[:])
}

func maxVersion(value int64) int64 {
	if value < 1 {
		return 1
	}
	return value
}

func boundedCode(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	if len(value) > maxReasonCodeLength {
		return value[:maxReasonCodeLength]
	}
	return value
}

func failureCode(err error) string {
	if err == nil {
		return "file_operation_failed"
	}
	switch {
	case errors.Is(err, ErrOverwriteUnsupported):
		return "overwrite_unsupported"
	case errors.Is(err, ErrArtifactFingerprintMismatch):
		return "source_fingerprint_mismatch"
	case errors.Is(err, ErrArtifactVersionMismatch):
		return "artifact_version_mismatch"
	case errors.Is(err, ErrInvalidTarget):
		return "target_invalid"
	case errors.Is(err, ErrArtifactNotVerified):
		return "artifact_not_verified"
	case errors.Is(err, ErrSubtitleBindingMissing):
		return "subtitle_binding_missing"
	case errors.Is(err, ErrSubtitleBindingAmbiguous):
		return "subtitle_binding_ambiguous"
	case errors.Is(err, ErrBoundTargetMismatch):
		return "bound_target_mismatch"
	case errors.Is(err, ErrMediaFactsMissing):
		return "media_facts_missing"
	case errors.Is(err, ErrBatchPlanDrift):
		return "batch_plan_drift"
	default:
		// Naming engine errors carry their bounded product code (for example
		// SUBTITLE_SUFFIX_INVALID); other failures stay generic.
		var namingErr *naming.Error
		if errors.As(err, &namingErr) && strings.TrimSpace(namingErr.Code) != "" {
			return boundedCode(namingErr.ReasonCode(), "file_operation_failed")
		}
		return "file_operation_failed"
	}
}

func safeReason(err error) string {
	if err == nil {
		return "file operation failed"
	}
	value, _ := redact.Apply(err.Error(), maxReasonLength, redact.Mask, redact.Redacted)
	if value == "" {
		return "file operation failed"
	}
	return value
}

func isRetryable(err error) bool {
	if err == nil {
		return false
	}
	var retryable RetryableError
	if errors.As(err, &retryable) {
		return true
	}
	var retryablePointer *RetryableError
	if errors.As(err, &retryablePointer) {
		return true
	}
	var temporary interface{ Temporary() bool }
	if errors.As(err, &temporary) && temporary.Temporary() {
		return true
	}
	var networkError net.Error
	return errors.As(err, &networkError) && (networkError.Timeout() || networkError.Temporary())
}

func safeString(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	return safeReason(errors.New(value))
}

func jsonFieldString(raw []byte, field string) (string, error) {
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		return "", err
	}
	value, ok := object[field].(string)
	if !ok || strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("%s is missing", field)
	}
	return value, nil
}
