package application

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"bansoko/internal/download/admission"
	"bansoko/internal/download/domain"
	"bansoko/internal/download/qbittorrent"
)

func (s *Service) MatchAdmission(ctx context.Context, request admission.Request) (admission.Result, error) {
	if s == nil || s.provider == nil || s.runtimeConfig == nil || strings.TrimSpace(request.OperationKey) == "" {
		return admission.Result{}, admission.ErrInvalidRequest
	}
	hashes := request.HashCandidates()
	if len(hashes) == 0 {
		return admission.Result{}, fmt.Errorf("%w: a frozen torrent hash is required", admission.ErrInvalidRequest)
	}
	config, err := s.runtimeConfig.GetQBittorrentRuntimeConfig(ctx)
	if err != nil {
		return admission.Result{}, fmt.Errorf("resolve qB admission configuration: %w", err)
	}
	snapshot, err := qbittorrent.DerivePathSnapshot(config)
	if err != nil || strings.TrimSpace(snapshot.RemoteRoot) == "" || strings.TrimSpace(snapshot.ApplicationRoot) == "" {
		return admission.Result{}, fmt.Errorf("%w: qB path snapshot is unavailable", admission.ErrInvalidRequest)
	}
	lookup, ok := s.provider.(interface {
		LookupByOperationKeyWithSnapshot(context.Context, string, domain.PathSnapshot) (domain.ProviderTask, bool, error)
		LookupByInfoHashesWithSnapshot(context.Context, []string, domain.PathSnapshot) (domain.ProviderTask, bool, error)
	})
	if !ok {
		return admission.Result{}, fmt.Errorf("%w: qB provider does not support frozen admission lookup", admission.ErrInvalidRequest)
	}
	task, found, err := lookup.LookupByOperationKeyWithSnapshot(ctx, request.OperationKey, snapshot)
	if err != nil {
		return admission.Result{}, classifyAdmissionLookupError(err)
	}
	if found {
		if err := validateReplayTask(task, hashes, snapshot); err != nil {
			return admission.Result{}, err
		}
		return admission.Result{PathSnapshot: snapshot}, nil
	}
	task, found, err = lookup.LookupByInfoHashesWithSnapshot(ctx, hashes, snapshot)
	if err != nil {
		return admission.Result{}, classifyAdmissionLookupError(err)
	}
	if !found {
		return admission.Result{PathSnapshot: snapshot}, nil
	}
	if err := validateAdoptionTask(task, hashes, snapshot); err != nil {
		return admission.Result{}, err
	}
	return adoptedAdmissionResult(request.OperationKey, snapshot, task), nil
}

func classifyAdmissionLookupError(err error) error {
	if errors.Is(err, qbittorrent.ErrLookupAmbiguous) || strings.Contains(strings.ToLower(err.Error()), "multiple qbittorrent tasks") {
		return fmt.Errorf("%w: %v", admission.ErrAmbiguousMatch, err)
	}
	return err
}

func validateTaskHash(task domain.ProviderTask, hashes []string) error {
	providerHash := strings.ToLower(strings.TrimSpace(task.Hash))
	for _, hash := range hashes {
		if providerHash == hash || (len(providerHash) == 40 && len(hash) == 64 && providerHash == hash[:40]) {
			return nil
		}
	}
	return fmt.Errorf("%w: provider task has a different torrent hash", admission.ErrConflictingMatch)
}

// validateReplayTask keeps the independent rules for a B-local operation-key
// hit: the stable tag is the identity, so the frozen snapshot echo, save root
// and in-root bounds stay as they are for replay.
func validateReplayTask(task domain.ProviderTask, hashes []string, snapshot domain.PathSnapshot) error {
	if err := validateTaskHash(task, hashes); err != nil {
		return err
	}
	if task.PathSnapshot == nil || !qbittorrent.PathSnapshotsEqual(*task.PathSnapshot, snapshot) {
		return fmt.Errorf("%w: provider path snapshot differs from frozen admission", admission.ErrConflictingMatch)
	}
	savePath := strings.TrimSpace(task.SavePath)
	if savePath == "" || filepath.Clean(savePath) != filepath.Clean(snapshot.RemoteRoot) {
		return fmt.Errorf("%w: provider save root differs from frozen admission", admission.ErrConflictingMatch)
	}
	roots := []string{filepath.Clean(snapshot.RemoteRoot), filepath.Clean(snapshot.ApplicationRoot)}
	for _, candidate := range append(append([]string(nil), task.Paths...), task.Path) {
		normalized, err := qbittorrent.NormalizeEvidencePath(candidate)
		if err != nil {
			return fmt.Errorf("%w: provider content path is unsafe: %v", admission.ErrConflictingMatch, err)
		}
		if normalized == "" {
			continue
		}
		if !pathWithinRoots(normalized, roots) {
			return fmt.Errorf("%w: provider content is outside the frozen save roots", admission.ErrConflictingMatch)
		}
	}
	return nil
}

// validateAdoptionTask gates the cross-owner hash fallback on real provider
// evidence. The client's own PathSnapshot echo is deliberately not evidence;
// only qB-reported facts count: the save root must equal the frozen remote
// root, and at least one non-empty, normalized content path (torrents/info
// ContentPath and/or torrents/files paths) must sit inside the frozen roots.
// Missing evidence, out-of-root paths, traversal attempts or conflicting
// ContentPath/files facts fail closed; provider read errors already propagated
// from the lookup stay typed retryable.
func validateAdoptionTask(task domain.ProviderTask, hashes []string, snapshot domain.PathSnapshot) error {
	if err := validateTaskHash(task, hashes); err != nil {
		return err
	}
	savePath := strings.TrimSpace(task.SavePath)
	if savePath == "" || filepath.Clean(savePath) != filepath.Clean(snapshot.RemoteRoot) {
		return fmt.Errorf("%w: provider save root differs from frozen admission", admission.ErrConflictingMatch)
	}
	roots := []string{filepath.Clean(snapshot.RemoteRoot), filepath.Clean(snapshot.ApplicationRoot)}

	contentPath, err := qbittorrent.NormalizeEvidencePath(task.ContentPath)
	if err != nil {
		return fmt.Errorf("%w: provider content path is unsafe: %v", admission.ErrConflictingMatch, err)
	}
	evidence := make([]string, 0, len(task.Paths)+2)
	if contentPath != "" {
		evidence = append(evidence, contentPath)
	}
	filePaths := make([]string, 0, len(task.Paths))
	for _, candidate := range append(append([]string(nil), task.Paths...), task.Path) {
		normalized, err := qbittorrent.NormalizeEvidencePath(candidate)
		if err != nil {
			return fmt.Errorf("%w: provider content path is unsafe: %v", admission.ErrConflictingMatch, err)
		}
		if normalized == "" {
			continue
		}
		evidence = append(evidence, normalized)
		filePaths = append(filePaths, normalized)
	}
	for _, path := range evidence {
		if !pathWithinRoots(path, roots) {
			return fmt.Errorf("%w: provider content is outside the frozen save roots", admission.ErrConflictingMatch)
		}
	}
	if len(evidence) == 0 {
		return fmt.Errorf("%w: provider returned no content path evidence inside the frozen save roots", admission.ErrConflictingMatch)
	}
	if contentPath != "" && len(filePaths) > 0 {
		consistent := false
		for _, path := range filePaths {
			if path == contentPath || strings.HasPrefix(path, contentPath+string(filepath.Separator)) {
				consistent = true
				break
			}
		}
		if !consistent {
			return fmt.Errorf("%w: provider content path conflicts with its file paths", admission.ErrConflictingMatch)
		}
	}
	return nil
}

func pathWithinRoots(path string, roots []string) bool {
	for _, root := range roots {
		relative, err := filepath.Rel(root, path)
		if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative) {
			return true
		}
	}
	return false
}

func adoptedAdmissionResult(operationKey string, snapshot domain.PathSnapshot, task domain.ProviderTask) admission.Result {
	return admission.Result{PathSnapshot: snapshot, Provenance: &admission.Provenance{
		Kind: "unique_infohash_path", ProviderTaskID: task.ID, ProviderInfoHash: strings.ToLower(strings.TrimSpace(task.Hash)), OperationKeyChecked: operationKey,
	}}
}
