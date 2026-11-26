package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"bansoko/internal/download/domain"
	"bansoko/internal/download/qbittorrent"
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/workitems"
)

var errPartialDownloadCancelled = errors.New("partial download has a pending cancellation")

func (s *Service) observePartialDownload(ctx context.Context, claim workitems.Claim, task taskSnapshot, correlation providerCorrelation, input frozenInput, session string, ordinal int) (domain.ProviderTask, error) {
	observer, ok := s.provider.(qbittorrent.PartialDownloadObserver)
	if !ok || input.PathSnapshot == nil || input.PartialNamespace == "" || claim.OneShot == nil ||
		correlation.ExternalID != "tag:bansoko-"+claim.IdempotencyKey {
		return domain.ProviderTask{}, qbittorrent.ErrPartialDownloadIdentity
	}
	manifest, err := partialDownloadManifest(input)
	if err != nil {
		return domain.ProviderTask{}, err
	}
	return observer.ObservePartialDownload(ctx, qbittorrent.PartialDownloadRequest{
		TaskID: correlation.ExternalID, Hashes: frozenInfoHashCandidates(input), Namespace: input.PartialNamespace,
		Snapshot: *input.PathSnapshot, Selection: *input.PartialDownload, Manifest: manifest, StartSent: input.PartialStartSent,
		BeforeWrite: func(ctx context.Context, start bool) (time.Duration, error) {
			var gate struct {
				Granted     bool  `json:"granted"`
				RemainingMS int64 `json:"remaining_ms"`
			}
			began := time.Now()
			err := s.workItems.WithTypedStageTransaction(ctx, claim, func(scope txscope.Tx) (workitems.EffectDisposition, error) {
				var raw []byte
				if err := scope.SQLTx().QueryRowContext(ctx, `SELECT download_authorize_partial_write($1,$2,$3,$4,$5,$6)`, task.id, claim.ID, claim.FencingToken, session, ordinal, start).Scan(&raw); err != nil {
					return "", err
				}
				return workitems.DispositionAmbiguous, json.Unmarshal(raw, &gate)
			})
			if err != nil {
				return 0, fmt.Errorf("authorize partial download: %w", err)
			}
			if !gate.Granted {
				return 0, errPartialDownloadCancelled
			}
			return time.Duration(gate.RemainingMS)*time.Millisecond - time.Since(began), nil
		},
	})
}

func partialDownloadManifest(input frozenInput) (map[string]int64, error) {
	selection := input.PartialDownload
	if selection == nil || selection.Validate() != nil {
		return nil, qbittorrent.ErrPartialDownloadIdentity
	}
	var files []json.RawMessage
	if err := json.Unmarshal(input.ReleaseFiles, &files); err != nil || len(files) != selection.FileCount {
		return nil, qbittorrent.ErrPartialDownloadIdentity
	}
	manifest := make(map[string]int64, len(files))
	for _, raw := range files {
		var file struct {
			Path string `json:"source_path"`
			Size *int64 `json:"size_bytes"`
		}
		if err := json.Unmarshal(raw, &file); err != nil || file.Path == "" || file.Size == nil || *file.Size < 0 {
			return nil, qbittorrent.ErrPartialDownloadIdentity
		}
		if _, exists := manifest[file.Path]; exists {
			return nil, qbittorrent.ErrPartialDownloadIdentity
		}
		manifest[file.Path] = *file.Size
	}
	for _, path := range selection.Paths {
		if _, exists := manifest[path]; !exists {
			return nil, qbittorrent.ErrPartialDownloadIdentity
		}
	}
	return manifest, nil
}

func partialSubmissionTask(observed domain.ProviderTask, input frozenInput) domain.ProviderTask {
	if input.PartialDownload == nil {
		return observed
	}
	// Submission proves ownership, not file priorities or download completion.
	// Both add receipts and recovery observations must go through the sync poll.
	return domain.ProviderTask{ID: observed.ID, Hash: strings.ToLower(observed.Hash), Name: observed.Name,
		State: domain.TaskDownloading, PathSnapshot: observed.PathSnapshot}
}
