package filemaintenance

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"

	"bansoko/internal/apperrors"
)

// RetentionContest is what a retention recheck left of a set of same-instance
// placements.
type RetentionContest struct {
	// Mode is the instance replacement mode the recheck read.
	Mode AdmissionMode
	// Superseded are the placements a settled recheck decision superseded;
	// their deletion was admitted to the retention cleanup in the same
	// transaction.
	Superseded []string
}

// RecheckPlacements puts the current coverage keys of the given placements
// back through retention, with the instance mode and the current scores. It
// is how a file whose key changed after its import (an episode offset change)
// or a file that meets a same-key file on its alignment target competes: on
// every such key the one placement that beats every other member of its pool
// is evaluated as the candidate and its replace decision settled, so the
// losers are superseded and deleted by the existing retention path. A key
// where no placement beats all the others (all mode, different per_group
// groups, a tie) keeps every file and freezes nothing.
//
// The evaluation is the ordinary EvaluateApplication with the placement's
// current keys (recheck: never bound to a pre-acquisition decision, the key's
// members in the digest), so a replay with the same pool converges on the
// decision already frozen. An instance whose subscription is not active
// returns the evaluate error: retention waits for resume.
func (s *ReplacementDecisionService) RecheckPlacements(ctx context.Context, animeID string, placementIDs []string) (RetentionContest, error) {
	snapshot, err := s.retentionSnapshot(ctx, animeID)
	if err != nil {
		return RetentionContest{}, err
	}
	contest := RetentionContest{Mode: snapshot.mode}
	if snapshot.mode == ModeAll || len(placementIDs) == 0 {
		return contest, nil
	}
	wanted := map[string]bool{}
	for _, id := range placementIDs {
		wanted[id] = true
	}
	keys := map[string]bool{}
	for _, placement := range snapshot.placements {
		if wanted[placement.PlacementID] {
			for _, key := range placement.Coverage {
				keys[key] = true
			}
		}
	}
	contenders := retentionContenders(snapshot.mode, snapshot.profile, snapshot.placements, keys)
	members := map[string]bool{}
	for _, placement := range snapshot.placements {
		for _, key := range placement.Coverage {
			if keys[key] {
				members[placement.PlacementID] = true
			}
		}
	}
	for _, contender := range contenders {
		releaseID, err := s.placementRelease(ctx, contender.placementID)
		if err != nil {
			return contest, err
		}
		if releaseID == "" {
			continue
		}
		units := make(map[string][]string, len(contender.keys))
		for _, key := range contender.keys {
			units[key] = contender.coverage
		}
		if _, err := s.EvaluateApplication(ctx, EvaluateRequest{
			AnimeID: animeID, CandidateRelease: releaseID,
			plannedCoverage: contender.keys, plannedUnits: units, recheck: true,
		}); err != nil {
			return contest, err
		}
		if err := s.SettleForPlacement(ctx, contender.placementID); err != nil {
			return contest, fmt.Errorf("settle retention recheck: %w", err)
		}
	}
	ids := make([]string, 0, len(members))
	for id := range members {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		var state string
		if err := s.db.QueryRowContext(ctx, `SELECT state FROM placements WHERE id = $1`, id).Scan(&state); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			return contest, err
		}
		if state == "superseded" {
			contest.Superseded = append(contest.Superseded, id)
		}
	}
	return contest, nil
}

// retentionContender is one placement that beats every other member of its
// pool on the listed keys.
type retentionContender struct {
	placementID string
	keys        []string
	coverage    []string
}

// retentionContenders finds, for every key, the placement whose evaluation
// as the candidate would replace the other members of its pool (the same
// pure pool evaluation an import runs). A pool has at most one such
// placement; a key whose pools have none keeps every file.
func retentionContenders(mode AdmissionMode, profile Profile, placements []PlacementFacts, keys map[string]bool) []retentionContender {
	if mode == ModeAll {
		return nil
	}
	sortedKeys := make([]string, 0, len(keys))
	for key := range keys {
		sortedKeys = append(sortedKeys, key)
	}
	sortedKeys = SortCoverage(sortedKeys)
	byPlacement := map[string]*retentionContender{}
	order := []string{}
	for _, key := range sortedKeys {
		for _, placement := range placements {
			if !placement.FactsOK || !slices.Contains(placement.Coverage, key) {
				continue
			}
			ref := PlacementRef{PlacementID: placement.PlacementID, Version: placement.Version, Fingerprint: placement.Fingerprint}
			pools := EvaluatePools(mode, []string{key}, placement.Facts, ref, placements, profile, map[string][]string{key: placement.Coverage})
			replaces := false
			for _, pool := range pools {
				if pool.Verdict == "replace" && len(pool.Losers) > 0 {
					replaces = true
				}
			}
			if !replaces {
				continue
			}
			contender, ok := byPlacement[placement.PlacementID]
			if !ok {
				contender = &retentionContender{placementID: placement.PlacementID, coverage: SortCoverage(placement.Coverage)}
				byPlacement[placement.PlacementID] = contender
				order = append(order, placement.PlacementID)
			}
			contender.keys = append(contender.keys, key)
		}
	}
	out := make([]retentionContender, 0, len(order))
	for _, id := range order {
		out = append(out, *byPlacement[id])
	}
	return out
}

// retentionComparable reports whether two same-key files meet in one
// retention pool: single_copy compares across groups, per_group only within
// one group (a file without a group belongs to every group's pool), all never.
func retentionComparable(mode AdmissionMode, sameGroup bool) bool {
	switch mode {
	case ModeSingleCopy:
		return true
	case ModePerGroup:
		return sameGroup
	}
	return false
}

// SameRetentionPool reports the instance mode and whether the two placements
// share a coverage key and meet in one retention pool on it.
func (s *ReplacementDecisionService) SameRetentionPool(ctx context.Context, animeID, left, right string) (AdmissionMode, bool, error) {
	snapshot, err := s.retentionSnapshot(ctx, animeID)
	if err != nil {
		return "", false, err
	}
	var a, b *PlacementFacts
	for index := range snapshot.placements {
		switch snapshot.placements[index].PlacementID {
		case left:
			a = &snapshot.placements[index]
		case right:
			b = &snapshot.placements[index]
		}
	}
	if a == nil || b == nil {
		return snapshot.mode, false, nil
	}
	shared := false
	for _, key := range a.Coverage {
		if slices.Contains(b.Coverage, key) {
			shared = true
		}
	}
	if !shared {
		return snapshot.mode, false, nil
	}
	groupA, okA := a.Facts.poolGroup()
	groupB, okB := b.Facts.poolGroup()
	sameGroup := !okA || !okB || !a.FactsOK || !b.FactsOK || groupA == groupB
	return snapshot.mode, retentionComparable(snapshot.mode, sameGroup), nil
}

type retentionSnapshotFacts struct {
	mode       AdmissionMode
	profile    Profile
	placements []PlacementFacts
}

// retentionSnapshot reads the instance mode and profile and the current
// pooled placements with their scored facts, as an evaluation would.
func (s *ReplacementDecisionService) retentionSnapshot(ctx context.Context, animeID string) (retentionSnapshotFacts, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return retentionSnapshotFacts{}, err
	}
	defer tx.Rollback()
	var (
		modeRaw    string
		profileRaw []byte
	)
	err = tx.QueryRowContext(ctx, `
SELECT admission_mode, quality_profile
FROM anime_business_configs WHERE managed_anime_id = $1 AND replacement_version IS NOT NULL`, animeID).Scan(&modeRaw, &profileRaw)
	if errors.Is(err, sql.ErrNoRows) {
		return retentionSnapshotFacts{}, &apperrors.NotFound{Resource: "instance_replacement_policy", ID: animeID}
	}
	if err != nil {
		return retentionSnapshotFacts{}, err
	}
	snapshot := retentionSnapshotFacts{mode: AdmissionMode(modeRaw)}
	if snapshot.profile, err = ParseProfile(profileRaw); err != nil {
		return retentionSnapshotFacts{}, err
	}
	if snapshot.placements, err = s.loadPlacementFacts(ctx, tx, animeID); err != nil {
		return retentionSnapshotFacts{}, err
	}
	scores, err := loadSubgroupScoresTx(ctx, tx, animeID)
	if err != nil {
		return retentionSnapshotFacts{}, err
	}
	for index := range snapshot.placements {
		snapshot.placements[index].Facts = snapshot.placements[index].Facts.scored(scores)
	}
	return snapshot, nil
}

// placementRelease is the release a placement descends from ("" for a
// scan-managed file no release claimed).
func (s *ReplacementDecisionService) placementRelease(ctx context.Context, placementID string) (string, error) {
	var releaseID string
	err := s.db.QueryRowContext(ctx, `
SELECT COALESCE(quality_facts_ref, '') FROM placements
WHERE id = $1 AND source_evidence_type = 'file_operation' AND state = 'active'`, placementID).Scan(&releaseID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return strings.TrimSpace(releaseID), err
}

// keyMembers are the other current members of one key, as "id@version",
// sorted: the pool composition a recheck decision is frozen for.
func keyMembers(placements []PlacementFacts, key, candidatePlacementID string) []string {
	members := []string{}
	for _, placement := range placements {
		if placement.PlacementID == candidatePlacementID || !slices.Contains(placement.Coverage, key) {
			continue
		}
		members = append(members, placement.PlacementID+"@"+strconv.FormatInt(placement.Version, 10))
	}
	sort.Strings(members)
	return members
}
