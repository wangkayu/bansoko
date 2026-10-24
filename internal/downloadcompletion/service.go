// Package downloadcompletion carries out the download_completion policy:
// what happens to the qBittorrent task after an acquisition succeeded and all
// its file operations are terminal.
//
// A pass turns each successful download acquisition into one owner row that
// freezes the action accepted for its release (seed, pause, remove or
// remove_with_data, and for seed its optional share limits) together with the
// qBittorrent identity of the task. A seed row without limits is already
// settled; seed limits are written to qBittorrent once and the product lets go. Any other action is carried by one retryable
// completion_execute WorkItem, whose result is written back to the row and
// shown as the completion stage of the acquisition detail. Only a succeeded
// acquisition acts; a failed one keeps its task so a retry can adopt it again.
//
// The product never deletes a file itself: remove_with_data asks qBittorrent
// to delete the task's own files after CheckRemoveWithData has proved no
// library file lies inside them.
package downloadcompletion

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"bansoko/internal/batchplan"
	"bansoko/internal/download/qbittorrent"
	"bansoko/internal/events"
	"bansoko/internal/release/facts"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
)

// SnapshotReader reads a release's latest frozen media fact snapshot.
type SnapshotReader interface {
	ReadSnapshot(ctx context.Context, releaseID string) (*facts.Snapshot, error)
}

// Service is the completion pass, the WorkItem handler and the retry command.
type Service struct {
	store    *Store
	snapshot SnapshotReader
	executor Executor
	hub      *events.Hub
	batch    int
}

// NewService wires the service. qb may be nil in a deployment without one; the
// action then retries until it exists.
func NewService(store *Store, snapshot SnapshotReader, qb QB) (*Service, error) {
	if store == nil || store.DB == nil || store.Work == nil {
		return nil, errors.New("download completion requires a store with a database and a work item store")
	}
	if snapshot == nil {
		return nil, errors.New("download completion requires a media fact snapshot reader")
	}
	return &Service{
		store: store, snapshot: snapshot, batch: 50,
		executor: Executor{QB: qb, Library: libraryAdapter{store: store}, Sharing: store},
	}, nil
}

// SetEventHub wires the realtime refresh after a completion settles.
func (s *Service) SetEventHub(hub *events.Hub) {
	if s != nil {
		s.hub = hub
	}
}

type libraryAdapter struct {
	store *Store
}

func (l libraryAdapter) EachLibraryPath(ctx context.Context, visit func(string) error) error {
	return l.store.EachLibraryPath(ctx, visit)
}

// PassResult counts one pass.
type PassResult struct {
	Created int
}

// RunPass writes the completion row of every successful download acquisition
// that has none, freezing the action its release was accepted under. It is
// idempotent and restart-safe: the row is the marker.
func (s *Service) RunPass(ctx context.Context) (PassResult, error) {
	var result PassResult
	candidates, err := s.store.listCandidates(ctx, s.batch)
	if err != nil {
		return result, err
	}
	for _, c := range candidates {
		action, limits, err := s.frozenAction(ctx, c.ReleaseID)
		if err != nil {
			return result, fmt.Errorf("read frozen completion action of %s: %w", c.AcquisitionID, err)
		}
		created, err := s.store.create(ctx, c.AcquisitionID, action, limits, factsOf(c))
		if err != nil {
			return result, err
		}
		if created {
			result.Created++
		}
	}
	if result.Created > 0 && s.hub != nil {
		s.hub.Publish(events.Event{Target: events.TargetRecords})
	}
	return result, nil
}

// frozenAction is the action, with the seed action's limits, in the batch
// plan frozen when the release was accepted. A release without a snapshot or
// plan reads as seed without limits: no action. A plan frozen before seed
// limits existed has none and reads the same way.
func (s *Service) frozenAction(ctx context.Context, releaseID string) (string, *batchplan.SeedLimits, error) {
	snapshot, err := s.snapshot.ReadSnapshot(ctx, releaseID)
	if err != nil {
		return "", nil, err
	}
	if snapshot == nil || snapshot.ReleaseFacts.BatchPlan == nil {
		return batchplan.CompletionSeed, nil, nil
	}
	plan := snapshot.ReleaseFacts.BatchPlan
	return plan.CompletionAction(), plan.CompletionSeedLimits(), nil
}

// Handler is the completion_execute WorkItem handler.
func (s *Service) Handler() worker.Handler { return handler{service: s} }

type handler struct{ service *Service }

func (h handler) Handle(ctx context.Context, claim workitems.Claim) (worker.Outcome, error) {
	s := h.service
	if claim.Kind != workitems.KindCompletionExecute || claim.Owner.Type != workitems.OwnerDownloadCompletion {
		return worker.Outcome{}, fmt.Errorf("download completion handler received kind %q", claim.Kind)
	}
	row, err := s.store.ByID(ctx, claim.Owner.ID)
	if err != nil {
		return worker.Outcome{}, err
	}
	if row == nil {
		return worker.Finish(workitems.StateFailed, workitems.OwnerResult{State: StateFailed, ReasonCode: "completion_owner_missing", Reason: "completion does not exist"}, nil), nil
	}
	if row.State != StatePending {
		// Settled elsewhere (a replay after the row was already settled): the
		// work item follows its owner and nothing runs again.
		terminal := workitems.StateSucceeded
		if row.State == StateFailed {
			terminal = workitems.StateFailed
		}
		return worker.Finish(terminal, workitems.OwnerResult{State: row.State, ReasonCode: row.ResultCode}, settledWriter(row)), nil
	}
	if row.Version != claim.ExpectedOwnerVersion {
		return worker.Finish(workitems.StateFailed, workitems.OwnerResult{State: StateFailed, ReasonCode: "completion_fencing_stale", Reason: "stale claim cannot settle the completion"}, nil), nil
	}

	outcome := s.executor.Run(ctx, row.completion())
	switch {
	case outcome.Success:
		return s.published(worker.Finish(workitems.StateSucceeded,
			workitems.OwnerResult{State: StateSucceeded, ReasonCode: outcome.Code}, settleWriter(row, StateSucceeded, outcome.Code, outcome.Detail))), nil
	case outcome.Retryable:
		delay := time.Duration(claim.Attempt) * 10 * time.Second
		return s.published(worker.Retry(outcome.Code, boundedReason(outcome.Detail).String, delay, retryWriter(row, outcome.Code, outcome.Detail))), nil
	default:
		return s.published(worker.Finish(workitems.StateFailed,
			workitems.OwnerResult{State: StateFailed, ReasonCode: outcome.Code}, settleWriter(row, StateFailed, outcome.Code, outcome.Detail))), nil
	}
}

func (s *Service) published(outcome worker.Outcome) worker.Outcome {
	outcome.AfterCommit = s.hub.AfterCommit(events.TargetRecords, outcome.AfterCommit)
	return outcome
}

// TorrentClient is the qBittorrent client surface the adapter needs; both
// qbittorrent.ConfiguredClient (production, current typed configuration) and
// qbittorrent.Client (a fixed endpoint) provide it.
type TorrentClient interface {
	TorrentByHash(ctx context.Context, hash string) (qbittorrent.TorrentFacts, bool, error)
	ListTorrents(ctx context.Context) ([]qbittorrent.TorrentInfo, error)
	GetTorrentFiles(ctx context.Context, hash string) ([]qbittorrent.TorrentFile, error)
	PauseTorrent(ctx context.Context, hash string) error
	DeleteTorrent(ctx context.Context, hash string, deleteFiles bool) error
	WebAPIVersion(ctx context.Context) (string, error)
	SetShareLimits(ctx context.Context, hash string, limits qbittorrent.ShareLimits) error
}

var (
	_ TorrentClient = qbittorrent.ConfiguredClient{}
	_ TorrentClient = qbittorrent.Client{}
)

// TorrentAdapter adapts the qBittorrent client to the QB port.
type TorrentAdapter struct {
	Client TorrentClient
}

// Torrent reads one task and its complete file list by infohash.
func (a TorrentAdapter) Torrent(ctx context.Context, hash string) (Torrent, bool, error) {
	facts, found, err := a.Client.TorrentByHash(ctx, hash)
	if err != nil || !found {
		return Torrent{}, false, err
	}
	return Torrent{Hash: facts.Hash, Name: facts.Name, State: facts.State, SavePath: facts.SavePath, ContentPath: facts.ContentPath, FileNames: facts.FileNames}, true, nil
}

// Others lists every task but the given one. Where a task's content path is its
// save path (files directly in the save folder) its complete file list is read
// too, so overlap can be judged file by file.
func (a TorrentAdapter) Others(ctx context.Context, hash string) ([]Torrent, error) {
	listed, err := a.Client.ListTorrents(ctx)
	if err != nil {
		return nil, err
	}
	var others []Torrent
	for _, item := range listed {
		if strings.EqualFold(strings.TrimSpace(item.Hash), strings.TrimSpace(hash)) {
			continue
		}
		other := Torrent{Hash: item.Hash, Name: item.Name, State: item.State, SavePath: item.SavePath, ContentPath: item.ContentPath}
		if filepath.Clean(strings.TrimSpace(item.ContentPath)) == filepath.Clean(strings.TrimSpace(item.SavePath)) {
			files, err := a.Client.GetTorrentFiles(ctx, item.Hash)
			if err != nil {
				return nil, err
			}
			for _, file := range files {
				other.FileNames = append(other.FileNames, file.Name)
			}
		}
		others = append(others, other)
	}
	return others, nil
}

// Pause pauses one task by infohash.
func (a TorrentAdapter) Pause(ctx context.Context, hash string) error {
	return a.Client.PauseTorrent(ctx, hash)
}

// Delete removes one task by infohash, with its files when deleteFiles is true.
func (a TorrentAdapter) Delete(ctx context.Context, hash string, deleteFiles bool) error {
	return a.Client.DeleteTorrent(ctx, hash, deleteFiles)
}

// WebAPIVersion reads the qBittorrent WebAPI version.
func (a TorrentAdapter) WebAPIVersion(ctx context.Context) (string, error) {
	return a.Client.WebAPIVersion(ctx)
}

// SetShareLimits writes one task's share limits by infohash.
func (a TorrentAdapter) SetShareLimits(ctx context.Context, hash string, limits ShareLimitsRequest) error {
	return a.Client.SetShareLimits(ctx, hash, qbittorrent.ShareLimits{
		RatioLimit: limits.RatioLimit, SeedingTimeLimit: limits.SeedingTimeLimit, InactiveSeedingTimeLimit: limits.InactiveSeedingTimeLimit,
		ShareLimitAction: limits.Action, ShareLimitsMode: limits.Mode,
	})
}
