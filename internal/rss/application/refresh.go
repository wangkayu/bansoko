package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	acquisitionapplication "bansoko/internal/acquisition/application"
	acquisitiondomain "bansoko/internal/acquisition/domain"
	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/attrrules"
	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/parserpolicy"
	"bansoko/internal/persistence/txscope"
	providerdomain "bansoko/internal/provider/domain"
	"bansoko/internal/runtime/worker"
	subscriptiondomain "bansoko/internal/subscription/domain"
	"bansoko/internal/workitems"
	"github.com/google/uuid"
)

func (s *Service) handleRefresh(ctx context.Context, claim workitems.Claim) (worker.Outcome, error) {
	if claim.Owner.Type != workitems.OwnerRSSSourceRuntime || strings.TrimSpace(claim.Owner.ID) == "" {
		return worker.Outcome{}, fmt.Errorf("RSS refresh claim must target rss_source_runtime")
	}
	snapshot, err := s.loadSourceSnapshot(ctx, claim.Owner.ID)
	if err != nil {
		// A missing/unusable instance parser policy is an integrity fault: the
		// selected parser can no longer be resolved, and no runtime path may
		// substitute the bundled regex parser. Terminate without touching
		// the source runtime: the owner state is not this item's to rewrite, and
		// nothing is refreshed, evaluated or submitted.
		if errors.Is(err, parserpolicy.ErrInstanceParserPolicyRequired) {
			return worker.Finish(workitems.StateFailed, workitems.OwnerResult{
				State:      workitems.StateFailed,
				ReasonCode: "parser_policy_required",
				Reason:     safeReason(err.Error()),
			}, nil), nil
		}
		// The snapshot JOIN reads rss_sources → subscriptions →
		// anime_metadata with INNER JOINs. When any link is missing (for
		// example a broken test fixture whose subscription has no metadata row,
		// or an anime deleted out from under a pending item), no row is returned
		// and this work item can never succeed. Terminate it instead of retrying
		// forever: retries would keep the item leased indefinitely and spam the
		// log every few minutes (observed as "RSS source not found" for 12h on a
		// fixture without a metadata row). The owner row is gone or unreachable,
		// so there is no owner state to write.
		if errors.Is(err, sql.ErrNoRows) {
			return worker.Finish(workitems.StateFailed, workitems.OwnerResult{State: workitems.StateFailed, ReasonCode: "source_unavailable", Reason: safeReason(err.Error())}, nil), nil
		}
		return worker.Outcome{}, err
	}
	ineligibility := subscriptiondomain.EvaluateSourceRefreshEligibility(subscriptiondomain.SourceRefreshEligibilityFacts{
		SubscriptionState:       subscriptiondomain.SubscriptionState(snapshot.subscriptionState),
		ProviderSource:          snapshot.sourceKind == animedomain.SourceKindProviderGroup,
		ProviderSelectionExists: snapshot.selectionExists,
		ProviderSelected:        snapshot.providerSelected,
	})
	// Stopped blocks new automatic work only. An explicit manual refresh
	// ({"trigger":"manual"}) is an allowed manual command and proceeds;
	// dispatcher- and subscribe-scheduled items still no-op here. A pending
	// (never started) subscription has no manual exemption: every refresh
	// no-ops with subscription_pending.
	if ineligibility == subscriptiondomain.SourceRefreshSubscriptionStopped && refreshTriggerIsManual(claim.InputSnapshot) {
		ineligibility = subscriptiondomain.SourceRefreshEligible
	}
	if ineligibility != subscriptiondomain.SourceRefreshEligible {
		return worker.Finish(workitems.StateSucceeded, workitems.OwnerResult{State: "ready", ReasonCode: string(ineligibility)}, nil), nil
	}
	if snapshot.sourceKind == animedomain.SourceKindCustomRss {
		if _, locatorErr := animedomain.LocatorURL(snapshot.locator); locatorErr != nil {
			reasonCode := "feed_locator_unsafe"
			if errors.Is(locatorErr, subscriptiondomain.ErrRSSLocatorSchemeUnsupported) {
				reasonCode = subscriptiondomain.RSSLocatorSchemeUnsupportedCode
				locatorErr = errors.New("RSS locator scheme is not supported for execution")
			}
			return s.refreshTerminalFailure(ctx, claim, snapshot, reasonCode, locatorErr), nil
		}
	}
	exchange := &acquisitionapplication.FeedExchange{
		ETag:                 snapshot.etag,
		LastModified:         snapshot.lastModified,
		ReuseTorrentMetadata: s.torrentMetadataReuser(ctx, snapshot),
	}
	var items []acquisitiondomain.FeedItem
	items, err = s.sourceFetcher.FetchSource(acquisitionapplication.WithFeedExchange(ctx, exchange), SourceLocator{
		Kind:               snapshot.sourceKind,
		URL:                snapshot.locator,
		HTTPFeedConnection: snapshot.httpFeedConnection,
		NetworkRoute:       snapshot.networkRoute,
		Provider:           snapshot.provider,
		ProviderEntryRef:   snapshot.providerEntryRef,
		ProviderGroupRef:   snapshot.providerGroupRef,
	})
	if errors.Is(err, acquisitionapplication.ErrFeedNotModified) {
		effect := &circuitEffect{}
		return worker.WithAfterCommit(
			worker.Finish(workitems.StateSucceeded, workitems.OwnerResult{State: "ready", ReasonCode: "refresh_not_modified"}, s.refreshNotModifiedWriter(claim, snapshot, effect)),
			s.circuitAfterCommit(snapshot, effect, nil)), nil
	}
	if err != nil {
		reason := safeReason(err.Error())
		if reason == "" {
			reason = "RSS source fetch failed"
		}
		code, runtimeDetail := "feed_fetch_failed", reason
		// A feed that was read but yields nothing usable (every item skipped, or
		// a listed site whose format changed) keeps its plain summary on the
		// work item; the runtime detail carries the stable code the RSS page
		// names it by.
		variant := observabilityport.VariantRefreshFailed
		var failure *acquisitiondomain.FeedFailure
		if errors.As(err, &failure) {
			code, runtimeDetail = failure.Code, failure.Code
			variant = observabilityport.VariantUnreadable
		}
		effect := &circuitEffect{}
		return worker.WithAfterCommit(
			worker.Retry(code, reason, subscriptiondomain.RefreshBackoff(snapshot.consecutiveFailures+1), s.refreshFailureWriter(claim, snapshot, runtimeDetail, effect)),
			s.circuitAfterCommit(snapshot, effect, &refreshFailureNote{err: err, variant: variant})), nil
	}

	feed, err := buildFeedEvaluations(ctx, items, snapshot)
	if err != nil {
		effect := &circuitEffect{}
		return worker.WithAfterCommit(
			worker.Retry("feed_parse_failed", safeReason(err.Error()), subscriptiondomain.RefreshBackoff(snapshot.consecutiveFailures+1), s.refreshFailureWriter(claim, snapshot, safeReason(err.Error()), effect)),
			s.circuitAfterCommit(snapshot, effect, &refreshFailureNote{err: err, variant: observabilityport.VariantUnreadable})), nil
	}
	effect := &circuitEffect{}
	return worker.WithAfterCommit(
		worker.Finish(workitems.StateSucceeded, workitems.OwnerResult{State: "ready", ReasonCode: "refresh_succeeded"}, s.refreshSuccessWriter(claim, snapshot, feed, completeFeedValidators(exchange, feed), effect)),
		s.circuitAfterCommit(snapshot, effect, nil)), nil
}

// refreshTriggerIsManual reports whether the WorkItem carries an explicit
// manual command. Manual refreshes are the only items allowed to run while
// the Subscription is stopped; dispatcher items carry no input snapshot and
// subscribe-scheduled items use their own trigger value.
func refreshTriggerIsManual(snapshot json.RawMessage) bool {
	var input struct {
		Trigger string `json:"trigger"`
	}
	if err := json.Unmarshal(snapshot, &input); err != nil {
		return false
	}
	return input.Trigger == "manual"
}

func buildFeedEvaluations(ctx context.Context, items []acquisitiondomain.FeedItem, snapshot sourceSnapshot) ([]feedEvaluation, error) {
	result := make([]feedEvaluation, 0, len(items))
	input := animedomain.SourceEvaluationInput{
		Index: 0, GroupName: snapshot.groupName, Offset: snapshot.episodeOffset,
		Group: providerdomain.GroupRefOrZero(providerdomain.Kind(snapshot.provider), snapshot.providerGroupRef),
	}
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		kind, stableKey := publicStableItemKey(item)
		seenKey := kind + "\x00" + stableKey
		if _, exists := seen[seenKey]; exists {
			continue
		}
		seen[seenKey] = struct{}{}
		item = refuseOversizedTorrent(item)
		rules, _, err := attrrules.Current()
		if err != nil {
			return nil, fmt.Errorf("read active attr rules: %w", err)
		}
		// One selected parser/profile governs this discovery pass. An
		// unavailable/offline selection fails this item closed with a typed
		// reason instead of degrading to the bundled parser; it never becomes a
		// scalar episode.
		identity := parserUnavailableIdentity()
		evidence, evidenceErr := resolveSourceParserEvidence(ctx, snapshot, item.Title)
		if evidenceErr == nil {
			identity = evidence.Identity
		}
		evaluation := animedomain.EvaluateFeedItemWithIdentity(item, input, snapshot.effective, snapshot.metadataHold, rules, identity)
		switch {
		case evidenceErr != nil:
			evaluation.Verdict = animedomain.ReleaseVerdict("parse_failed")
			evaluation.ReasonCode = "parser_selection_unavailable"
			detail := "the selected parser/profile is unavailable"
			evaluation.ReasonDetail = &detail
		case item.TorrentErrorCode != "":
			markTorrentUnavailable(&evaluation, item)
		}
		evaluation.StableItemKey = stableKey
		rawFacts, err := encodeFeedFacts(item)
		if err != nil {
			return nil, fmt.Errorf("encode feed facts: %w", err)
		}
		internalFacts, err := encodeInternalFeedFacts(item)
		if err != nil {
			return nil, fmt.Errorf("encode internal feed facts: %w", err)
		}
		_, _, sourceFacts, err := encodeSourceFacts(item, snapshot.groupName)
		if err != nil {
			return nil, fmt.Errorf("encode source facts: %w", err)
		}
		parsedFacts, err := encodeParsedFacts(evaluation)
		if err != nil {
			return nil, fmt.Errorf("encode parsed facts: %w", err)
		}
		result = append(result, feedEvaluation{
			item:          item,
			result:        evaluation,
			stableKind:    kind,
			stableKey:     stableKey,
			rawFacts:      rawFacts,
			sourceFacts:   sourceFacts,
			parsedFacts:   parsedFacts,
			internalFacts: internalFacts,
		})
	}
	return result, nil
}

func (s *Service) refreshSuccessWriter(claim workitems.Claim, snapshot sourceSnapshot, feed []feedEvaluation, validators feedValidators, effect *circuitEffect) workitems.OwnerWriter {
	return func(ctx context.Context, scope txscope.Tx, _ workitems.OwnerResult) (int64, error) {
		tx := scope.SQLTx()
		if tx == nil {
			return 0, errors.New("RSS refresh owner writer transaction is nil")
		}
		var state string
		if err := tx.QueryRowContext(ctx, `SELECT state FROM subscriptions WHERE id=$1 FOR UPDATE`, snapshot.subscriptionID).Scan(&state); err != nil {
			return 0, err
		}
		selected, err := sourceScopeSelected(ctx, tx, snapshot.sourceID, true)
		if err != nil {
			return 0, err
		}
		if !selected || (state != "active" && !refreshTriggerIsManual(claim.InputSnapshot)) {
			return snapshot.runtimeVersion, nil
		}
		// Releases first seen after the source's baseline scan, while the
		// subscription is active, may notify once they pass filtering. The
		// first successful refresh after a resume only records the baseline:
		// the feed window is stored, nothing is discovered, evaluated or
		// acquired.
		discoveries, resumeBaseline := false, false
		if state == "active" {
			if resumeBaseline, err = sourceResumeBaselinePendingTx(ctx, tx, snapshot.sourceID); err != nil {
				return 0, err
			}
			if !resumeBaseline {
				if discoveries, err = sourceHasBaselineTx(ctx, tx, snapshot.sourceID); err != nil {
					return 0, err
				}
			}
		}
		newKeys := make([]string, 0)
		for index := range feed {
			evaluation := &feed[index]
			releaseID, releaseVersion, created, changed, inputMutable, err := upsertRelease(ctx, tx, snapshot, evaluation)
			if err != nil {
				return 0, err
			}
			if created && resumeBaseline {
				// Stored as history: the durable verdict keeps every
				// evaluation path (refresh, re-evaluation, retry) away.
				if _, err := tx.ExecContext(ctx, `
UPDATE releases SET verdict = $2, reason_code = $2, reason_detail = NULL WHERE id = $1`, releaseID, ReleaseVerdictResumeBaseline); err != nil {
					return 0, fmt.Errorf("mark resume baseline release: %w", err)
				}
			}
			if created {
				newKeys = append(newKeys, evaluation.stableKey)
				if discoveries {
					if err := markReleaseDiscoveryTx(ctx, tx, releaseID); err != nil {
						return 0, err
					}
				}
			}
			if inputMutable {
				if err := persistReleaseTorrentInput(ctx, tx, releaseID, evaluation.item); err != nil {
					return 0, err
				}
			}
			// Torrent identity writes can advance the same Release version after
			// the facts update. Schedule against the final transaction-local value.
			if err := tx.QueryRowContext(ctx, `SELECT version FROM releases WHERE id=$1`, releaseID).Scan(&releaseVersion); err != nil {
				return 0, fmt.Errorf("read release version after torrent input: %w", err)
			}
			if !resumeBaseline && (created || changed) && (evaluation.item.TorrentErrorCode == "" || evaluation.torrentMetadataReused) {
				if err := CreateOrMergeReleaseEvaluation(ctx, txscope.Wrap(tx), s.workItems, releaseID, releaseVersion, "rss_refresh"); err != nil {
					return 0, fmt.Errorf("schedule release evaluation: %w", err)
				}
			}
		}
		cursor, lastGood := refreshFacts(feed, snapshot)
		cursorJSON, err := json.Marshal(cursor)
		if err != nil {
			return 0, fmt.Errorf("encode RSS cursor: %w", err)
		}
		lastGoodJSON, err := json.Marshal(lastGood)
		if err != nil {
			return 0, fmt.Errorf("encode RSS last-good facts: %w", err)
		}
		var newVersion int64
		if err := tx.QueryRowContext(ctx, `
UPDATE rss_source_runtime
SET cursor = $2, last_good_facts = $3, last_refresh_at = CURRENT_TIMESTAMP,
    last_success_at = CURRENT_TIMESTAMP, health = 'ready', reason_code = NULL,
    reason_detail = NULL, etag = NULLIF($5, ''), last_modified = NULLIF($6, ''),
    resume_baseline_at = CASE WHEN $7 THEN (SELECT resumed_at FROM subscriptions WHERE id = $8) ELSE resume_baseline_at END,
    version = version + 1, updated_at = CURRENT_TIMESTAMP
WHERE rss_source_id = $1 AND version = $4
RETURNING version`, snapshot.sourceID, cursorJSON, lastGoodJSON, claim.ExpectedOwnerVersion, validators.etag, validators.lastModified, resumeBaseline, snapshot.subscriptionID).Scan(&newVersion); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return 0, fmt.Errorf("RSS source runtime version conflict")
			}
			return 0, fmt.Errorf("update RSS source runtime: %w", err)
		}
		if err := resetRefreshFailuresTx(ctx, tx, snapshot, effect); err != nil {
			return 0, err
		}
		if len(newKeys) > 0 && !resumeBaseline {
			count := len(newKeys)
			details, err := marshalActivityDetails(map[string]any{"item_count": count})
			if err != nil {
				return 0, err
			}
			if err := appendActivity(ctx, tx, s.activityWriter, observabilityport.ActivityRecord{
				SubscriptionID:  snapshot.subscriptionID,
				ManagedAnimeID:  snapshot.managedAnimeID,
				Kind:            rssLogKindNewContent,
				Outcome:         rssLogOutcomeInfo,
				ReasonCode:      "rss_new_content",
				SubjectType:     "rss_source",
				SubjectID:       snapshot.sourceID,
				SubjectVersion:  newVersion,
				StableResultKey: digestStableKeys(newKeys),
				RSSSourceID:     snapshot.sourceID,
				GroupRef:        snapshot.groupName,
				MessageCode:     observabilityport.MessageRSSNewContent,
				MessageParams:   map[string]any{observabilityport.ParamCount: count},
				Details:         details,
			}); err != nil {
				return 0, err
			}
		}
		return newVersion, nil
	}
}

func (s *Service) refreshOwnerWriter(claim workitems.Claim, snapshot sourceSnapshot, health, reasonCode, reason string, feed []feedEvaluation) workitems.OwnerWriter {
	return func(ctx context.Context, scope txscope.Tx, result workitems.OwnerResult) (int64, error) {
		tx := scope.SQLTx()
		if tx == nil {
			return 0, errors.New("RSS refresh owner writer transaction is nil")
		}
		if feed != nil {
			return s.refreshSuccessWriter(claim, snapshot, feed, feedValidators{}, nil)(ctx, scope, result)
		}
		ownerHealth := health
		if result.State == workitems.StateFailed {
			ownerHealth = "failed"
		}
		var version int64
		if err := tx.QueryRowContext(ctx, `
UPDATE rss_source_runtime
SET last_refresh_at = CURRENT_TIMESTAMP, health = $2, reason_code = $3,
    reason_detail = $4, version = version + 1, updated_at = CURRENT_TIMESTAMP
WHERE rss_source_id = $1 AND version = $5
RETURNING version`, snapshot.sourceID, ownerHealth, reasonCode, nullableString(safeReason(reason)), claim.ExpectedOwnerVersion).Scan(&version); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return 0, fmt.Errorf("RSS source runtime version conflict")
			}
			return 0, fmt.Errorf("update RSS failure runtime: %w", err)
		}
		return version, nil
	}
}

func (s *Service) refreshTerminalFailure(ctx context.Context, claim workitems.Claim, snapshot sourceSnapshot, reasonCode string, err error) worker.Outcome {
	reason := safeReason(err.Error())
	outcome := worker.Finish(workitems.StateFailed, workitems.OwnerResult{State: workitems.StateFailed, ReasonCode: reasonCode, Reason: reason}, s.refreshOwnerWriter(claim, snapshot, "failed", reasonCode, reason, nil))
	return worker.WithAfterCommit(outcome, observabilityport.DiagnosticFailureAfterCommit(s.diagnostics, s.logger,
		observabilityport.InternalFailure(observabilityport.InternalRSSRefresh, "")))
}

func refreshFacts(feed []feedEvaluation, snapshot sourceSnapshot) (rssCursor, rssLastGoodFacts) {
	lastGood := rssLastGoodFacts{ItemCount: len(feed)}
	if len(feed) == 0 {
		var cursor rssCursor
		if len(snapshot.cursorJSON) > 0 {
			_ = json.Unmarshal(snapshot.cursorJSON, &cursor)
		}
		var previous rssLastGoodFacts
		if len(snapshot.lastGoodJSON) > 0 {
			_ = json.Unmarshal(snapshot.lastGoodJSON, &previous)
		}
		return cursor, previous
	}
	ordered := append([]feedEvaluation(nil), feed...)
	sort.SliceStable(ordered, func(i, j int) bool {
		left := ordered[i].item.Published
		right := ordered[j].item.Published
		if !left.Equal(right) {
			return left.After(right)
		}
		return ordered[i].stableKey < ordered[j].stableKey
	})
	newest := ordered[0]
	lastGood.NewestStableItemKey = newest.stableKey
	strategy := newest.stableKind
	if strategy != rssCursorStrategyGuid && strategy != rssCursorStrategyEnclosure && strategy != rssCursorStrategyLink && strategy != rssCursorStrategyContentHash {
		strategy = rssCursorStrategyContentHash
	}
	return rssCursor{Strategy: strategy, Value: newest.stableKey}, lastGood
}

func upsertRelease(ctx context.Context, tx *sql.Tx, snapshot sourceSnapshot, evaluation *feedEvaluation) (releaseID string, version int64, created, changed, inputMutable bool, err error) {
	parsed := nullableJSON(evaluation.parsedFacts)
	reasonDetail := safeReason(pointerString(evaluation.result.ReasonDetail))
	candidateID := newID()
	insertErr := tx.QueryRowContext(ctx, `
INSERT INTO releases(
    id, rss_source_id, stable_item_key, raw_facts, source_facts, parsed_facts,
    internal_facts, verdict, reason_code, reason_detail, evaluation_version
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, NULLIF($10, ''), $11)
ON CONFLICT (rss_source_id, stable_item_key) DO NOTHING
RETURNING id, version`, candidateID, snapshot.sourceID, evaluation.stableKey,
		evaluation.rawFacts, evaluation.sourceFacts, parsed, evaluation.internalFacts,
		string(evaluation.result.Verdict), evaluation.result.ReasonCode, reasonDetail, evaluationVersionString()).Scan(&releaseID, &version)
	if insertErr == nil {
		inputMutable := evaluation.item.TorrentErrorCode == "" && len(evaluation.item.TorrentPayload) > 0
		return releaseID, version, true, true, inputMutable, nil
	}
	if !errors.Is(insertErr, sql.ErrNoRows) {
		return "", 0, false, false, false, fmt.Errorf("insert or find release: %w", insertErr)
	}

	// WorkItem completion locks the WorkItem before its owner row. Match that
	// order here so a refresh never invalidates an evaluation that is already
	// finishing. The release is then re-read under lock for the current version.
	var existingID string
	if err := tx.QueryRowContext(ctx, `
SELECT id
FROM releases
WHERE rss_source_id = $1 AND stable_item_key = $2`, snapshot.sourceID, evaluation.stableKey).Scan(&existingID); err != nil {
		return "", 0, false, false, false, fmt.Errorf("find existing release: %w", err)
	}
	liveID, liveState, _, liveFound, err := lockLiveReleaseEvaluation(ctx, tx, existingID)
	if err != nil {
		return "", 0, false, false, false, err
	}
	var existingTask sql.NullString
	var existingVerdict string
	var existingInternalFacts []byte
	if err := tx.QueryRowContext(ctx, `
SELECT id, version, download_task_id, verdict, internal_facts
FROM releases
WHERE id = $1
FOR UPDATE`, existingID).Scan(&releaseID, &version, &existingTask, &existingVerdict, &existingInternalFacts); err != nil {
		return "", 0, false, false, false, fmt.Errorf("lock existing release: %w", err)
	}
	if existingTask.Valid || isTerminalReleaseVerdict(existingVerdict) {
		return releaseID, version, false, false, false, nil
	}
	if liveFound && liveState == workitems.StateLeased {
		// The claimed evaluator owns the current release version. Let it finish;
		// the next refresh will observe and persist newer feed facts.
		return releaseID, version, false, false, false, nil
	}

	if evaluation.item.TorrentErrorCode == "torrent_metadata_unavailable" && len(evaluation.item.ReleaseFiles) == 0 {
		input, inputErr := loadReleaseTorrentInputRef(ctx, tx, releaseID)
		if inputErr == nil {
			item, reused := reuseVerifiedTorrentMetadata(existingInternalFacts, evaluation.item, input)
			if reused {
				internalFacts, encodeErr := encodeInternalFeedFacts(item)
				if encodeErr != nil {
					return "", 0, false, false, false, fmt.Errorf("encode reused torrent facts: %w", encodeErr)
				}
				evaluation.item = item
				evaluation.internalFacts = internalFacts
				evaluation.torrentMetadataReused = true
			}
		} else if !errors.Is(inputErr, errTorrentPayloadMissing) && !errors.Is(inputErr, errTorrentPayloadInvalid) {
			return "", 0, false, false, false, inputErr
		}
	}

	var updatedVersion int64
	updateErr := tx.QueryRowContext(ctx, `
UPDATE releases
SET raw_facts = $2, source_facts = $3, parsed_facts = $4, internal_facts = $5,
    verdict = $6, reason_code = $7, reason_detail = NULLIF($8, ''),
    evaluation_version = $9, version = version + 1, updated_at = CURRENT_TIMESTAMP
WHERE id = $1 AND download_task_id IS NULL
  AND (raw_facts IS DISTINCT FROM CAST($2 AS jsonb)
    OR source_facts IS DISTINCT FROM CAST($3 AS jsonb)
    OR parsed_facts IS DISTINCT FROM CAST($4 AS jsonb)
    OR internal_facts IS DISTINCT FROM CAST($5 AS jsonb)
    OR verdict IS DISTINCT FROM $6
    OR reason_code IS DISTINCT FROM $7
    OR reason_detail IS DISTINCT FROM NULLIF($8, '')
    OR evaluation_version IS DISTINCT FROM $9)
RETURNING version`, releaseID, evaluation.rawFacts, evaluation.sourceFacts, parsed, evaluation.internalFacts,
		string(evaluation.result.Verdict), evaluation.result.ReasonCode, reasonDetail, evaluationVersionString()).Scan(&updatedVersion)
	inputMutable = evaluation.item.TorrentErrorCode == "" && len(evaluation.item.TorrentPayload) > 0
	if errors.Is(updateErr, sql.ErrNoRows) {
		if liveFound && liveState == workitems.StateAvailable && liveID != "" {
			if err := syncReleaseEvaluationVersion(ctx, tx, liveID, version); err != nil {
				return "", 0, false, false, false, err
			}
		}
		return releaseID, version, false, false, inputMutable, nil
	}
	if updateErr != nil {
		return "", 0, false, false, false, fmt.Errorf("update release: %w", updateErr)
	}
	if liveFound && liveState == workitems.StateAvailable && liveID != "" {
		if err := syncReleaseEvaluationVersion(ctx, tx, liveID, updatedVersion); err != nil {
			return "", 0, false, false, false, err
		}
	}
	return releaseID, updatedVersion, false, true, inputMutable, nil
}

func lockLiveReleaseEvaluation(ctx context.Context, tx *sql.Tx, releaseID string) (id, state string, expectedVersion int64, found bool, err error) {
	err = workitems.LockReleaseEvaluationTx(ctx, tx, workitems.OwnerRelease, releaseID, workitems.KindReleaseEvaluate).Scan(&id, &state, &expectedVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", 0, false, nil
	}
	if err != nil {
		return "", "", 0, false, fmt.Errorf("lock release evaluation work item: %w", err)
	}
	return id, state, expectedVersion, true, nil
}

func syncReleaseEvaluationVersion(ctx context.Context, tx *sql.Tx, workItemID string, version int64) error {
	if err := workitems.SynchronizeAvailableReleaseVersionTx(ctx, tx, workItemID, version); err != nil {
		return fmt.Errorf("sync release evaluation version: %w", err)
	}
	return nil
}

// CreateOrMergeReleaseEvaluation enqueues a release_evaluate WorkItem for a
// release if no live (available/leased) one exists. trigger is recorded in the
// input snapshot for observability (for example "rss_refresh").
func CreateOrMergeReleaseEvaluation(ctx context.Context, scope txscope.Tx, store *workitems.Store, releaseID string, releaseVersion int64, trigger string) error {
	tx := scope.SQLTx()
	if tx == nil {
		return errors.New("release evaluation transaction is nil")
	}
	return createOrMergeReleaseEvaluationRaw(ctx, tx, store, releaseID, releaseVersion, trigger)
}

func createOrMergeReleaseEvaluationRaw(ctx context.Context, tx *sql.Tx, store *workitems.Store, releaseID string, releaseVersion int64, trigger string) error {
	if store == nil {
		return errors.New("work item store is nil")
	}
	var verdict string
	if err := tx.QueryRowContext(ctx, `SELECT verdict FROM releases WHERE id = $1 FOR UPDATE`, releaseID).Scan(&verdict); err != nil {
		return fmt.Errorf("read release verdict for evaluation: %w", err)
	}
	if isTerminalReleaseVerdict(verdict) {
		return nil
	}
	snapshot := json.RawMessage(`{"trigger":"` + trigger + `"}`)
	_, _, err := store.CreateOrMerge(ctx, tx, workitems.WorkItemSpec{
		Owner:                workitems.OwnerRef{Type: workitems.OwnerRelease, ID: releaseID},
		Kind:                 workitems.KindReleaseEvaluate,
		IdempotencyKey:       "release_evaluate:" + releaseID,
		InputSnapshot:        snapshot,
		MaxAttempts:          defaultMaxAttempts,
		ExpectedOwnerVersion: releaseVersion,
	})
	return err
}

func pointerString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func evaluationVersionString() string {
	return evaluationVersion
}

func newID() string {
	return uuid.NewString()
}
