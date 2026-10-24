// Package target owns the frozen per-entry TargetSet contract. It is pure and
// deliberately does not inspect the filesystem or create a DownloadTask.
package target

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"bansoko/internal/library/domain"
)

type Intent string

const (
	IntentEnsurePresent Intent = "ensure_present"
	IntentReplace       Intent = "replace"
)

type Entry struct {
	TargetRef         string
	SourcePath        string
	MediaKind         string
	TargetPath        string
	ExpectedSizeBytes *int64
	Intent            Intent
	CollisionPolicy   domain.CollisionPolicy
	RenderSnapshot    json.RawMessage
	ConfigSnapshot    json.RawMessage
}

type Spec struct {
	ID                string                 `json:"id"`
	ReleaseID         string                 `json:"release_id"`
	TargetRef         string                 `json:"target_ref"`
	SourcePath        string                 `json:"source_path"`
	MediaKind         string                 `json:"media_kind"`
	TargetPath        string                 `json:"target_path"`
	ExpectedSizeBytes int64                  `json:"expected_size_bytes"`
	Intent            Intent                 `json:"intent"`
	CollisionPolicy   domain.CollisionPolicy `json:"collision_policy"`
	RenderSnapshot    json.RawMessage        `json:"render_snapshot"`
	ConfigSnapshot    json.RawMessage        `json:"config_snapshot"`
	SpecDigest        string                 `json:"spec_digest"`
}

type Set struct {
	ReleaseID string `json:"release_id"`
	Targets   []Spec `json:"targets"`
	Digest    string `json:"digest"`
}

var (
	ErrEmptyRelease       = errors.New("target planner: release id is required")
	ErrNoTargets          = errors.New("target planner: at least one target is required")
	ErrUnknownSize        = errors.New("target planner: exact per-entry size is required")
	ErrDuplicateTargetRef = errors.New("target planner: duplicate target reference")
	ErrDuplicateTarget    = errors.New("target planner: duplicate rendered target path")
	ErrInvalidPath        = errors.New("target planner: path is not canonical")
)

func Plan(releaseID string, entries []Entry) (Set, error) {
	releaseID = strings.TrimSpace(releaseID)
	if releaseID == "" {
		return Set{}, ErrEmptyRelease
	}
	if len(entries) == 0 {
		return Set{}, ErrNoTargets
	}
	ordered := append([]Entry(nil), entries...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].TargetRef < ordered[j].TargetRef })
	seenRefs := make(map[string]struct{}, len(ordered))
	seenPaths := make(map[string]struct{}, len(ordered))
	targets := make([]Spec, 0, len(ordered))
	for index, entry := range ordered {
		ref := strings.TrimSpace(entry.TargetRef)
		if ref == "" {
			return Set{}, fmt.Errorf("target %d: target reference is required", index)
		}
		if _, exists := seenRefs[ref]; exists {
			return Set{}, fmt.Errorf("%w: %s", ErrDuplicateTargetRef, ref)
		}
		seenRefs[ref] = struct{}{}
		sourcePath, err := canonicalRelative(entry.SourcePath)
		if err != nil {
			return Set{}, fmt.Errorf("target %s source path: %w", ref, err)
		}
		targetPath, err := canonicalAbsolute(entry.TargetPath)
		if err != nil {
			return Set{}, fmt.Errorf("target %s target path: %w", ref, err)
		}
		if _, exists := seenPaths[targetPath]; exists {
			return Set{}, fmt.Errorf("%w: %s", ErrDuplicateTarget, targetPath)
		}
		seenPaths[targetPath] = struct{}{}
		if entry.ExpectedSizeBytes == nil || *entry.ExpectedSizeBytes < 0 {
			return Set{}, fmt.Errorf("target %s: %w", ref, ErrUnknownSize)
		}
		if entry.MediaKind != "video" && entry.MediaKind != "subtitle" && entry.MediaKind != "other" {
			return Set{}, fmt.Errorf("target %s: invalid media kind %q", ref, entry.MediaKind)
		}
		if entry.Intent != IntentEnsurePresent && entry.Intent != IntentReplace {
			return Set{}, fmt.Errorf("target %s: invalid intent %q", ref, entry.Intent)
		}
		if entry.CollisionPolicy != domain.CollisionBlock && entry.CollisionPolicy != domain.CollisionVersion && entry.CollisionPolicy != domain.CollisionOverwrite {
			return Set{}, fmt.Errorf("target %s: invalid collision policy %q", ref, entry.CollisionPolicy)
		}
		render := normalizeObject(entry.RenderSnapshot)
		config := normalizeObject(entry.ConfigSnapshot)
		spec := Spec{
			ReleaseID: releaseID,
			TargetRef: ref, SourcePath: sourcePath, MediaKind: entry.MediaKind,
			TargetPath: targetPath, ExpectedSizeBytes: *entry.ExpectedSizeBytes,
			Intent: entry.Intent, CollisionPolicy: entry.CollisionPolicy,
			RenderSnapshot: render, ConfigSnapshot: config,
		}
		spec.SpecDigest = digest(spec)
		// IDs are deterministic for a Release/entry so a retry can replay the
		// same target row without creating another owner identity.
		spec.ID = "target-" + shortDigest(digest(struct {
			ReleaseID string
			TargetRef string
		}{releaseID, ref}))
		targets = append(targets, spec)
	}
	set := Set{ReleaseID: releaseID, Targets: targets}
	set.Digest = digest(set)
	return set, nil
}

func canonicalRelative(value string) (string, error) {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	if value == "" || strings.HasPrefix(value, "/") || strings.ContainsAny(value, "\x00\r\n") {
		return "", ErrInvalidPath
	}
	clean := filepath.ToSlash(filepath.Clean(value))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", ErrInvalidPath
	}
	return clean, nil
}

func canonicalAbsolute(value string) (string, error) {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	if value == "" || !strings.HasPrefix(value, "/") || strings.ContainsAny(value, "\x00\r\n") {
		return "", ErrInvalidPath
	}
	clean := filepath.ToSlash(filepath.Clean(value))
	if clean == "/" || strings.HasPrefix(clean, "/../") || clean == "/.." {
		return "", ErrInvalidPath
	}
	return clean, nil
}

func normalizeObject(value json.RawMessage) json.RawMessage {
	if len(value) == 0 || strings.TrimSpace(string(value)) == "null" {
		return json.RawMessage(`{}`)
	}
	var object map[string]any
	if json.Unmarshal(value, &object) != nil || object == nil {
		return json.RawMessage(`{}`)
	}
	result, _ := json.Marshal(object)
	return result
}

func digest(value any) string {
	raw, _ := json.Marshal(value)
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func shortDigest(value string) string {
	value = strings.TrimPrefix(value, "sha256:")
	if len(value) > 32 {
		return value[:32]
	}
	return value
}
