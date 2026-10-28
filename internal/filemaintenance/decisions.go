package filemaintenance

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	acquisitiontarget "bansoko/internal/acquisition/target"
	"bansoko/internal/apperrors"
	"bansoko/internal/batchplan"
	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/qualityprofile"
	facts "bansoko/internal/release/facts"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
)

// EvaluateRequest is the server-side evaluate input: only the candidate
// identity plus the optional caller idempotency key. Every winner-defining
// fact (coverage, group, quality facts, Subscription version, instance policy
// version/mode) is read from server-owned rows inside the evaluate
// transaction; a client can never submit winner evidence.
type EvaluateRequest struct {
	AnimeID          string
	CandidateRelease string
	// plannedCoverage is the coverage of the batch plan the Release evaluation
	// is admitting right now (server-internal, set only by CandidateIntents).
	// Before acquisition it decides the pools: the latest media fact snapshot
	// may still hold an earlier evaluation's plan (for a retried Release, the
	// failed acquisition's), whose keys this admission may no longer place.
	plannedCoverage []string
	// plannedUnits maps each planned key to the keys of the planned file that
	// holds it (one file replaces only what it covers).
	plannedUnits map[string][]string
}

// Typed business codes for evaluate failure conditions. A candidate Release
// may be evaluated before its first Placement exists; Placement remains an
// optional replacement index and is not acquisition truth.
const (
	codeSubscriptionStopped   = "SUBSCRIPTION_STOPPED"
	codeCandidateFactsMissing = "CANDIDATE_FACTS_MISSING"
	// Compatibility diagnostic for legacy callers; new evaluation no longer
	// emits it because Placement is optional before acquisition.
	codeCandidatePlacementMissing = "CANDIDATE_PLACEMENT_MISSING"
)

// Typed stale reasons recorded when a frozen decision is invalidated before
// any effect. Settlement writes exactly one of these into stale_reason.
const (
	staleSubscriptionStopped         = "subscription_stopped"
	staleSubscriptionVersionChanged  = "subscription_version_changed"
	staleSubscriptionMissing         = "subscription_missing"
	stalePolicyVersionChanged        = "policy_version_changed"
	stalePolicyMissing               = "policy_missing"
	stalePlacementMissing            = "placement_missing"
	stalePlacementVersionChanged     = "placement_version_changed"
	stalePlacementFingerprintChanged = "placement_fingerprint_changed"
	stalePlacementInactive           = "placement_inactive"
	staleLegacyEvidenceIncomplete    = "legacy_evidence_incomplete"
	// staleSubgroupScoreChanged: a subgroup score the comparison used was
	// edited after the decision froze (scores are read live, not versioned).
	staleSubgroupScoreChanged = "subgroup_score_changed"
	// staleLoserCoverageOutsideCandidate: a frozen loser now covers a key the
	// candidate placement does not, so superseding it would drop that key.
	staleLoserCoverageOutsideCandidate = "loser_coverage_outside_candidate"
)

// unresolvedCoverageKey is the coverage_key of the single blocked decision a
// release without resolvable coverage freezes.
const unresolvedCoverageKey = "unresolved"

// ReplacementDecisionService owns the persisted decisions: evaluate freezes
// the winner/losers and every execution input; settle revalidates the frozen
// evidence and supersedes only the frozen losers, handing them to cleanup
// with the frozen admission_mode.
type ReplacementDecisionService struct {
	db         *sql.DB
	now        func() time.Time
	cleanup    *CleanupService
	mediaFacts facts.MediaRepository
	activity   observabilityport.ActivityWriter
}

// NewReplacementDecisionService wires the decision owner.
func NewReplacementDecisionService(db *sql.DB) *ReplacementDecisionService {
	return &ReplacementDecisionService{db: db, now: time.Now}
}

// SetMediaFactsStore wires the P6 frozen coverage source. Unset (nil) keeps
// the legacy candidate-facts coverage.
func (s *ReplacementDecisionService) SetMediaFactsStore(store facts.MediaRepository) {
	if s != nil {
		s.mediaFacts = store
	}
}

// SetActivityWriter wires the central transaction-local business history
// writer. Unset (nil) keeps evaluation behavior identical without Activity.
func (s *ReplacementDecisionService) SetActivityWriter(writer observabilityport.ActivityWriter) {
	if s != nil {
		s.activity = writer
	}
}

// SetCleanupHandoff wires the cleanup admission used by settlement.
func (s *ReplacementDecisionService) SetCleanupHandoff(cleanup *CleanupService) {
	if s != nil {
		s.cleanup = cleanup
	}
}

// errCandidateEvidenceMissing is the typed fail-closed result when a Release
// needs a fresh replacement decision but the caller did not supply the frozen
// selected evidence. Without that evidence no candidate projection can be
// proven, so the admission must not invent coverage or fall back to a title
// re-parse.
var errCandidateEvidenceMissing = errors.New("replacement admission requires the frozen selected evidence")

// CandidateIntents reads or creates the frozen replacement admission of every
// planned coverage key of a Release (one decision per key). It is used
// before TargetSet probing so a winning candidate cannot be short-circuited by
// a same-size incumbent. A key without an admitted replace decision
// deliberately means ordinary ensure_present; filesystem state is never used
// to invent the replacement decision result. In all mode nothing is frozen and every key
// is ensure_present.
//
// The Release evaluation builds the batch plan before target planning; its
// planned coverage and file units arrive here so the candidate-facts
// projection the decision reads is materialized from it, in the decision's own
// admission step, instead of the Release owner writer's later projection. A
// key that already has a live (non-stale) decision keeps it; only the keys
// without one are evaluated. A missing evidence is a typed fail-closed error:
// no coverage still projects unresolved coverage and stays ineligible for
// replacement.
func (s *ReplacementDecisionService) CandidateIntents(ctx context.Context, managedAnimeID, releaseID string, evidence *acquisitiontarget.CandidateEvidence) (map[string]acquisitiontarget.Intent, error) {
	intents := map[string]acquisitiontarget.Intent{}
	if s == nil || s.db == nil {
		return intents, errors.New("replacement decision service is not wired")
	}
	var admissionMode string
	policyErr := s.db.QueryRowContext(ctx, `
SELECT admission_mode
FROM anime_business_configs
WHERE managed_anime_id = $1 AND replacement_version IS NOT NULL`, managedAnimeID).Scan(&admissionMode)
	if policyErr == nil && AdmissionMode(admissionMode) == ModeAll {
		return intents, nil
	}
	if policyErr != nil && !errors.Is(policyErr, sql.ErrNoRows) {
		return intents, policyErr
	}
	var coverage []string
	if evidence != nil {
		coverage = SortCoverage(evidence.Coverage)
	}
	keys := coverage
	if len(keys) == 0 {
		keys = []string{unresolvedCoverageKey}
	}
	live, err := s.liveDecisionsByKey(ctx, managedAnimeID, releaseID)
	if err != nil {
		return intents, err
	}
	missing := false
	var scores SubgroupScores
	for _, key := range keys {
		decision, ok := live[key]
		if !ok {
			missing = true
			continue
		}
		// A live decision whose subgroup scores were edited since is not
		// reused: the evaluation below stales it and decides again.
		if decision.State == "admitted" && len(decision.SubgroupScores) > 0 {
			if scores == nil {
				if scores, err = loadSubgroupScoresTx(ctx, s.db, managedAnimeID); err != nil {
					return intents, err
				}
			}
			if subgroupScoresChanged(decision.SubgroupScores, scores) {
				missing = true
			}
		}
	}
	if missing {
		// Materialize the candidate-facts projection from the same frozen
		// selected evidence the target plan consumes, then freeze the
		// replacement decisions before target probing/qB admission. Persisting
		// it here (a single adapter-owned transaction) means a first accepted
		// evaluation never races the Release owner writer's later projection;
		// the write is idempotent, so a candidate-facts row arriving later
		// cannot move an already-frozen decision.
		if evidence == nil || strings.TrimSpace(evidence.Title) == "" {
			return intents, fmt.Errorf("%w: release %s", errCandidateEvidenceMissing, releaseID)
		}
		if err := facts.EnsureSelectedEvidenceProjection(ctx, s.db, releaseID, evidence.Title, evidence.Coverage, evidence.SizeBytes); err != nil {
			return intents, err
		}
		if _, err := s.EvaluateApplication(ctx, EvaluateRequest{AnimeID: managedAnimeID, CandidateRelease: releaseID, plannedCoverage: evidence.Coverage, plannedUnits: evidence.Units}); err != nil {
			return intents, err
		}
		if live, err = s.liveDecisionsByKey(ctx, managedAnimeID, releaseID); err != nil {
			return intents, err
		}
	}
	for _, key := range coverage {
		intents[key] = acquisitiontarget.IntentEnsurePresent
		if decision := live[key]; decision.Verdict == "replace" && decision.State == "admitted" {
			intents[key] = acquisitiontarget.IntentReplace
		}
	}
	return intents, nil
}

// liveDecision is the latest non-stale decision of one coverage key.
type liveDecision struct {
	Verdict        string
	State          string
	SubgroupScores []FrozenSubgroupScore
}

// liveDecisionsByKey reads the latest non-stale decision of every coverage
// key of a Release.
func (s *ReplacementDecisionService) liveDecisionsByKey(ctx context.Context, managedAnimeID, releaseID string) (map[string]liveDecision, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT DISTINCT ON (coverage_key) coverage_key, verdict, state, pools
FROM replacement_decisions
WHERE managed_anime_id = $1 AND candidate_release_id = $2 AND state <> 'stale'
ORDER BY coverage_key, created_at DESC, id DESC`, managedAnimeID, releaseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	live := map[string]liveDecision{}
	for rows.Next() {
		var key string
		var decision liveDecision
		var poolsRaw []byte
		if err := rows.Scan(&key, &decision.Verdict, &decision.State, &poolsRaw); err != nil {
			return nil, err
		}
		var pools []PoolEvidence
		if err := json.Unmarshal(poolsRaw, &pools); err != nil {
			return nil, fmt.Errorf("decode replacement pools: %w", err)
		}
		decision.SubgroupScores = poolsSubgroupScores(pools)
		live[key] = decision
	}
	return live, rows.Err()
}

// decisionRow is the frozen decision as settlement reads it.
type decisionRow struct {
	ID                   string
	AnimeID              string
	CandidatePlacementID string
	SubscriptionVersion  int64
	PolicyVersion        int64
	AdmissionMode        AdmissionMode
	Verdict              string
	State                string
	CorrelationID        string
	// Winner and losers are the frozen placement evidence from the pools.
	// A replace decision must carry at least one winner ref (the candidate).
	Winner *PlacementRef
	Losers []PlacementRef
	// SubgroupScores are the subgroup scores the pools' comparisons used.
	SubgroupScores []FrozenSubgroupScore
}

// placementRefs returns the unique winner + loser placement refs.
func (d *decisionRow) placementRefs() []PlacementRef {
	seen := make(map[string]struct{}, 1+len(d.Losers))
	refs := make([]PlacementRef, 0, 1+len(d.Losers))
	appendRef := func(ref PlacementRef) {
		if _, exists := seen[ref.PlacementID]; exists {
			return
		}
		seen[ref.PlacementID] = struct{}{}
		refs = append(refs, ref)
	}
	if d.Winner != nil {
		appendRef(*d.Winner)
	}
	for _, loser := range d.Losers {
		appendRef(loser)
	}
	return refs
}

func (d *decisionRow) loserIDs() []string {
	ids := make([]string, 0, len(d.Losers))
	for _, loser := range d.Losers {
		ids = append(ids, loser.PlacementID)
	}
	return ids
}

// EvaluateApplication runs the server-side evaluate in one transaction and
// freezes one decision per coverage key: it locks the Subscription and
// the instance retention policy, reads the candidate facts plus any verified
// active incumbent placements, builds frozen per-pool evidence, and persists
// every key's decision idempotently. A repeated evaluate with identical
// frozen inputs returns the original decisions.
//
// A key the candidate release already placed (a verified active placement of
// the release covers it) is decided for that placement: its pre-acquisition
// decision is bound to it, or the key is evaluated with the placement as the
// candidate. A key the release has not placed yet keeps its pre-acquisition
// (synthetic release winner) semantics; before acquisition only replace and
// blocked conclusions are frozen, since anything else admits ordinarily.
func (s *ReplacementDecisionService) EvaluateApplication(ctx context.Context, input EvaluateRequest) ([]ReplacementDecisionView, error) {
	if strings.TrimSpace(input.AnimeID) == "" || strings.TrimSpace(input.CandidateRelease) == "" {
		return nil, &apperrors.BadRequest{Code: "EVALUATE_INPUT_MISSING", Message: "anime and candidate release are required"}
	}
	if err := facts.EnsureCurrentProfile(ctx, s.db, input.CandidateRelease); err != nil {
		return nil, fmt.Errorf("ensure current candidate facts: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	// Freeze the Subscription identity: only an active subscription evaluates;
	// the frozen version makes every later stop/change detectable at settle.
	ev := keyEvaluation{request: input}
	var subState string
	err = tx.QueryRowContext(ctx, `
SELECT state, version FROM subscriptions WHERE managed_anime_id = $1 FOR UPDATE`, input.AnimeID).
		Scan(&subState, &ev.subscriptionVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, &apperrors.NotFound{Resource: "subscription", ID: input.AnimeID}
	}
	if err != nil {
		return nil, err
	}
	if subState != "active" {
		return nil, &apperrors.Business{Status: 422, Code: codeSubscriptionStopped, Message: "subscription is not active; evaluate is a no-op until resume"}
	}

	var sourceKind string
	var inScope bool
	err = tx.QueryRowContext(ctx, `SELECT src.source_kind, COALESCE(selection.available AND selection.effective_selected,FALSE) FROM releases r JOIN rss_sources src ON src.id=r.rss_source_id LEFT JOIN provider_group_selections selection ON selection.subscription_id=src.subscription_id AND selection.provider=src.provider AND selection.group_ref=src.provider_group_ref WHERE r.id=$1 AND src.subscription_id=(SELECT id FROM subscriptions WHERE managed_anime_id=$2)`, input.CandidateRelease, input.AnimeID).Scan(&sourceKind, &inScope)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, &apperrors.Business{Status: 422, Code: codeCandidatePlacementMissing, Message: "candidate release has no candidate facts or verified placement in this subscription"}
	}
	if err != nil {
		return nil, err
	}
	if sourceKind == "provider_group" && !inScope {
		return nil, &apperrors.Business{Status: 409, Code: "GROUP_SCOPE_NOT_SELECTED", Message: "candidate provider group is not available and selected"}
	}

	// Freeze the complete instance retention policy: mode, profile and
	// version come from the server-owned row, never from the client.
	var (
		modeRaw    string
		profileRaw []byte
	)
	err = tx.QueryRowContext(ctx, `
SELECT admission_mode, quality_profile, replacement_version
FROM anime_business_configs WHERE managed_anime_id = $1 AND replacement_version IS NOT NULL FOR UPDATE`, input.AnimeID).
		Scan(&modeRaw, &profileRaw, &ev.policyVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, &apperrors.NotFound{Resource: "instance_replacement_policy", ID: input.AnimeID}
	}
	if err != nil {
		return nil, err
	}
	ev.mode = AdmissionMode(modeRaw)
	if ev.profile, err = ParseProfile(profileRaw); err != nil {
		return nil, err
	}

	// The release's own verified active placements, when any file is already
	// placed. Pre-acquisition evaluation intentionally permits none: the
	// candidate Release/facts are enough to freeze replacement intent, while the
	// eventual FileOperation may admit the Placement after publication.
	candidates, err := loadReleasePlacementsTx(ctx, tx, input.AnimeID, input.CandidateRelease)
	if err != nil {
		return nil, err
	}

	// Candidate quality facts come from the versioned facts row.
	var (
		coverageRaw, compareRaw []byte
		resolution, revision    sql.NullString
		source, language, group sql.NullString
		size                    sql.NullInt64
	)
	err = tx.QueryRowContext(ctx, `
SELECT parser_version, input_fingerprint, coverage_keys,
       resolution, revision, source, language, "group", size_bytes, compare_values
FROM release_candidate_facts
WHERE release_id = $1 ORDER BY release_version DESC LIMIT 1`, input.CandidateRelease).
		Scan(&ev.factsVersion, &ev.factsFingerprint, &coverageRaw,
			&resolution, &revision, &source, &language, &group, &size, &compareRaw)
	if errors.Is(err, sql.ErrNoRows) {
		if len(candidates) == 0 {
			// Preserve the legacy typed retry for a completely unmaterialized
			// candidate. Once candidate facts exist, no Placement is required.
			return nil, &apperrors.Business{Status: 422, Code: codeCandidatePlacementMissing, Message: "candidate release has no candidate facts or verified placement"}
		}
		return nil, &apperrors.Business{Status: 422, Code: codeCandidateFactsMissing, Message: "candidate release has no versioned quality facts"}
	}
	if err != nil {
		return nil, err
	}
	ev.candidateFacts = Facts{
		Revision:   revision.String,
		Resolution: resolution.String,
		Source:     source.String,
		Language:   language.String,
		Group:      group.String,
	}
	if size.Valid {
		ev.candidateFacts.Size = &size.Int64
	}
	ev.candidateFacts = ev.candidateFacts.withFrozenValues(compareRaw)
	// Pool identity comes from the frozen batch plan coverage when the release
	// has a snapshot: replacement must compare exactly the keys the release was
	// accepted to place. Before the snapshot exists, the candidate-facts
	// coverage is the durable projection of the same plan keys (written by
	// CandidateFromCoverage), not a second parser. A candidate-facts row that
	// cannot be projected is never re-parsed.
	//
	// A pre-acquisition evaluation that carries the admitting plan's coverage
	// uses it as is: that plan is the one this acquisition will place.
	coverage := []string{}
	units := input.plannedUnits
	if len(input.plannedCoverage) > 0 {
		coverage = append(coverage, input.plannedCoverage...)
	} else if frozenCoverage, frozenUnits, ok := s.loadFrozenPlanTx(ctx, tx, input.CandidateRelease); ok {
		coverage, units = frozenCoverage, frozenUnits
	} else if err := json.Unmarshal(coverageRaw, &coverage); err != nil {
		return nil, err
	}
	coverage = SortCoverage(dedupePlacementIDs(coverage))

	// Current verified active replacement-eligible placements with their
	// server-read quality facts (facts_ok = a release chain exists; an
	// eligible placement without one competes without values in single_copy
	// and belongs to no per_group pool).
	if ev.placements, err = s.loadPlacementFacts(ctx, tx, input.AnimeID); err != nil {
		return nil, err
	}
	// Subgroup scores are the subscription's current effective scores of
	// each side's frozen subgroup source.
	scores, err := loadSubgroupScoresTx(ctx, tx, input.AnimeID)
	if err != nil {
		return nil, err
	}
	ev.scores = scores
	ev.candidateFacts = ev.candidateFacts.scored(scores)
	for index := range ev.placements {
		ev.placements[index].Facts = ev.placements[index].Facts.scored(scores)
	}
	if ev.subscriptionID, err = s.activitySubscriptionTx(ctx, tx, input.AnimeID); err != nil {
		return nil, err
	}

	// Pools still need a stable candidate ref for deterministic evidence; the
	// synthetic release ref is never treated as a physical Placement and is
	// never sent to cleanup.
	synthetic := PlacementRef{PlacementID: "release:" + input.CandidateRelease, Version: 1, Fingerprint: ev.factsFingerprint}
	if len(coverage) == 0 || (len(coverage) == 1 && coverage[0] == unresolvedCoverageKey) {
		pools := EvaluatePools(ev.mode, nil, ev.candidateFacts, synthetic, ev.placements, ev.profile, nil)
		view, err := s.freezeKeyDecisionTx(ctx, tx, ev, unresolvedCoverageKey, nil, pools, synthetic, false, "")
		if err != nil {
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return []ReplacementDecisionView{view}, nil
	}

	// Each key is decided for the placement of the release that covers it (the
	// latest one), or before acquisition when none does yet.
	placedBy := map[string]int{}
	for index, candidate := range candidates {
		for _, key := range candidate.Coverage {
			placedBy[key] = index
		}
	}
	unplaced := []string{}
	byPlacement := map[int][]string{}
	for _, key := range coverage {
		if index, ok := placedBy[key]; ok {
			byPlacement[index] = append(byPlacement[index], key)
			continue
		}
		unplaced = append(unplaced, key)
	}
	views := []ReplacementDecisionView{}
	if len(unplaced) > 0 {
		decided, err := s.evaluateKeysTx(ctx, tx, ev, unplaced, units, synthetic, false, "")
		if err != nil {
			return nil, err
		}
		views = append(views, decided...)
	}
	for index, candidate := range candidates {
		keys := byPlacement[index]
		if len(keys) == 0 {
			continue
		}
		// A placed file's unit is what the file covers.
		placedUnits := make(map[string][]string, len(candidate.Coverage))
		for _, key := range candidate.Coverage {
			placedUnits[key] = SortCoverage(candidate.Coverage)
		}
		decided, err := s.evaluateKeysTx(ctx, tx, ev, keys, placedUnits, candidate.Ref, true, candidate.Correlation)
		if err != nil {
			return nil, err
		}
		views = append(views, decided...)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return views, nil
}

// keyEvaluation is the frozen evaluate input shared by every key of one
// release evaluation.
type keyEvaluation struct {
	request             EvaluateRequest
	subscriptionID      string
	subscriptionVersion int64
	policyVersion       int64
	mode                AdmissionMode
	profile             Profile
	factsVersion        string
	factsFingerprint    string
	candidateFacts      Facts
	placements          []PlacementFacts
	// scores are the subscription's current subgroup scores.
	scores SubgroupScores
}

// releasePlacement is one verified active placement of the candidate release.
type releasePlacement struct {
	Ref         PlacementRef
	Correlation string
	Coverage    []string
}

// loadReleasePlacementsTx reads the candidate release's verified, active,
// replacement-eligible placements (the files it already placed), oldest
// first, with their coverage.
func loadReleasePlacementsTx(ctx context.Context, tx *sql.Tx, animeID, releaseID string) ([]releasePlacement, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT p.id, p.version, p.target_fingerprint, COALESCE(p.correlation_id, ''),
       COALESCE((SELECT jsonb_agg(pc.observation_key ORDER BY pc.coverage_ordinal)
                 FROM placement_coverage pc WHERE pc.placement_id = p.id), '[]'::jsonb)
FROM placements p
WHERE p.managed_anime_id = $1
  AND p.quality_facts_ref = $2
  AND p.source_evidence_type = 'file_operation'
  AND p.verification_state = 'verified'
  AND p.state = 'active'
  AND p.replacement_eligible
ORDER BY p.created_at, p.id`, animeID, releaseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	placements := []releasePlacement{}
	for rows.Next() {
		var placement releasePlacement
		var coverageRaw []byte
		if err := rows.Scan(&placement.Ref.PlacementID, &placement.Ref.Version, &placement.Ref.Fingerprint, &placement.Correlation, &coverageRaw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(coverageRaw, &placement.Coverage); err != nil {
			return nil, fmt.Errorf("decode candidate placement coverage: %w", err)
		}
		placements = append(placements, placement)
	}
	return placements, rows.Err()
}

// evaluateKeysTx freezes the decisions of the keys one candidate ref decides.
// For a placed candidate, a key's pre-acquisition decision is bound to the
// placement first; before acquisition, a key's live unbound decision of the
// same versions is kept. The remaining keys are evaluated together, so a
// multi-episode file's unit is judged as one.
func (s *ReplacementDecisionService) evaluateKeysTx(ctx context.Context, tx *sql.Tx, ev keyEvaluation, keys []string, units map[string][]string, candidate PlacementRef, placed bool, correlation string) ([]ReplacementDecisionView, error) {
	views := []ReplacementDecisionView{}
	pending := []string{}
	for _, key := range keys {
		var (
			view ReplacementDecisionView
			ok   bool
			err  error
		)
		if placed {
			// A pre-acquisition winner is initially represented by a synthetic
			// release ref. Once the verified FileOperation admits the
			// Placement, bind that exact frozen decision instead of creating a
			// second decision with a different idempotency digest. The bind is
			// version-gated and happens before any new pool evaluation/effect
			// is persisted.
			view, ok, err = s.bindPreAcquisitionDecisionTxApplication(ctx, tx, ev, key, units[key], candidate, correlation)
		} else {
			view, ok, err = s.liveUnboundDecisionTx(ctx, tx, ev, key)
		}
		if err != nil {
			return nil, err
		}
		if ok {
			views = append(views, view)
			continue
		}
		pending = append(pending, key)
	}
	if len(pending) == 0 {
		return views, nil
	}
	pools := EvaluatePools(ev.mode, pending, ev.candidateFacts, candidate, ev.placements, ev.profile, units)
	freezeReplacementComparisons(pools, ev.candidateFacts, ev.placements, profileForMode(ev.mode, ev.profile))
	for _, key := range pending {
		keyPools := poolsForKey(pools, key, ev.mode)
		if !placed {
			// Before acquisition only a replacement (or a blocked pool) needs
			// a frozen admission; every other key admits ordinarily.
			if verdict := poolsVerdict(keyPools); verdict != "replace" && verdict != "blocked" {
				continue
			}
		}
		view, err := s.freezeKeyDecisionTx(ctx, tx, ev, key, unitOf(units, key), keyPools, candidate, placed, correlation)
		if err != nil {
			return nil, err
		}
		views = append(views, view)
	}
	return views, nil
}

// poolsForKey returns the pools of one coverage key (the all-mode and
// unresolved "all" pool belongs to every key).
func poolsForKey(pools []PoolEvidence, key string, mode AdmissionMode) []PoolEvidence {
	out := []PoolEvidence{}
	for _, pool := range pools {
		poolKey := pool.PoolKey
		if mode == ModePerGroup {
			poolKey, _, _ = strings.Cut(poolKey, "|")
		}
		if poolKey == key || pool.PoolKey == "all" {
			out = append(out, pool)
		}
	}
	return out
}

// liveUnboundDecisionTx returns a key's latest non-stale pre-acquisition
// decision when it was frozen under the current Subscription and policy
// versions. One frozen under other versions is staled, and the key is
// evaluated again.
func (s *ReplacementDecisionService) liveUnboundDecisionTx(ctx context.Context, tx *sql.Tx, ev keyEvaluation, key string) (ReplacementDecisionView, bool, error) {
	var (
		id                 string
		frozenPolicy       int64
		frozenSubscription sql.NullInt64
		state              string
		poolsRaw           []byte
	)
	err := tx.QueryRowContext(ctx, `
SELECT id, policy_version, subscription_version, state, pools
FROM replacement_decisions
WHERE managed_anime_id = $1 AND candidate_release_id = $2 AND coverage_key = $3
  AND candidate_placement_id IS NULL AND state <> 'stale'
ORDER BY created_at DESC, id DESC
LIMIT 1
FOR UPDATE`, ev.request.AnimeID, ev.request.CandidateRelease, key).Scan(&id, &frozenPolicy, &frozenSubscription, &state, &poolsRaw)
	if errors.Is(err, sql.ErrNoRows) {
		return ReplacementDecisionView{}, false, nil
	}
	if err != nil {
		return ReplacementDecisionView{}, false, err
	}
	if frozenSubscription.Int64 != ev.subscriptionVersion || frozenPolicy != ev.policyVersion {
		if state == "admitted" {
			reason := staleSubscriptionVersionChanged
			if frozenSubscription.Int64 == ev.subscriptionVersion {
				reason = stalePolicyVersionChanged
			}
			if _, err := tx.ExecContext(ctx, `
UPDATE replacement_decisions
SET state = 'stale', stale_reason = $2, version = version + 1, updated_at = CURRENT_TIMESTAMP
WHERE id = $1 AND state = 'admitted'`, id, reason); err != nil {
				return ReplacementDecisionView{}, false, err
			}
		}
		return ReplacementDecisionView{}, false, nil
	}
	if changed, err := frozenScoresChanged(poolsRaw, ev.scores); err != nil {
		return ReplacementDecisionView{}, false, err
	} else if changed {
		if state == "admitted" {
			if _, err := tx.ExecContext(ctx, `
UPDATE replacement_decisions
SET state = 'stale', stale_reason = $2, version = version + 1, updated_at = CURRENT_TIMESTAMP
WHERE id = $1 AND state = 'admitted'`, id, staleSubgroupScoreChanged); err != nil {
				return ReplacementDecisionView{}, false, err
			}
		}
		return ReplacementDecisionView{}, false, nil
	}
	view, err := s.loadDecisionByIDTxApplication(ctx, tx, id)
	if err != nil {
		return ReplacementDecisionView{}, false, err
	}
	return view, true, nil
}

// frozenScoresChanged reports whether a decision's frozen subgroup scores
// differ from the current ones.
func frozenScoresChanged(poolsRaw []byte, current SubgroupScores) (bool, error) {
	var pools []PoolEvidence
	if err := json.Unmarshal(poolsRaw, &pools); err != nil {
		return false, fmt.Errorf("decode replacement pools: %w", err)
	}
	return subgroupScoresChanged(poolsSubgroupScores(pools), current), nil
}

// freezeKeyDecisionTx persists one coverage key's decision idempotently and
// records its Activity. The idempotency key carries the anime, the release, a
// hash of the coverage key and the input digest; the id is derived from it so
// it stays bounded.
func (s *ReplacementDecisionService) freezeKeyDecisionTx(ctx context.Context, tx *sql.Tx, ev keyEvaluation, key string, unit []string, pools []PoolEvidence, candidate PlacementRef, placed bool, correlation string) (ReplacementDecisionView, error) {
	input := ev.request
	verdict := poolsVerdict(pools)
	state := "admitted"
	if verdict == "blocked" {
		state = "blocked"
	}
	poolsRaw, err := json.Marshal(pools)
	if err != nil {
		return ReplacementDecisionView{}, err
	}
	candidatePlacementID := ""
	if placed {
		candidatePlacementID = candidate.PlacementID
	}
	// A key whose earlier decisions went stale (its acquisition failed and the
	// Release was accepted again) must freeze a new decision even from
	// identical inputs, never replay the stale one.
	var staleDecisions int
	if err := tx.QueryRowContext(ctx, `
SELECT count(*) FROM replacement_decisions
WHERE managed_anime_id = $1 AND candidate_release_id = $2 AND coverage_key = $3 AND state = 'stale'`,
		input.AnimeID, input.CandidateRelease, key).Scan(&staleDecisions); err != nil {
		return ReplacementDecisionView{}, err
	}
	digest, err := decisionInputDigest(input.AnimeID, input.CandidateRelease, candidatePlacementID,
		ev.subscriptionVersion, ev.policyVersion, ev.mode, ev.profile.Version, ev.factsVersion, ev.factsFingerprint, key, unit, staleDecisions, poolsSubgroupScores(pools))
	if err != nil {
		return ReplacementDecisionView{}, err
	}
	idempotencyKey, id := decisionIdentity(input.AnimeID, input.CandidateRelease, key, digest)
	coverageKeys := []string{}
	if key != unresolvedCoverageKey {
		coverageKeys = append(coverageKeys, key)
	}
	coverageRaw, _ := json.Marshal(coverageKeys)
	causationType := "placement"
	causationID := candidatePlacementID
	if causationID == "" {
		causationType = "release"
		causationID = input.CandidateRelease
	}
	_, err = tx.ExecContext(ctx, `
INSERT INTO replacement_decisions
(id, idempotency_key, managed_anime_id, candidate_release_id, candidate_placement_id,
 policy_version, profile_version, subscription_version, input_digest,
 candidate_facts_ref, snapshot_generation, coverage_key, coverage_keys, admission_mode, pools, verdict, state,
 correlation_id, causation_type, causation_id, version, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, NULL, $11, $12, $13, $14, $15, $16,
	        NULLIF($17, ''), $18, $19, 1, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
ON CONFLICT (idempotency_key) DO NOTHING`,
		id, idempotencyKey, input.AnimeID, input.CandidateRelease, nullableText(candidatePlacementID),
		ev.policyVersion, ev.profile.Version, ev.subscriptionVersion, digest,
		input.CandidateRelease+":"+ev.factsVersion, key, coverageRaw, string(ev.mode), poolsRaw, verdict, state,
		correlation, causationType, causationID)
	if err != nil {
		return ReplacementDecisionView{}, err
	}
	view, err := s.loadDecisionByKeyTxApplication(ctx, tx, idempotencyKey)
	if err != nil {
		return ReplacementDecisionView{}, err
	}
	if s.activity != nil {
		title, err := activityAnimeTitle(ctx, tx, input.AnimeID)
		if err != nil {
			return ReplacementDecisionView{}, fmt.Errorf("read anime title for replacement activity: %w", err)
		}
		details, logReleaseID, err := replacementActivityDetailsTx(ctx, tx, input.AnimeID, input.CandidateRelease, key, verdict, pools, candidatePlacementID)
		if err != nil {
			return ReplacementDecisionView{}, err
		}
		if err := s.activity.AppendTx(ctx, txscope.Wrap(tx), observabilityport.ActivityRecord{
			SubscriptionID:  ev.subscriptionID,
			ManagedAnimeID:  input.AnimeID,
			Kind:            "replacement_decided",
			Outcome:         "info",
			ReasonCode:      verdict,
			SubjectType:     "replacement_decision",
			SubjectID:       view.ID,
			SubjectVersion:  1,
			StableResultKey: verdict,
			Title:           title,
			Summary:         replacementActivitySummary(verdict),
			ReleaseID:       logReleaseID,
			Details:         details,
			CorrelationID:   correlation,
			CausationType:   causationType,
			CausationID:     causationID,
		}); err != nil {
			return ReplacementDecisionView{}, fmt.Errorf("append replacement activity: %w", err)
		}
	}
	return view, nil
}

// activitySubscriptionTx reads the anime's subscription id for the decision
// Activity (empty when no Activity writer is wired).
func (s *ReplacementDecisionService) activitySubscriptionTx(ctx context.Context, tx *sql.Tx, animeID string) (string, error) {
	if s.activity == nil {
		return "", nil
	}
	subscriptionID, err := subscriptionIDForAnime(ctx, tx, animeID)
	if err != nil {
		return "", fmt.Errorf("read subscription for replacement activity: %w", err)
	}
	return subscriptionID, nil
}

// decisionIdentity derives a key decision's idempotency key and its bounded
// id: repl:<anime>:<release>:<sha256(key)[:12]>:<digest[:32]>, and "dec-"
// plus the first 40 hex digits of the idempotency key's sha256.
func decisionIdentity(animeID, releaseID, key, digest string) (string, string) {
	keySum := sha256.Sum256([]byte(key))
	idempotencyKey := fmt.Sprintf("repl:%s:%s:%s:%s", animeID, releaseID, hex.EncodeToString(keySum[:])[:12], digest[:32])
	idSum := sha256.Sum256([]byte(idempotencyKey))
	return idempotencyKey, "dec-" + hex.EncodeToString(idSum[:])[:40]
}

// loadPlacementFacts reads the verified active replacement-eligible
// placements of one anime with their coverage and quality facts
// (server-owned rows only). A placement with a pending redownload request no
// longer holds its episodes: the user asked for a new file, and the new
// placement displaces it. A placement the latest calibrated scan reports
// missing or changed (fingerprint_conflict) holds no coverage while the scan
// says so: it is neither pooled nor compared. A scan-managed placement
// (reconciliation) is a managed member like any other; until a release claims
// it, it has no facts and loses to any candidate. Released (unmanaged) files
// and other anime's placements are never read.
func (s *ReplacementDecisionService) loadPlacementFacts(ctx context.Context, tx *sql.Tx, animeID string) ([]PlacementFacts, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT p.id, p.version, p.target_fingerprint, p.created_at,
       (f.parser_version IS NOT NULL) AS facts_ok,
       COALESCE(f.resolution, ''), COALESCE(f.revision, ''), COALESCE(f.source, ''),
       COALESCE(f.language, ''), COALESCE(f."group", ''), f.size_bytes,
       COALESCE(f.compare_values, '{}'::jsonb)
FROM placements p
LEFT JOIN LATERAL (
    SELECT resolution, revision, source, language, "group", size_bytes, parser_version, compare_values
    FROM release_candidate_facts f
    WHERE f.release_id = p.quality_facts_ref
    ORDER BY f.release_version DESC LIMIT 1
) f ON true
WHERE p.managed_anime_id = $1 AND p.verification_state = 'verified' AND p.state = 'active'
  AND p.replacement_eligible AND p.redownload_requested_at IS NULL
  AND `+latestObservationIssue+` = ''
ORDER BY p.created_at, p.id`, animeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	placements := make([]PlacementFacts, 0)
	ids := make([]string, 0)
	for rows.Next() {
		var (
			placement  PlacementFacts
			factsOK    bool
			resolution string
			revision   string
			source     string
			language   string
			group      string
			size       sql.NullInt64
			compareRaw []byte
		)
		if err := rows.Scan(&placement.PlacementID, &placement.Version, &placement.Fingerprint,
			&placement.CreatedAt, &factsOK, &resolution, &revision, &source, &language, &group, &size, &compareRaw); err != nil {
			return nil, err
		}
		placement.FactsOK = factsOK
		placement.Facts = Facts{Revision: revision, Resolution: resolution, Source: source, Language: language, Group: group}
		if size.Valid {
			placement.Facts.Size = &size.Int64
		}
		// A placed file compares with the values its own release froze at
		// acceptance; a batch's files share them.
		placement.Facts = placement.Facts.withFrozenValues(compareRaw)
		placements = append(placements, placement)
		ids = append(ids, placement.PlacementID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return placements, nil
	}
	coverageRows, err := tx.QueryContext(ctx, `
SELECT placement_id, observation_key FROM placement_coverage
WHERE placement_id = ANY($1) ORDER BY placement_id, coverage_ordinal`, ids)
	if err != nil {
		return nil, err
	}
	defer coverageRows.Close()
	byID := make(map[string]*PlacementFacts, len(placements))
	for index := range placements {
		byID[placements[index].PlacementID] = &placements[index]
	}
	for coverageRows.Next() {
		var placementID, key string
		if err := coverageRows.Scan(&placementID, &key); err != nil {
			return nil, err
		}
		if placement, exists := byID[placementID]; exists {
			placement.Coverage = append(placement.Coverage, key)
		}
	}
	if err := coverageRows.Err(); err != nil {
		return nil, err
	}
	return placements, nil
}

// rowsQueryer is a database or transaction that returns row sets.
type rowsQueryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// loadSubgroupScoresTx reads the subscription's effective score of every
// provider group it has a selection row for. The release's frozen subgroup
// source is looked up here by (provider, group ref) only — never by a title
// group name. It takes no row lock: callers include read-only transactions
// (CoverageVerdicts, where SELECT ... FOR SHARE is an error), and RSS admission
// holds FOR UPDATE on the selection row while other connections evaluate.
func loadSubgroupScoresTx(ctx context.Context, tx rowsQueryer, animeID string) (SubgroupScores, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT selection.provider, selection.group_ref, selection.effective_score
FROM provider_group_selections selection
JOIN subscriptions sub ON sub.id = selection.subscription_id
WHERE sub.managed_anime_id = $1`, animeID)
	if err != nil {
		return nil, fmt.Errorf("read subgroup scores: %w", err)
	}
	defer rows.Close()
	scores := SubgroupScores{}
	for rows.Next() {
		var (
			source qualityprofile.SubgroupSource
			score  int64
		)
		if err := rows.Scan(&source.Provider, &source.GroupRef, &score); err != nil {
			return nil, err
		}
		scores[source] = score
	}
	return scores, rows.Err()
}

// decisionInputDigest is the deterministic digest over the complete frozen
// input of one key decision: candidate identity, versions, mode, facts
// identity, the coverage key, the candidate file's unit and the subgroup
// scores the comparison used. The count of the key's stale decisions and the
// scores enter only when non-empty, so a key decided once keeps its
// historical digest.
func decisionInputDigest(animeID, releaseID, placementID string, subscriptionVersion, policyVersion int64, mode AdmissionMode, profileVersion, factsVersion, factsFingerprint, coverageKey string, unit []string, staleDecisions int, scores []FrozenSubgroupScore) (string, error) {
	raw, err := json.Marshal(struct {
		AnimeID             string                `json:"anime_id"`
		CandidateRelease    string                `json:"candidate_release_id"`
		CandidatePlacement  string                `json:"candidate_placement_id"`
		SubscriptionVersion int64                 `json:"subscription_version"`
		PolicyVersion       int64                 `json:"policy_version"`
		AdmissionMode       string                `json:"admission_mode"`
		ProfileVersion      string                `json:"profile_version"`
		FactsVersion        string                `json:"facts_version"`
		FactsFingerprint    string                `json:"facts_fingerprint"`
		CoverageKey         string                `json:"coverage_key"`
		Unit                []string              `json:"unit,omitempty"`
		StaleDecisions      int                   `json:"stale_decisions,omitempty"`
		SubgroupScores      []FrozenSubgroupScore `json:"subgroup_scores,omitempty"`
	}{
		AnimeID: animeID, CandidateRelease: releaseID, CandidatePlacement: placementID,
		SubscriptionVersion: subscriptionVersion, PolicyVersion: policyVersion,
		AdmissionMode: string(mode), ProfileVersion: profileVersion,
		FactsVersion: factsVersion, FactsFingerprint: factsFingerprint,
		CoverageKey: coverageKey, Unit: SortCoverage(unit), StaleDecisions: staleDecisions,
		SubgroupScores: scores,
	})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// bindPreAcquisitionDecisionTxApplication attaches a verified candidate
// Placement to the key's decision that was frozen before download admission.
// The row is deliberately not re-evaluated: its pool/loser evidence remains
// the frozen winner decision, with only the synthetic candidate ref replaced
// by the real Placement evidence needed for settlement revalidation. A policy
// or Subscription version change invalidates the old row and lets the current
// Placement run through a fresh evaluate instead.
func (s *ReplacementDecisionService) bindPreAcquisitionDecisionTxApplication(
	ctx context.Context,
	tx *sql.Tx,
	ev keyEvaluation,
	key string,
	unit []string,
	candidate PlacementRef,
	correlationID string,
) (ReplacementDecisionView, bool, error) {
	input := ev.request
	var (
		id                 string
		frozenPolicy       int64
		frozenSubscription int64
		poolsRaw           []byte
	)
	err := tx.QueryRowContext(ctx, `
SELECT id, policy_version, subscription_version, pools
FROM replacement_decisions
WHERE managed_anime_id = $1
  AND candidate_release_id = $2
  AND coverage_key = $3
  AND candidate_placement_id IS NULL
  AND verdict = 'replace'
  AND state = 'admitted'
ORDER BY created_at DESC, id DESC
LIMIT 1
FOR UPDATE`, input.AnimeID, input.CandidateRelease, key).Scan(&id, &frozenPolicy, &frozenSubscription, &poolsRaw)
	if errors.Is(err, sql.ErrNoRows) {
		return ReplacementDecisionView{}, false, nil
	}
	if err != nil {
		return ReplacementDecisionView{}, false, err
	}
	if frozenSubscription != ev.subscriptionVersion {
		if _, err := tx.ExecContext(ctx, `
UPDATE replacement_decisions
SET state = 'stale', stale_reason = $2, version = version + 1, updated_at = CURRENT_TIMESTAMP
WHERE id = $1 AND state = 'admitted'`, id, staleSubscriptionVersionChanged); err != nil {
			return ReplacementDecisionView{}, false, err
		}
		return ReplacementDecisionView{}, false, nil
	}
	if frozenPolicy != ev.policyVersion {
		if _, err := tx.ExecContext(ctx, `
UPDATE replacement_decisions
SET state = 'stale', stale_reason = $2, version = version + 1, updated_at = CURRENT_TIMESTAMP
WHERE id = $1 AND state = 'admitted'`, id, stalePolicyVersionChanged); err != nil {
			return ReplacementDecisionView{}, false, err
		}
		return ReplacementDecisionView{}, false, nil
	}

	var pools []PoolEvidence
	if err := json.Unmarshal(poolsRaw, &pools); err != nil {
		return ReplacementDecisionView{}, false, fmt.Errorf("decode pre-acquisition replacement pools: %w", err)
	}
	if subgroupScoresChanged(poolsSubgroupScores(pools), ev.scores) {
		if _, err := tx.ExecContext(ctx, `
UPDATE replacement_decisions
SET state = 'stale', stale_reason = $2, version = version + 1, updated_at = CURRENT_TIMESTAMP
WHERE id = $1 AND state = 'admitted'`, id, staleSubgroupScoreChanged); err != nil {
			return ReplacementDecisionView{}, false, err
		}
		return ReplacementDecisionView{}, false, nil
	}
	synthetic := "release:" + input.CandidateRelease
	replaced := false
	for index := range pools {
		if pools[index].Winner != nil && pools[index].Winner.PlacementID == synthetic {
			winner := candidate
			pools[index].Winner = &winner
			replaced = true
		}
	}
	if !replaced {
		return ReplacementDecisionView{}, false, fmt.Errorf("pre-acquisition replacement decision %s has no synthetic candidate winner", id)
	}
	updatedPools, err := json.Marshal(pools)
	if err != nil {
		return ReplacementDecisionView{}, false, err
	}
	digest, err := decisionInputDigest(input.AnimeID, input.CandidateRelease, candidate.PlacementID,
		ev.subscriptionVersion, ev.policyVersion, ev.mode, ev.profile.Version, ev.factsVersion, ev.factsFingerprint, key, unit, 0, poolsSubgroupScores(pools))
	if err != nil {
		return ReplacementDecisionView{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE replacement_decisions
SET candidate_placement_id = $2,
    input_digest = $3,
    pools = $4,
    correlation_id = NULLIF($5, ''),
    causation_type = 'placement',
    causation_id = $2,
    version = version + 1,
    updated_at = CURRENT_TIMESTAMP
WHERE id = $1 AND state = 'admitted'`, id, candidate.PlacementID, digest, updatedPools, correlationID); err != nil {
		return ReplacementDecisionView{}, false, err
	}
	view, err := s.loadDecisionByIDTxApplication(ctx, tx, id)
	if err != nil {
		return ReplacementDecisionView{}, false, err
	}
	return view, true, nil
}

// SettleForPlacement settles every admitted replace decision bound to one
// candidate placement (the just-admitted file; a multi-episode file holds one
// decision per key) together. It never scans the anime or re-selects a
// winner; without a decision for this placement it is a no-op.
func (s *ReplacementDecisionService) SettleForPlacement(ctx context.Context, placementID string) error {
	rows, err := s.db.QueryContext(ctx, `
SELECT id FROM replacement_decisions
WHERE candidate_placement_id = $1 AND verdict = 'replace' AND state = 'admitted'
ORDER BY coverage_key, created_at DESC, id DESC`, placementID)
	if err != nil {
		return err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if len(ids) == 0 {
		return nil
	}
	return s.settleDecisions(ctx, ids)
}

// SettleDecision executes one frozen replace decision by id. Inside one
// transaction it revalidates the frozen Subscription (active + version),
// instance policy version, and winner/loser placement versions and
// fingerprints; any change marks the decision stale with a typed reason and
// performs zero new effects. On success only the frozen losers are superseded,
// their deletion obligations are recorded and the decision is marked settled,
// all in one transaction.
func (s *ReplacementDecisionService) SettleDecision(ctx context.Context, decisionID string) error {
	if s.cleanup == nil {
		return errors.New("cleanup handoff is not wired")
	}
	decision, err := s.loadDecisionRow(ctx, decisionID)
	if err != nil {
		return err
	}
	if decision == nil {
		return &apperrors.NotFound{Resource: "replacement_decision", ID: decisionID}
	}
	if decision.State == "settled" {
		// The deletion obligations committed with the settle itself.
		return nil
	}
	return s.settleDecisions(ctx, []string{decisionID})
}

// settleDecisions settles admitted replace decisions of one candidate in one
// transaction. Each decision is revalidated on its own (a stale one only
// stales itself), and each loser must be covered entirely by the candidate
// placement: superseding a loser that also holds a key the candidate does not
// cover would drop that key (loser_coverage_outside_candidate). The valid
// decisions' losers are superseded together, each superseded loser's deletion
// obligation is recorded once (even when two key decisions froze the same
// multi-episode loser) and the decisions settled, in the same transaction.
func (s *ReplacementDecisionService) settleDecisions(ctx context.Context, ids []string) error {
	if s.cleanup == nil {
		return errors.New("cleanup handoff is not wired")
	}
	decisions := make([]*decisionRow, 0, len(ids))
	for _, id := range ids {
		decision, err := s.loadDecisionRow(ctx, id)
		if err != nil {
			return err
		}
		if decision == nil || decision.State != "admitted" || decision.Verdict != "replace" {
			continue // stale/blocked/failed decisions are never executed
		}
		if decision.Winner == nil {
			// A replace decision without frozen winner evidence cannot be
			// settled safely (legacy shape); fail closed instead of guessing.
			if err := s.markStale(ctx, id, staleLegacyEvidenceIncomplete); err != nil {
				return err
			}
			continue
		}
		decisions = append(decisions, decision)
	}
	if len(decisions) == 0 {
		return nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Revalidate inside the settle transaction: the FOR UPDATE row locks
	// serialize settle against a concurrent stop/policy-save/placement change,
	// so the check and the effect commit atomically.
	valid := make([]*decisionRow, 0, len(decisions))
	validIDs := []string{}
	losers := []string{}
	for _, decision := range decisions {
		stale, err := revalidateDecisionTx(ctx, tx, decision)
		if err != nil {
			return err
		}
		if stale == "" {
			if stale, err = loserCoverageOutsideCandidateTx(ctx, tx, decision); err != nil {
				return err
			}
		}
		if stale != "" {
			if _, err := tx.ExecContext(ctx, `
UPDATE replacement_decisions
SET state = 'stale', stale_reason = $2, version = version + 1, updated_at = CURRENT_TIMESTAMP
WHERE id = $1 AND state = 'admitted'`, decision.ID, stale); err != nil {
				return err
			}
			continue
		}
		valid = append(valid, decision)
		validIDs = append(validIDs, decision.ID)
		losers = append(losers, decision.loserIDs()...)
	}
	if len(losers) > 0 {
		if _, err := tx.ExecContext(ctx, `
UPDATE placements SET state = 'superseded', version = version + 1, updated_at = CURRENT_TIMESTAMP
WHERE id = ANY($1) AND state = 'active'`, dedupePlacementIDs(losers)); err != nil {
			return err
		}
	}
	if len(validIDs) > 0 {
		if _, err := tx.ExecContext(ctx, `
UPDATE replacement_decisions SET state = 'settled', version = version + 1, updated_at = CURRENT_TIMESTAMP
WHERE id = ANY($1) AND state = 'admitted'`, validIDs); err != nil {
			return err
		}
	}
	handed := map[string]bool{}
	for _, decision := range valid {
		if err := s.admitLoserDeletionsTx(ctx, txscope.Wrap(tx), decision, handed); err != nil {
			return err
		}
		if err := s.appendSettledActivityTx(ctx, tx, decision); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// replacementSettledReason is the reason code of the run-log line a settled
// replace decision writes: the candidate is placed and the losers are
// superseded, so the replacement has happened.
const replacementSettledReason = "replaced"

// appendSettledActivityTx records, in the settle transaction, that one replace
// decision took effect. The decision line written at acceptance only plans the
// replacement; this line is written only here, so a release whose import
// fails never shows a replacement as done.
func (s *ReplacementDecisionService) appendSettledActivityTx(ctx context.Context, tx *sql.Tx, decision *decisionRow) error {
	if s.activity == nil {
		return nil
	}
	var releaseID, key string
	var poolsRaw []byte
	if err := tx.QueryRowContext(ctx, `
SELECT candidate_release_id, coverage_key, pools FROM replacement_decisions WHERE id = $1`, decision.ID).Scan(&releaseID, &key, &poolsRaw); err != nil {
		return fmt.Errorf("read settled replacement decision: %w", err)
	}
	var pools []PoolEvidence
	if err := json.Unmarshal(poolsRaw, &pools); err != nil {
		return fmt.Errorf("decode settled replacement pools: %w", err)
	}
	subscriptionID, err := subscriptionIDForAnime(ctx, tx, decision.AnimeID)
	if err != nil {
		return fmt.Errorf("read subscription for replacement activity: %w", err)
	}
	title, err := activityAnimeTitle(ctx, tx, decision.AnimeID)
	if err != nil {
		return fmt.Errorf("read anime title for replacement activity: %w", err)
	}
	details, logReleaseID, err := replacementActivityDetailsTx(ctx, tx, decision.AnimeID, releaseID, key, "replace", pools, decision.CandidatePlacementID)
	if err != nil {
		return err
	}
	if err := s.activity.AppendTx(ctx, txscope.Wrap(tx), observabilityport.ActivityRecord{
		SubscriptionID:  subscriptionID,
		ManagedAnimeID:  decision.AnimeID,
		Kind:            "replacement_decided",
		Outcome:         "info",
		ReasonCode:      replacementSettledReason,
		SubjectType:     "replacement_decision",
		SubjectID:       decision.ID,
		SubjectVersion:  1,
		StableResultKey: replacementSettledReason,
		Title:           title,
		Summary:         replacementActivitySummary(replacementSettledReason),
		ReleaseID:       logReleaseID,
		Details:         details,
		CorrelationID:   decision.CorrelationID,
		CausationType:   "placement",
		CausationID:     decision.CandidatePlacementID,
	}); err != nil {
		return fmt.Errorf("append settled replacement activity: %w", err)
	}
	return nil
}

// loserCoverageOutsideCandidateTx returns loser_coverage_outside_candidate
// when a frozen loser currently covers a key the decision's candidate
// placement does not.
func loserCoverageOutsideCandidateTx(ctx context.Context, q queryer, decision *decisionRow) (string, error) {
	candidate := decision.CandidatePlacementID
	if candidate == "" || len(decision.Losers) == 0 {
		return "", nil
	}
	var outside bool
	if err := q.QueryRowContext(ctx, `
SELECT EXISTS (
    SELECT 1 FROM placement_coverage loser
    WHERE loser.placement_id = ANY($1)
      AND NOT EXISTS (
          SELECT 1 FROM placement_coverage covered
          WHERE covered.placement_id = $2 AND covered.observation_key = loser.observation_key))`,
		decision.loserIDs(), candidate).Scan(&outside); err != nil {
		return "", err
	}
	if outside {
		return staleLoserCoverageOutsideCandidate, nil
	}
	return "", nil
}

// markStale transitions an admitted decision to stale with the typed reason
// (zero effects). A decision that already left 'admitted' (stale or settled)
// is a no-op.
func (s *ReplacementDecisionService) markStale(ctx context.Context, decisionID, reason string) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE replacement_decisions
SET state = 'stale', stale_reason = $2, version = version + 1, updated_at = CURRENT_TIMESTAMP
WHERE id = $1 AND state = 'admitted'`, decisionID, reason)
	return err
}

// queryer is the shared SQL row source for the revalidation read (database
// or transaction).
type queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// revalidateDecisionTx checks the frozen evidence against the live owners
// inside the caller's transaction. All rows are locked FOR UPDATE so the
// check and the settle effect commit atomically. It returns the typed stale
// reason, or "" when the decision is still valid.
func revalidateDecisionTx(ctx context.Context, q queryer, decision *decisionRow) (string, error) {
	var subState string
	var subVersion int64
	err := q.QueryRowContext(ctx, `
SELECT state, version FROM subscriptions WHERE managed_anime_id = $1 FOR UPDATE`, decision.AnimeID).
		Scan(&subState, &subVersion)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return staleSubscriptionMissing, nil
	case err != nil:
		return "", err
	case subState != "active":
		return staleSubscriptionStopped, nil
	case subVersion != decision.SubscriptionVersion:
		return staleSubscriptionVersionChanged, nil
	}
	var policyVersion int64
	err = q.QueryRowContext(ctx, `
SELECT replacement_version FROM anime_business_configs WHERE managed_anime_id = $1 AND replacement_version IS NOT NULL FOR UPDATE`, decision.AnimeID).
		Scan(&policyVersion)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return stalePolicyMissing, nil
	case err != nil:
		return "", err
	case policyVersion != decision.PolicyVersion:
		return stalePolicyVersionChanged, nil
	}
	if len(decision.SubgroupScores) > 0 {
		scorer, ok := q.(rowsQueryer)
		if !ok {
			return "", errors.New("revalidate subgroup scores: queryer cannot read rows")
		}
		current, err := loadSubgroupScoresTx(ctx, scorer, decision.AnimeID)
		if err != nil {
			return "", err
		}
		if subgroupScoresChanged(decision.SubgroupScores, current) {
			return staleSubgroupScoreChanged, nil
		}
	}
	for _, ref := range decision.placementRefs() {
		var placementState, verification string
		var version int64
		var fingerprint string
		err = q.QueryRowContext(ctx, `
SELECT state, verification_state, version, target_fingerprint
FROM placements WHERE id = $1 FOR UPDATE`, ref.PlacementID).
			Scan(&placementState, &verification, &version, &fingerprint)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			return stalePlacementMissing, nil
		case err != nil:
			return "", err
		case placementState != "active" || verification != "verified":
			return stalePlacementInactive, nil
		case version != ref.Version:
			return stalePlacementVersionChanged, nil
		case fingerprint != ref.Fingerprint:
			return stalePlacementFingerprintChanged, nil
		}
	}
	return "", nil
}

// EvaluateHandler is the replacement_evaluate work item handler. The claim
// owner is the managed anime (file_maintenance owner id); the input snapshot
// carries the candidate release identity. The handler calls the same
// server-side evaluate owner: success reflects the persisted decision, a
// stopped subscription is a typed no-op, a missing candidate placement
// retries, and unknown conditions return the error for the runtime retry
// policy. A persisted replace decision is then settled in its own
// transaction (phase-3 effect gate): settlement revalidates the frozen
// Subscription/policy/placement evidence and supersedes only the frozen
// losers; a stop or version change between the evaluate commit and the
// settle transaction marks the decision stale with zero new effects. The
// evaluate owner is idempotent, so a crash between evaluate and settle
// converges on replay. There is no empty success branch.
func (s *ReplacementDecisionService) EvaluateHandler() worker.Handler {
	return replacementRecoveryHandler{service: s, handle: s.replacementBusinessHandler()}
}
func (s *ReplacementDecisionService) replacementBusinessHandler() worker.Handler {
	return worker.HandlerFunc(func(ctx context.Context, claim workitems.Claim) (worker.Outcome, error) {
		if claim.Kind != workitems.KindReplacementEvaluate {
			return worker.Outcome{}, fmt.Errorf("replacement evaluate handler received kind %q", claim.Kind)
		}
		var input struct {
			CandidateReleaseID string `json:"candidate_release_id"`
			Trigger            string `json:"trigger"`
		}
		if err := json.Unmarshal(claim.InputSnapshot, &input); err != nil || strings.TrimSpace(input.CandidateReleaseID) == "" {
			return worker.Finish(workitems.StateFailed, workitems.OwnerResult{State: "failed", ReasonCode: "replacement_claim_invalid", Reason: "claim input must carry candidate_release_id"}, nil), nil
		}
		views, err := s.EvaluateApplication(ctx, EvaluateRequest{AnimeID: claim.Owner.ID, CandidateRelease: input.CandidateReleaseID})
		if err != nil {
			var business *apperrors.Business
			if errors.As(err, &business) {
				switch business.Code {
				case codeSubscriptionStopped:
					// No-op with a typed reason: resume creates a fresh epoch.
					return worker.Finish(workitems.StateSucceeded, workitems.OwnerResult{State: "admitted", ReasonCode: "subscription_stopped", Reason: business.Message}, nil), nil
				case codeCandidatePlacementMissing:
					return worker.Retry("candidate_placement_missing", business.Message, 30*time.Second, nil), nil
				default:
					return worker.Finish(workitems.StateFailed, workitems.OwnerResult{State: "failed", ReasonCode: business.Code, Reason: business.Message}, nil), nil
				}
			}
			var notFound *apperrors.NotFound
			if errors.As(err, &notFound) {
				return worker.Finish(workitems.StateFailed, workitems.OwnerResult{State: "failed", ReasonCode: "replacement_input_invalid", Reason: notFound.Error()}, nil), nil
			}
			// Transient/unknown: keep the retryable owner truth in the runtime.
			return worker.Outcome{}, err
		}
		// Phase-3 effect gate: settle revalidates inside its own transaction
		// and supersedes only the frozen losers of the decisions bound to
		// each candidate placement. Any settle error is transient (cleanup
		// handoff etc.) and retried; the idempotent evaluate converges on
		// replay.
		settled := map[string]bool{}
		ids := make([]string, 0, len(views))
		state := "admitted"
		for _, view := range views {
			ids = append(ids, view.ID)
			if view.State == "blocked" {
				state = "blocked"
			}
			if view.Verdict != "replace" || view.State != "admitted" || view.CandidatePlacementID == nil || settled[*view.CandidatePlacementID] {
				continue
			}
			settled[*view.CandidatePlacementID] = true
			if err := s.SettleForPlacement(ctx, *view.CandidatePlacementID); err != nil {
				return worker.Outcome{}, fmt.Errorf("settle after evaluate: %w", err)
			}
		}
		return worker.Finish(workitems.StateSucceeded, workitems.OwnerResult{State: state, ReasonCode: "replacement_evaluated", Reason: "decisions persisted: " + strings.Join(ids, ",")}, nil), nil
	})
}

// ListDecisions returns persisted decisions (side-effect free).
func (s *ReplacementDecisionService) ListDecisionsApplication(ctx context.Context, animeID string, limit, offset int) ([]ReplacementDecisionView, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, managed_anime_id, candidate_release_id, coverage_key, candidate_placement_id,
       policy_version, profile_version, subscription_version, input_digest,
       snapshot_generation, admission_mode, verdict, state, stale_reason, pools, version, created_at
FROM replacement_decisions
WHERE ($1 = '' OR managed_anime_id = $1)
ORDER BY created_at DESC
LIMIT $2 OFFSET $3`, animeID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	views := make([]ReplacementDecisionView, 0, limit)
	for rows.Next() {
		view, err := scanDecisionViewApplication(rows.Scan)
		if err != nil {
			return nil, err
		}
		views = append(views, view)
	}
	return views, rows.Err()
}

// loadDecisionRow reads the frozen settlement evidence of one decision.
func (s *ReplacementDecisionService) loadDecisionRow(ctx context.Context, decisionID string) (*decisionRow, error) {
	var (
		row                 decisionRow
		candidatePlacement  sql.NullString
		subscriptionVersion sql.NullInt64
		poolsRaw            []byte
		mode                string
		corrID              sql.NullString
	)
	err := s.db.QueryRowContext(ctx, `
SELECT id, managed_anime_id, candidate_placement_id, subscription_version,
       policy_version, admission_mode, verdict, state, pools, correlation_id
FROM replacement_decisions WHERE id = $1`, decisionID).
		Scan(&row.ID, &row.AnimeID, &candidatePlacement, &subscriptionVersion,
			&row.PolicyVersion, &mode, &row.Verdict, &row.State, &poolsRaw, &corrID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	row.CorrelationID = corrID.String
	row.CandidatePlacementID = candidatePlacement.String
	row.AdmissionMode = AdmissionMode(mode)
	if subscriptionVersion.Valid {
		row.SubscriptionVersion = subscriptionVersion.Int64
	}
	var pools []PoolEvidence
	if err := json.Unmarshal(poolsRaw, &pools); err != nil {
		return nil, err
	}
	for index := range pools {
		if pools[index].Winner != nil {
			winner := *pools[index].Winner
			if row.Winner == nil {
				row.Winner = &winner
			}
		}
		row.Losers = append(row.Losers, pools[index].Losers...)
	}
	row.SubgroupScores = poolsSubgroupScores(pools)
	return &row, nil
}

// loadDecisionByKeyTxApplication reads one decision inside the caller's transaction (the
// insert may have conflicted idempotently, so the read is the only writer).
func (s *ReplacementDecisionService) loadDecisionByKeyTxApplication(ctx context.Context, tx *sql.Tx, idempotencyKey string) (ReplacementDecisionView, error) {
	row := tx.QueryRowContext(ctx, `
SELECT id, managed_anime_id, candidate_release_id, coverage_key, candidate_placement_id,
       policy_version, profile_version, subscription_version, input_digest,
       snapshot_generation, admission_mode, verdict, state, stale_reason, pools, version, created_at
FROM replacement_decisions WHERE idempotency_key = $1`, idempotencyKey)
	return scanDecisionViewApplication(row.Scan)
}

func (s *ReplacementDecisionService) loadDecisionByIDTxApplication(ctx context.Context, tx *sql.Tx, id string) (ReplacementDecisionView, error) {
	row := tx.QueryRowContext(ctx, `
SELECT id, managed_anime_id, candidate_release_id, coverage_key, candidate_placement_id,
       policy_version, profile_version, subscription_version, input_digest,
       snapshot_generation, admission_mode, verdict, state, stale_reason, pools, version, created_at
FROM replacement_decisions WHERE id = $1`, id)
	return scanDecisionViewApplication(row.Scan)
}

// scanDecisionViewApplication maps one frozen decision row onto the application view.
func scanDecisionViewApplication(scan func(...any) error) (ReplacementDecisionView, error) {
	var (
		view                ReplacementDecisionView
		mode                string
		candidatePlacement  sql.NullString
		subscriptionVersion sql.NullInt64
		inputDigest         sql.NullString
		snapshotGeneration  sql.NullInt64
		staleReason         sql.NullString
		poolsRaw            []byte
	)
	err := scan(&view.ID, &view.ManagedAnimeID, &view.CandidateReleaseID, &view.CoverageKey, &candidatePlacement,
		&view.PolicyVersion, &view.ProfileVersion, &subscriptionVersion, &inputDigest,
		&snapshotGeneration, &mode, &view.Verdict, &view.State, &staleReason, &poolsRaw,
		&view.Version, &view.CreatedAt)
	if err != nil {
		return ReplacementDecisionView{}, err
	}
	view.AdmissionMode = mode
	if candidatePlacement.Valid {
		view.CandidatePlacementID = &candidatePlacement.String
	}
	if subscriptionVersion.Valid {
		value := int(subscriptionVersion.Int64)
		view.SubscriptionVersion = &value
	}
	if inputDigest.Valid {
		view.InputDigest = &inputDigest.String
	}
	if snapshotGeneration.Valid {
		value := int(snapshotGeneration.Int64)
		view.SnapshotGeneration = &value
	}
	if staleReason.Valid {
		view.StaleReason = &staleReason.String
	}
	var pools []PoolEvidence
	if err := json.Unmarshal(poolsRaw, &pools); err != nil {
		return ReplacementDecisionView{}, err
	}
	if bytes.Equal(bytes.TrimSpace(poolsRaw), []byte("null")) {
		return view, nil
	}
	evidence := make([]ReplacementPoolEvidence, len(pools))
	for index, pool := range pools {
		item := ReplacementPoolEvidence{
			PoolKey: pool.PoolKey,
			Verdict: pool.Verdict,
			Steps:   pool.Steps,
		}
		if pool.Reason != "" {
			reason := pool.Reason
			item.Reason = &reason
		}
		if pool.Winner != nil {
			item.Winner = &ReplacementPlacementRef{
				PlacementID: pool.Winner.PlacementID,
				Version:     int(pool.Winner.Version),
				Fingerprint: pool.Winner.Fingerprint,
			}
		}
		if pool.Losers != nil {
			losers := make([]ReplacementPlacementRef, len(pool.Losers))
			for index, loser := range pool.Losers {
				losers[index] = ReplacementPlacementRef{
					PlacementID: loser.PlacementID,
					Version:     int(loser.Version),
					Fingerprint: loser.Fingerprint,
				}
			}
			item.Losers = &losers
		}
		evidence[index] = item
	}
	view.Pools = &evidence
	return view, nil
}

// loadFrozenPlanTx reads the placeable coverage frozen in the batch plan of
// the release's media fact snapshot: the keys of the planned videos, so a
// covered or collided file of the same torrent never joins the decision, and
// each key's file unit. It returns ok=false when the release has no snapshot
// or no plan yet (the pre-acquisition admission runs before the owner writer
// freezes it); the candidate-facts coverage is then the same plan projection.
func (s *ReplacementDecisionService) loadFrozenPlanTx(ctx context.Context, tx *sql.Tx, releaseID string) ([]string, map[string][]string, bool) {
	var raw []byte
	err := tx.QueryRowContext(ctx, `
SELECT release_facts->'batch_plan'
FROM media_fact_snapshots
WHERE release_id = $1
ORDER BY created_at DESC, id DESC
LIMIT 1`, releaseID).Scan(&raw)
	if err != nil || len(raw) == 0 || string(raw) == "null" {
		return nil, nil, false
	}
	var plan batchplan.Plan
	if err := json.Unmarshal(raw, &plan); err != nil || len(plan.CoverageKeys) == 0 {
		return nil, nil, false
	}
	return SortCoverage(plan.CoverageKeys), plan.CandidateUnits(), true
}

// replacementActivitySummary is the business-history line of one replacement
// decision. The verdict stays the typed reason code; the line never shows it raw.
// A replace decision is only planned until it settles (replacementSettledReason
// records the outcome), so its line is in the future tense.
func replacementActivitySummary(verdict string) string {
	switch verdict {
	case "replace":
		return "替换版本：将替换旧文件"
	case replacementSettledReason:
		return "替换版本：已替换旧文件"
	case "keep", "retain":
		return "替换版本：保留现有文件"
	case "queue":
		return "替换版本：保留所有版本"
	case "blocked":
		return "替换版本：无法比较，未替换"
	default:
		return "替换版本已评估"
	}
}
