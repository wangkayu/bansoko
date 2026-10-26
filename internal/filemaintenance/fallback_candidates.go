package filemaintenance

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"bansoko/internal/batchplan"
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
)

// TriggerReleaseFallback is the release_evaluate input trigger of a release the
// fallback query promoted. The release evaluation reads it to enqueue the
// query again when the promoted release did not start a download, so the next
// candidate of the pool gets its turn.
const TriggerReleaseFallback = "replacement_fallback"

// Fallback trigger reasons recorded in the replacement_fallback input.
const (
	FallbackCandidateSettled = "candidate_settled"
	FallbackPromotionEnded   = "promotion_ended"
)

// releaseEvaluateMaxAttempts matches the release_evaluate runtime policy.
const releaseEvaluateMaxAttempts = 5

// SetWorkItems wires the store the fallback query enqueues release
// evaluations with.
func (s *ReplacementDecisionService) SetWorkItems(store *workitems.Store) {
	if s != nil {
		s.workItems = store
	}
}

// FallbackPromotion is one release the fallback query evaluates again: the
// best candidate of at least one freed pool.
type FallbackPromotion struct {
	ReleaseID string
	Version   int64
	Pools     []string
}

// fallbackCandidate is one release a running acquisition kept out: its latest
// batch plan skipped at least one video as covered_in_flight.
type fallbackCandidate struct {
	releaseID    string
	version      int64
	discoveredAt time.Time
	keys         []string
	facts        Facts
}

// FallbackCandidates is the fallback query of one anime (owner ruling 3): no
// state machine, one highest-score query per pool. The input set is every
// release of the active subscription that
//   - came through a source in scope (custom, or an available selected group),
//   - may be evaluated again (ReleaseReevaluationScope: no download task and
//     no terminal verdict) — a failed or cancelled release keeps its download
//     task, so it is excluded here until a user retries it (rulings 5, 6),
//   - had a video skipped as covered_in_flight in its latest batch plan.
//
// A pool is the coverage key (single_copy) or the key plus the stable group
// identity (per_group). A pool another running acquisition still claims is
// skipped. The candidates of each free pool are ranked by the policy profile
// (profile-v1, subgroup scores resolved now, then size), ties by the earlier
// discovery and then the id; the first of each pool is promoted. A release
// promoted for several pools is returned once. all mode, a missing policy and
// an inactive subscription promote nothing.
func (s *ReplacementDecisionService) FallbackCandidates(ctx context.Context, animeID string) ([]FallbackPromotion, string, error) {
	if s == nil || s.db == nil {
		return nil, "", errors.New("replacement decision service is not wired")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, "", err
	}
	defer tx.Rollback()
	var subscriptionState string
	err = tx.QueryRowContext(ctx, `SELECT state FROM subscriptions WHERE managed_anime_id = $1`, animeID).Scan(&subscriptionState)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "subscription_missing", nil
	}
	if err != nil {
		return nil, "", fmt.Errorf("read subscription: %w", err)
	}
	if subscriptionState != "active" {
		return nil, "subscription_inactive", nil
	}
	var (
		modeRaw    string
		profileRaw []byte
	)
	err = tx.QueryRowContext(ctx, `
SELECT admission_mode, quality_profile
FROM anime_business_configs WHERE managed_anime_id = $1 AND retention_version IS NOT NULL`, animeID).Scan(&modeRaw, &profileRaw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "policy_missing", nil
	}
	if err != nil {
		return nil, "", fmt.Errorf("read retention policy: %w", err)
	}
	mode := AdmissionMode(modeRaw)
	if mode == ModeAll {
		return nil, "admission_all", nil
	}
	profile, err := ParseProfile(profileRaw)
	if err != nil {
		return nil, "", err
	}
	profile = profileForMode(mode, profile)
	candidates, err := loadFallbackCandidatesTx(ctx, tx, animeID)
	if err != nil {
		return nil, "", err
	}
	if len(candidates) == 0 {
		return nil, "no_candidate", nil
	}
	scores, err := loadSubgroupScoresTx(ctx, tx, animeID)
	if err != nil {
		return nil, "", err
	}
	claims, err := loadInFlightClaimsTx(ctx, tx, animeID, "")
	if err != nil {
		return nil, "", err
	}
	claimed := map[string]bool{}
	for _, claim := range claims {
		for _, key := range claim.Keys {
			if mode == ModePerGroup {
				if claim.Group == "" {
					continue
				}
				claimed[key+"|"+claim.Group] = true
				continue
			}
			claimed[key] = true
		}
	}
	pools := map[string][]*fallbackCandidate{}
	for index := range candidates {
		candidate := &candidates[index]
		candidate.facts = candidate.facts.scored(scores)
		group, hasGroup := candidate.facts.poolGroup()
		if mode == ModePerGroup && !hasGroup {
			continue
		}
		for _, key := range candidate.keys {
			pool := key
			if mode == ModePerGroup {
				pool = key + "|" + group
			}
			if claimed[pool] {
				continue
			}
			pools[pool] = append(pools[pool], candidate)
		}
	}
	poolKeys := make([]string, 0, len(pools))
	for key := range pools {
		poolKeys = append(poolKeys, key)
	}
	sort.Strings(poolKeys)
	promoted := map[string]*FallbackPromotion{}
	order := []string{}
	for _, pool := range poolKeys {
		best := pools[pool][0]
		for _, candidate := range pools[pool][1:] {
			if fallbackRanksAbove(candidate, best, profile) {
				best = candidate
			}
		}
		promotion, ok := promoted[best.releaseID]
		if !ok {
			promotion = &FallbackPromotion{ReleaseID: best.releaseID, Version: best.version}
			promoted[best.releaseID] = promotion
			order = append(order, best.releaseID)
		}
		promotion.Pools = append(promotion.Pools, pool)
	}
	out := make([]FallbackPromotion, 0, len(order))
	for _, id := range order {
		out = append(out, *promoted[id])
	}
	if len(out) == 0 {
		return nil, "pools_claimed", nil
	}
	return out, "", nil
}

// fallbackRanksAbove orders two candidates of one pool: the policy profile
// decides, then the earlier discovery, then the id.
func fallbackRanksAbove(a, b *fallbackCandidate, profile Profile) bool {
	comparison := Compare(a.facts, b.facts, profile)
	if comparison.Better {
		return true
	}
	if !comparison.Equal {
		return false
	}
	if !a.discoveredAt.Equal(b.discoveredAt) {
		return a.discoveredAt.Before(b.discoveredAt)
	}
	return a.releaseID < b.releaseID
}

// loadFallbackCandidatesTx reads the fallback input set (see
// FallbackCandidates) with each release's latest comparison facts and the
// coverage keys of its videos skipped as covered_in_flight.
func loadFallbackCandidatesTx(ctx context.Context, tx *sql.Tx, animeID string) ([]fallbackCandidate, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT r.id, r.version, r.discovered_at, m.plan,
       COALESCE(f.resolution, ''), COALESCE(f.revision, ''), COALESCE(f.source, ''),
       COALESCE(f.language, ''), COALESCE(f."group", ''), f.size_bytes,
       COALESCE(f.compare_values, '{}'::jsonb)
FROM releases r
JOIN rss_sources src ON src.id = r.rss_source_id
JOIN subscriptions sub ON sub.id = src.subscription_id
LEFT JOIN provider_group_selections selection
  ON selection.subscription_id = src.subscription_id AND selection.provider = src.provider
 AND selection.group_ref = src.provider_group_ref
JOIN LATERAL (
    SELECT release_facts->'batch_plan' AS plan
    FROM media_fact_snapshots
    WHERE release_id = r.id
    ORDER BY created_at DESC, id DESC
    LIMIT 1
) m ON true
JOIN LATERAL (
    SELECT resolution, revision, source, language, "group", size_bytes, compare_values
    FROM release_candidate_facts
    WHERE release_id = r.id
    ORDER BY release_version DESC
    LIMIT 1
) f ON true
WHERE sub.managed_anime_id = $1
  AND (src.source_kind <> 'provider_group' OR COALESCE(selection.available AND selection.effective_selected, FALSE))
  AND r.download_task_id IS NULL
  AND r.verdict NOT IN ('adopted_existing', 'acquisition_failed', 'resume_baseline', 'start_baseline')
  AND jsonb_typeof(m.plan) = 'object'
  AND jsonb_path_exists(m.plan, '$.files[*] ? (@.outcome == "covered_in_flight")')
ORDER BY r.discovered_at, r.id`, animeID)
	if err != nil {
		return nil, fmt.Errorf("read fallback candidates: %w", err)
	}
	defer rows.Close()
	out := []fallbackCandidate{}
	for rows.Next() {
		var (
			candidate                                     fallbackCandidate
			planRaw, compareRaw                           []byte
			resolution, revision, source, language, title string
			size                                          sql.NullInt64
		)
		if err := rows.Scan(&candidate.releaseID, &candidate.version, &candidate.discoveredAt, &planRaw,
			&resolution, &revision, &source, &language, &title, &size, &compareRaw); err != nil {
			return nil, err
		}
		var plan batchplan.Plan
		if err := json.Unmarshal(planRaw, &plan); err != nil {
			continue
		}
		seen := map[string]bool{}
		for _, file := range plan.Files {
			if file.Outcome != batchplan.OutcomeCoveredInFlight {
				continue
			}
			for _, key := range file.Keys() {
				if !seen[key] {
					seen[key] = true
					candidate.keys = append(candidate.keys, key)
				}
			}
		}
		if len(candidate.keys) == 0 {
			continue
		}
		sort.Strings(candidate.keys)
		candidate.facts = Facts{Resolution: resolution, Revision: revision, Source: source, Language: language, Group: title}
		if size.Valid {
			candidate.facts.Size = &size.Int64
		}
		candidate.facts = candidate.facts.withFrozenValues(compareRaw)
		out = append(out, candidate)
	}
	return out, rows.Err()
}

// FallbackHandler is the replacement_fallback work item handler. It runs the
// fallback query and, in the claim's finish transaction, enqueues a
// release_evaluate for every promoted release that is still unsubmitted. The
// promoted release then runs the whole normal acceptance: filters, the batch
// plan, deduplication, the replacement decision. It has no file effect.
func (s *ReplacementDecisionService) FallbackHandler() worker.Handler {
	return fallbackHandler{service: s}
}

type fallbackHandler struct {
	service *ReplacementDecisionService
}

func (h fallbackHandler) Handle(ctx context.Context, claim workitems.Claim) (worker.Outcome, error) {
	s := h.service
	if s == nil || s.db == nil || s.workItems == nil {
		return worker.Outcome{}, errors.New("replacement fallback is not fully wired")
	}
	if claim.Kind != workitems.KindReplacementFallback || claim.Owner.Type != workitems.OwnerFileMaintenance {
		return worker.Outcome{}, fmt.Errorf("replacement fallback handler received %s/%s", claim.Owner.Type, claim.Kind)
	}
	animeID := claim.Owner.ID
	promotions, reason, err := s.FallbackCandidates(ctx, animeID)
	if err != nil {
		return worker.Outcome{}, err
	}
	if len(promotions) == 0 {
		return worker.Finish(workitems.StateSucceeded, workitems.OwnerResult{State: "idle", ReasonCode: reason}, s.fallbackPromotionWriter(animeID, nil)), nil
	}
	ids := make([]string, 0, len(promotions))
	for _, promotion := range promotions {
		ids = append(ids, promotion.ReleaseID)
	}
	return worker.Finish(workitems.StateSucceeded, workitems.OwnerResult{State: "promoted", ReasonCode: "fallback_promoted", Reason: "releases: " + strings.Join(ids, ",")}, s.fallbackPromotionWriter(animeID, promotions)), nil
}

// ObserveRecovery: the fallback has no external effect; an interrupted claim
// ends failed and the next settle or resume enqueues the query again.
func (h fallbackHandler) ObserveRecovery(ctx context.Context, c workitems.Claim) (worker.Outcome, error) {
	if h.service == nil || c.Kind != workitems.KindReplacementFallback || c.Owner.Type != workitems.OwnerFileMaintenance {
		return worker.Outcome{}, workitems.ErrInvalidSpec
	}
	return recoveryNoEffect(h.service.fallbackPromotionWriter(c.Owner.ID, nil)), nil
}

// fallbackPromotionWriter enqueues the promoted releases inside the finish
// transaction. Lock order follows the release owner writer (subscription,
// then release). A subscription stopped since the query promotes nothing; a
// release submitted or made terminal since the query is skipped (it left the
// input set).
func (s *ReplacementDecisionService) fallbackPromotionWriter(animeID string, promotions []FallbackPromotion) workitems.OwnerWriter {
	return func(ctx context.Context, scope txscope.Tx, _ workitems.OwnerResult) (int64, error) {
		tx := scope.SQLTx()
		if tx == nil {
			return 0, errors.New("replacement fallback transaction is nil")
		}
		var (
			state   string
			version int64
		)
		err := tx.QueryRowContext(ctx, `SELECT state, version FROM subscriptions WHERE managed_anime_id = $1 FOR UPDATE`, animeID).Scan(&state, &version)
		if errors.Is(err, sql.ErrNoRows) {
			return 1, nil
		}
		if err != nil {
			return 0, err
		}
		if state != "active" {
			return version, nil
		}
		for _, promotion := range promotions {
			var (
				current int64
				task    sql.NullString
				verdict string
			)
			if err := tx.QueryRowContext(ctx, `SELECT version, download_task_id, verdict FROM releases WHERE id = $1 FOR UPDATE`, promotion.ReleaseID).Scan(&current, &task, &verdict); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					continue
				}
				return 0, err
			}
			if task.Valid {
				continue
			}
			switch verdict {
			case "adopted_existing", "acquisition_failed", "resume_baseline", "start_baseline":
				continue
			}
			if _, _, err := s.workItems.CreateOrMerge(ctx, tx, workitems.WorkItemSpec{
				Owner:                workitems.OwnerRef{Type: workitems.OwnerRelease, ID: promotion.ReleaseID},
				Kind:                 workitems.KindReleaseEvaluate,
				IdempotencyKey:       "release_evaluate:" + promotion.ReleaseID,
				InputSnapshot:        json.RawMessage(`{"trigger":"` + TriggerReleaseFallback + `"}`),
				MaxAttempts:          releaseEvaluateMaxAttempts,
				ExpectedOwnerVersion: current,
			}); err != nil {
				return 0, fmt.Errorf("enqueue fallback release evaluation: %w", err)
			}
		}
		return version, nil
	}
}

// EnqueueFallbackTx creates or merges the anime's replacement_fallback item
// inside the caller's transaction (the release owner writer when a promoted
// release did not start a download, the resume and scan triggers). Only an
// active subscription enqueues it.
func EnqueueFallbackTx(ctx context.Context, tx *sql.Tx, store *workitems.Store, animeID, reason string) error {
	if store == nil || tx == nil || strings.TrimSpace(animeID) == "" {
		return errors.New("replacement fallback enqueue is not wired")
	}
	var (
		state   string
		version int64
	)
	err := tx.QueryRowContext(ctx, `SELECT state, version FROM subscriptions WHERE managed_anime_id = $1`, animeID).Scan(&state, &version)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if state != "active" {
		return nil
	}
	snapshot, err := json.Marshal(map[string]string{"trigger": reason})
	if err != nil {
		return err
	}
	if version < 1 {
		version = 1
	}
	_, _, err = store.CreateOrMerge(ctx, tx, workitems.WorkItemSpec{
		Owner:                workitems.OwnerRef{Type: workitems.OwnerFileMaintenance, ID: animeID},
		Kind:                 workitems.KindReplacementFallback,
		IdempotencyKey:       "replacement_fallback:" + animeID,
		InputSnapshot:        snapshot,
		MaxAttempts:          triggerMaxAttempts,
		ExpectedOwnerVersion: version,
	})
	if err != nil {
		return fmt.Errorf("enqueue replacement fallback: %w", err)
	}
	return nil
}
