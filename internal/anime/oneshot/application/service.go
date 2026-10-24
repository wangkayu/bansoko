// Package application implements finite anime one-shot acquisition, replay,
// recovery, and read operations independently of root anime ownership.
package application

import (
	"context"
	"database/sql"
	"errors"

	acquisitiontarget "bansoko/internal/acquisition/target"
	animeapplication "bansoko/internal/anime/application"
	draftapplication "bansoko/internal/anime/draft/application"
	animeport "bansoko/internal/anime/port"
	"bansoko/internal/events"
	facts "bansoko/internal/release/facts"
	"bansoko/internal/workitems"
)

// DraftRevisionReader exposes only the draft operations required by one-shot
// admissions. The candidate-revision error methods keep the owning package's
// error vocabulary out of this use-case package.
type DraftRevisionReader interface {
	GetAnimeConfigCandidate(ctx context.Context, candidateID string) (draftapplication.AnimeConfigCandidate, error)
	ReadVerifiedAnimeConfigCandidateRevision(ctx context.Context, candidateID string, revision int64) (animeapplication.CandidateRevision, error)
	LoadCandidateRevisionTx(ctx context.Context, tx *sql.Tx, candidateID string, revision int64) (animeapplication.CandidateRevision, string, error)
	CandidateRevisionRequiredError() error
	CandidateRevisionMismatchError(current, requested int64) error
	CandidateNotWorkingError() error
	CandidateDigestMismatchError() error
}

// Dependencies are the explicit collaborators used by one-shot admission and
// recovery. CandidateRevisions is implemented by
// internal/anime/draft/application.Service.
type Dependencies struct {
	WorkItems          *workitems.Store
	OneShotMarkers     animeport.OneShotMarkerRepository
	Provisioner        *animeapplication.Provisioner
	TorrentFetcher     animeapplication.TorrentPayloadFetcher
	SeedPreviews       animeapplication.SeedPreviewSource
	CandidateRevisions DraftRevisionReader
	EventHub           *events.Hub
	TargetProbe        acquisitiontarget.Probe
	TargetStore        acquisitiontarget.Store
}

type invalidationPublisher struct {
	hub *events.Hub
}

func (publisher invalidationPublisher) publish(targets ...string) {
	if publisher.hub == nil {
		return
	}
	for _, target := range targets {
		publisher.hub.Publish(events.Event{Target: target})
	}
}

// Service owns one-shot workflows only. Root anime owns the candidate draft
// APIs and shared owner provisioning; this service stores only their narrow
// ports and never embeds the root service.
type Service struct {
	db                          *sql.DB
	workItems                   *workitems.Store
	oneShotMarkers              animeport.OneShotMarkerRepository
	provisioner                 *animeapplication.Provisioner
	torrentFetcher              animeapplication.TorrentPayloadFetcher
	seedPreviews                animeapplication.SeedPreviewSource
	candidateRevisions          DraftRevisionReader
	events                      invalidationPublisher
	targetProbe                 acquisitiontarget.Probe
	targetStore                 acquisitiontarget.Store
	targetAcquisitionConfigured bool
	// mediaFacts writes the Release media fact snapshot at admission.
	mediaFacts facts.MediaRepository
}

// NewService validates and creates the one-shot application service. The work
// item store, marker repository, owner provisioner, and draft revision reader
// are mandatory for a complete finite acquisition path.
func NewService(db *sql.DB, deps Dependencies) (*Service, error) {
	if db == nil {
		return nil, errors.New("one-shot service database is nil")
	}
	if deps.WorkItems == nil {
		return nil, errors.New("one-shot service work item store is nil")
	}
	if deps.OneShotMarkers == nil {
		return nil, errors.New("one-shot marker repository is not configured")
	}
	if deps.Provisioner == nil {
		return nil, errors.New("one-shot owner provisioner is not configured")
	}
	if deps.CandidateRevisions == nil {
		return nil, errors.New("one-shot draft revision reader is not configured")
	}
	targetConfigured := deps.TargetProbe != nil && deps.TargetStore.Configured()
	return &Service{
		db:                          db,
		workItems:                   deps.WorkItems,
		oneShotMarkers:              deps.OneShotMarkers,
		provisioner:                 deps.Provisioner,
		torrentFetcher:              deps.TorrentFetcher,
		seedPreviews:                deps.SeedPreviews,
		candidateRevisions:          deps.CandidateRevisions,
		events:                      invalidationPublisher{hub: deps.EventHub},
		targetProbe:                 deps.TargetProbe,
		targetStore:                 deps.TargetStore,
		targetAcquisitionConfigured: targetConfigured,
		mediaFacts:                  facts.NewStore(db),
	}, nil
}

func (s *Service) candidateRevisionRequiredError() error {
	return s.candidateRevisions.CandidateRevisionRequiredError()
}

func (s *Service) candidateRevisionMismatchError(current, requested int64) error {
	return s.candidateRevisions.CandidateRevisionMismatchError(current, requested)
}

func (s *Service) candidateNotWorkingError() error {
	return s.candidateRevisions.CandidateNotWorkingError()
}

func (s *Service) candidateDigestMismatchError() error {
	return s.candidateRevisions.CandidateDigestMismatchError()
}
