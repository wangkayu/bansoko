package target

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"bansoko/internal/apperrors"
)

var ErrInstanceNotFound = errors.New("target scan: managed anime not found")

// ErrInstanceRetired is the typed retirement boundary of the observation scan:
// a retired instance's owner graph is preserved but admits no new write, so the
// scan persists nothing and the HTTP boundary returns 409 MANAGED_ANIME_RETIRED.
var ErrInstanceRetired = &apperrors.Business{
	Status:  409,
	Code:    "MANAGED_ANIME_RETIRED",
	Message: "The managed anime is retired and admits no new work.",
}

// Scanner performs the narrow core observation contract: it reads only the
// frozen targets belonging to one existing instance, probes those paths, and
// replaces the latest observation rows. It never walks a root, hashes files,
// creates placements, or reopens Release/effect owners. Its only caller is the
// reconciliation scan run (RefreshObservations); there is no separate HTTP
// target scan.
type Scanner struct {
	Store Store
	Probe Probe
}

// NewScanner wires an observation scanner to its target store and filesystem
// probe. The store owns SQL, database handles, and transaction lifecycle.
func NewScanner(store Store, probe Probe) Scanner {
	return Scanner{Store: store, Probe: probe}
}

func (s Scanner) ScanSubscription(ctx context.Context, managedAnimeID string) ([]Observation, error) {
	if s.Probe == nil {
		return nil, errors.New("target scan: database and probe are required")
	}
	managedAnimeID = strings.TrimSpace(managedAnimeID)
	if managedAnimeID == "" {
		return nil, ErrInstanceNotFound
	}
	targets, exists, err := s.Store.ReadTargets(ctx, managedAnimeID)
	if err != nil {
		return nil, fmt.Errorf("target scan: read targets: %w", err)
	}
	if !exists {
		return nil, ErrInstanceNotFound
	}
	if len(targets) == 0 {
		return []Observation{}, nil
	}

	observations := make([]Observation, 0, len(targets))
	targetIDs := make([]string, 0, len(targets))
	for _, value := range targets {
		observation := s.Probe.Observe(Spec{ID: value.ID, TargetPath: value.TargetPath, ExpectedSizeBytes: value.ExpectedSizeBytes})
		observations = append(observations, observation)
		targetIDs = append(targetIDs, value.ID)
	}
	if err := s.Store.PersistObservations(ctx, observations); err != nil {
		return nil, err
	}
	versions, err := s.Store.ReadObservationVersions(ctx, targetIDs)
	if err != nil {
		return nil, fmt.Errorf("target scan: read observation versions: %w", err)
	}
	for index := range observations {
		observations[index].Version = versions[observations[index].TargetID]
	}
	return observations, nil
}

// RefreshObservations is the target pass of one reconciliation scan run: it
// stats the instance's frozen targets and replaces their latest observation
// rows, so the RSS row status reads the same run's file facts. A missing or
// retired instance has nothing to refresh and is not an error for the scan.
func (s Scanner) RefreshObservations(ctx context.Context, managedAnimeID string) error {
	_, err := s.ScanSubscription(ctx, managedAnimeID)
	if errors.Is(err, ErrInstanceNotFound) || errors.Is(err, ErrInstanceRetired) {
		return nil
	}
	return err
}
