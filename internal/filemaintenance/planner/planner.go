// Package planner owns deterministic snapshot-only FileMaintenance replanning.
package planner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"bansoko/internal/episodetitle"
	"bansoko/internal/postprocessing/naming"
)

// SnapshotObservation is one immutable reconciliation observation fact the
// planner consumes. It is provided through the SnapshotReader port — the
// planner never enumerates, hashes or re-scans the library.
type SnapshotObservation struct {
	AnimeID     string
	PlacementID string
	FilePath    string
	Fingerprint string
	SizeBytes   int64
	Coverage    []string // canonical coverage keys (P3.2 facts contract)
	Resolution  string
	Group       string
	Language    string
}

// SnapshotReader is the P3.2/P3.1 snapshot contract port. Composition wires
// the reconciliation + candidatefacts adapters.
type SnapshotReader interface {
	// GenerationUsable reports whether the generation is completed and
	// immutable (partial/incomplete/failed/stale are never consumable).
	GenerationUsable(ctx context.Context, animeID string, generation int64) (bool, error)
	// ListObservations pages the immutable observations of one generation.
	ListObservations(ctx context.Context, animeID string, generation int64) ([]SnapshotObservation, error)
}

// PlanCandidate is one dry-run candidate fact set. The plan keeps ALL
// candidate facts and coverage plus exclusion reasons — it never
// pre-filters winners, so P3.5 can distinguish all/per_group/single_copy
// admission semantics.
type PlanCandidate struct {
	ObservationRef    string   `json:"observation_ref"`
	SourcePath        string   `json:"source_path"`
	Fingerprint       string   `json:"fingerprint"`
	SizeBytes         int64    `json:"size_bytes"`
	Coverage          []string `json:"coverage"`
	TargetPath        string   `json:"target_path,omitempty"`
	Excluded          bool     `json:"excluded,omitempty"`
	ExcludeReason     string   `json:"exclude_reason,omitempty"`
	ExcludeReasonCode string   `json:"exclude_reason_code,omitempty"`
}

// PlanResult is the deterministic dry-run output for one anime/generation.
type PlanResult struct {
	AnimeID          string          `json:"anime_id"`
	Generation       int64           `json:"generation"`
	PolicyVersion    int64           `json:"policy_version"`
	InputFingerprint string          `json:"input_fingerprint"`
	Candidates       []PlanCandidate `json:"candidates"`
}

// Planner is the pure snapshot-only replan planner. It renders target paths
// from frozen observations and the naming contract via the TargetRenderer
// port (composition wires the naming renderer's pure computation).
type Planner struct {
	snapshots SnapshotReader
	renderer  TargetRenderer
}

// TargetRenderer renders the frozen target path for one observation without
// any filesystem I/O.
type TargetRenderer interface {
	RenderTarget(ctx context.Context, observation SnapshotObservation) (string, error)
}

// NewPlanner wires the planner ports.
func NewPlanner(snapshots SnapshotReader, renderer TargetRenderer) *Planner {
	return &Planner{snapshots: snapshots, renderer: renderer}
}

// Plan computes the deterministic dry-run candidate set. It fails closed on
// non-completed generations, policy/resolver errors, or naming render
// failures — no executable operation is ever created from a partial or
// stale snapshot.
func (p *Planner) Plan(ctx context.Context, animeID string, generation int64, policyVersion int64) (*PlanResult, error) {
	if p.snapshots == nil {
		return nil, fmt.Errorf("planner snapshot reader is not wired")
	}
	if p.renderer == nil {
		return nil, fmt.Errorf("planner target renderer is not wired")
	}
	usable, err := p.snapshots.GenerationUsable(ctx, animeID, generation)
	if err != nil {
		return nil, err
	}
	if !usable {
		return nil, fmt.Errorf("snapshot generation %d for %s is not completed/immutable; replan blocked", generation, animeID)
	}
	observations, err := p.snapshots.ListObservations(ctx, animeID, generation)
	if err != nil {
		return nil, err
	}
	result := &PlanResult{
		AnimeID:          animeID,
		Generation:       generation,
		PolicyVersion:    policyVersion,
		InputFingerprint: planFingerprint(animeID, generation, policyVersion, observations),
		Candidates:       make([]PlanCandidate, 0, len(observations)),
	}
	for _, observation := range observations {
		candidate := PlanCandidate{
			ObservationRef: fmt.Sprintf("%s:%d:%s", animeID, generation, observation.FilePath),
			SourcePath:     observation.FilePath,
			Fingerprint:    observation.Fingerprint,
			SizeBytes:      observation.SizeBytes,
			Coverage:       observation.Coverage,
		}
		// Coverage must be resolvable and non-empty, or the candidate is
		// excluded from automatic settlement (never guessed).
		if len(observation.Coverage) == 0 || (len(observation.Coverage) == 1 && observation.Coverage[0] == "unresolved") {
			candidate.Excluded = true
			candidate.ExcludeReason = "coverage unresolved"
			candidate.ExcludeReasonCode = "coverage_unresolved"
			result.Candidates = append(result.Candidates, candidate)
			continue
		}
		target, err := p.renderer.RenderTarget(ctx, observation)
		if err != nil {
			candidate.Excluded = true
			candidate.ExcludeReason = fmt.Sprintf("target render failed: %v", err)
			candidate.ExcludeReasonCode = "target_render_failed"
			if errors.Is(err, episodetitle.ErrTransient) {
				// The alignment render is on the episode-title whitelist: a
				// provider lookup that failed transiently excludes the
				// candidate with its own typed reason (the owner re-runs the
				// alignment later; nothing is placed on a guessed path).
				candidate.ExcludeReason = "episode title lookup failed"
				candidate.ExcludeReasonCode = "episode_title_lookup_failed"
			} else if errors.Is(err, naming.ErrRenderFactsUnavailable) {
				candidate.ExcludeReason = "render facts unavailable"
				candidate.ExcludeReasonCode = "render_facts_unavailable"
			}
		} else {
			candidate.TargetPath = target
		}
		result.Candidates = append(result.Candidates, candidate)
	}
	sort.SliceStable(result.Candidates, func(i, j int) bool {
		return result.Candidates[i].ObservationRef < result.Candidates[j].ObservationRef
	})
	return result, nil
}

// planFingerprint is the deterministic input identity of one plan.
func planFingerprint(animeID string, generation, policyVersion int64, observations []SnapshotObservation) string {
	hash := sha256.New()
	_, _ = fmt.Fprintf(hash, "%s|%d|%d", animeID, generation, policyVersion)
	for _, observation := range observations {
		coverage, _ := json.Marshal(observation.Coverage)
		_, _ = fmt.Fprintf(hash, "|%s|%s|%d|%s", observation.FilePath, observation.Fingerprint, observation.SizeBytes, coverage)
	}
	return hex.EncodeToString(hash.Sum(nil))
}
