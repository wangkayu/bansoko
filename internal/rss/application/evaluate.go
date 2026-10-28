package application

import (
	facts "bansoko/internal/release/facts"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	"bansoko/internal/acquisition/target"
	terminal "bansoko/internal/acquisition/terminal/application"
	animedomain "bansoko/internal/anime/domain"
	draft "bansoko/internal/anime/draft/application"
	"bansoko/internal/attrrules"
	"bansoko/internal/batchplan"
	"bansoko/internal/observability/businesslog"
	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/parser"
	"bansoko/internal/persistence/txscope"
	providerdomain "bansoko/internal/provider/domain"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/subscription/competition"
	subscriptiondomain "bansoko/internal/subscription/domain"
	"bansoko/internal/workitems"
	"github.com/google/uuid"
)

func isTerminalReleaseVerdict(verdict string) bool {
	return verdict == "adopted_existing" || verdict == "acquisition_failed" || verdict == ReleaseVerdictResumeBaseline || verdict == ReleaseVerdictStartBaseline
}

// ReleaseVerdictResumeBaseline marks a Release first stored by the baseline
// refresh after a resume. It was present in the feed
// when the subscription resumed, so it is history: never discovered, never
// evaluated, never acquired, by any automatic or manual path.
const ReleaseVerdictResumeBaseline = "resume_baseline"

func (s *Service) handleReleaseEvaluate(ctx context.Context, claim workitems.Claim) (worker.Outcome, error) {
	if claim.Owner.Type != workitems.OwnerRelease || strings.TrimSpace(claim.Owner.ID) == "" {
		return worker.Outcome{}, fmt.Errorf("release evaluation claim must target release")
	}
	snapshot, err := s.loadReleaseSnapshot(ctx, claim.Owner.ID)
	if err != nil {
		// Mirror the refresh handler: when the release snapshot JOIN can no
		// longer resolve its owner chain (release → source → subscription →
		// metadata), the item is orphaned and can never succeed. Terminate it
		// rather than retrying forever.
		if errors.Is(err, sql.ErrNoRows) {
			return worker.Finish(workitems.StateFailed, workitems.OwnerResult{State: workitems.StateFailed, ReasonCode: "release_unavailable", Reason: safeReason(err.Error())}, nil), nil
		}
		return worker.Outcome{}, err
	}
	if isTerminalReleaseVerdict(snapshot.verdict) {
		return worker.Finish(workitems.StateSucceeded, workitems.OwnerResult{
			State:      snapshot.verdict,
			ReasonCode: "release_terminal",
			Reason:     "release has a terminal acquisition verdict",
		}, nil), nil
	}
	if snapshot.downloadTaskID.Valid {
		return worker.Finish(workitems.StateSucceeded, workitems.OwnerResult{State: "submitted", ReasonCode: "release_already_submitted"}, nil), nil
	}
	// A pending (never started) subscription does no work: the item ends
	// without writing a verdict, so the Release stays undecided until a start.
	if snapshot.subscriptionState == string(subscriptiondomain.SubscriptionPending) {
		return worker.Finish(workitems.StateSucceeded, workitems.OwnerResult{State: "ready", ReasonCode: string(subscriptiondomain.SourceRefreshSubscriptionPending)}, nil), nil
	}
	// A refresh does not fetch the torrent of an item the feed-layer filter
	// rejects; once the item passes (a rules change), it is fetched here so
	// the evaluation judges it with its file list.
	claim, snapshot, err = s.ensureEvaluationTorrentInput(ctx, claim, snapshot)
	if err != nil {
		return worker.Outcome{}, err
	}
	return s.evaluateRelease(ctx, claim, snapshot)
}

// evaluateRelease is the one Release evaluation: the release_evaluate worker
// and the explicit retry command both run it, so a retry reaches exactly the
// verdicts automatic acceptance reaches. The snapshot's manualRetry flag only
// lets a stopped subscription accept the Release; filters, the metadata hold,
// the batch plan, deduplication and the replacement decision all apply.
func (s *Service) evaluateRelease(ctx context.Context, claim workitems.Claim, snapshot releaseSnapshot) (worker.Outcome, error) {
	// The instance's frozen parser policy is required before any automatic
	// admission step. A missing or unusable row terminates the item with the
	// typed integrity reason: the parser never runs, no Release verdict is
	// written, and nothing is submitted. Submitted/terminal work returned above
	// is unaffected.
	selected, err := sourceScopeSelected(ctx, s.db, snapshot.sourceID, false)
	if err != nil {
		return worker.Outcome{}, err
	}
	if !selected {
		return worker.Finish(workitems.StateSucceeded, workitems.OwnerResult{State: "not_selected", ReasonCode: "provider_group_not_selected"}, nil), nil
	}
	if snapshot.parserPolicyError != nil {
		return worker.Finish(workitems.StateFailed, workitems.OwnerResult{
			State:      workitems.StateFailed,
			ReasonCode: "parser_policy_required",
			Reason:     safeReason(snapshot.parserPolicyError.Error()),
		}, nil), nil
	}
	item, err := decodeFeedItemWithInternal(snapshot.rawFacts, snapshot.internalFacts, snapshot.sourceFacts, snapshot.parsedFacts, snapshot.stableItemKey)
	if err != nil {
		return worker.Finish(workitems.StateFailed, workitems.OwnerResult{State: workitems.StateFailed, ReasonCode: "feed_item_unavailable", Reason: safeReason(err.Error())}, s.releaseFailureWriter(claim, snapshot, "feed_item_unavailable", safeReason(err.Error()))), nil
	}
	// New-releases-only: a Release published before the instance was created, or
	// without a publish time, is recorded as start_baseline before any rule
	// runs. An explicit retry skips only this gate and evaluates normally.
	gated, gateErr := startBaselineApplies(snapshot, item.Published)
	if gateErr != nil {
		return worker.Finish(workitems.StateFailed, workitems.OwnerResult{
			State:      workitems.StateFailed,
			ReasonCode: "start_baseline_missing",
			Reason:     safeReason(gateErr.Error()),
		}, nil), nil
	}
	if gated {
		return worker.Finish(workitems.StateSucceeded, workitems.OwnerResult{State: ReleaseVerdictStartBaseline, ReasonCode: ReleaseVerdictStartBaseline}, s.startBaselineWriter(claim, snapshot)), nil
	}
	rules, _, err := attrrules.Current()
	if err != nil {
		return worker.Outcome{}, fmt.Errorf("read active attr rules: %w", err)
	}
	// Exactly one parser selection is resolved per evaluation. It is the single
	// source of the Release verdict, the target/naming plan, the frozen media
	// facts, and the DownloadTask policy, so the selected parser runs once and
	// no second implementation can decide an identity. An unknown/offline kind
	// or an unusable config fails the Release closed instead of degrading to
	// the bundled parser.
	evidence, evidenceErr := resolveReleaseParserEvidence(ctx, snapshot, item.Title)
	if evidenceErr != nil {
		reason := safeReason(evidenceErr.Error())
		if reason == "" {
			reason = "the selected parser/profile is unavailable"
		}
		return worker.Finish(workitems.StateFailed, workitems.OwnerResult{
			State:      workitems.StateFailed,
			ReasonCode: "parser_selection_unavailable",
			Reason:     reason,
		}, s.releaseFailureWriter(claim, snapshot, "parser_selection_unavailable", reason)), nil
	}
	result := animedomain.EvaluateFeedItemWithIdentity(item, animedomain.SourceEvaluationInput{
		Index:     0,
		GroupName: snapshot.groupName,
		Group:     providerdomain.GroupRefOrZero(providerdomain.Kind(snapshot.sourceProvider.String), snapshot.sourceGroupRef.String),
		Offset:    snapshot.episodeOffset,
	}, snapshot.effective, snapshot.metadataHold, rules, evidence.Identity)
	if !snapshot.admitsWork(snapshot.subscriptionState) {
		result.Verdict = animedomain.ReleaseVerdict("subscription_stopped")
		result.ReasonCode = "subscription_stopped"
		detail := "subscription is stopped"
		result.ReasonDetail = &detail
	} else if snapshot.metadataHold.Held {
		result.Verdict = animedomain.ReleaseVerdict("waiting_metadata")
		result.ReasonCode = "metadata_incomplete"
		missing := make([]string, 0, len(snapshot.metadataHold.Missing))
		for _, value := range snapshot.metadataHold.Missing {
			missing = append(missing, string(value))
		}
		detail := "missing: " + strings.Join(missing, ", ")
		result.ReasonDetail = &detail
	}
	// The batch plan needs the torrent file list: without one nothing can be
	// planned. A fetch failure that still carries the verified list of an
	// earlier fetch may recover inside the owner transaction, so only the
	// list's absence decides here.
	if len(item.ReleaseFiles) == 0 {
		markTorrentUnavailable(&result, item)
	}
	// Plan stage: with the competition wired, an active subscription's
	// downloadable release stops here. Its plain batch plan is frozen and it
	// waits for the instance's competition, which decides every admission.
	if s.awaitsCompetition(snapshot) && result.Verdict == animedomain.ReleaseVerdict("accepted_pending_download") {
		markAwaitingCompetition(&result)
		return worker.Finish(workitems.StateSucceeded, workitems.OwnerResult{State: string(result.Verdict), ReasonCode: result.ReasonCode}, s.releaseOwnerWriter(claim, snapshot, item, result, nil, &evidence)), nil
	}
	acquisition := s.preflightTargetAcquisition(ctx, snapshot, item, result, &evidence)
	if acquisition != nil && acquisition.varsInterrupted != nil {
		// A time-limit interrupt says nothing about the program: freeze
		// nothing and evaluate the release again.
		return worker.Retry("naming_vars_interrupted", safeReason(acquisition.varsInterrupted.Error()), s.retryAfter, s.releaseAdmissionRetryWriter(claim, snapshot)), nil
	}
	if acquisition != nil && acquisition.downloaderError != nil {
		reason := safeReason(acquisition.downloaderError.Error())
		if reason == "" {
			reason = "qB admission lookup failed"
		}
		return worker.Retry("qb_admission_lookup_failed", reason, s.retryAfter, s.releaseAdmissionRetryWriter(claim, snapshot)), nil
	}
	if acquisition != nil {
		switch acquisition.admission.Admission {
		case target.AdmissionAdopted:
			result.Verdict = animedomain.ReleaseVerdict("adopted_existing")
			result.ReasonCode = acquisition.admission.Reason
			detail := "all rendered targets already exist with the exact entry sizes"
			result.ReasonDetail = &detail
		case target.AdmissionFailed:
			result.Verdict = animedomain.ReleaseVerdict("acquisition_failed")
			result.ReasonCode = acquisition.admission.Reason
			detail := acquisition.planReason
			if detail == "" {
				detail = "target acquisition admission failed"
			}
			result.ReasonDetail = &detail
		case target.AdmissionSkipped:
			result.Verdict = animedomain.ReleaseVerdict("filtered")
			result.ReasonCode = acquisition.admission.Reason
			detail := acquisition.planReason
			result.ReasonDetail = &detail
		}
	}
	return worker.Finish(workitems.StateSucceeded, workitems.OwnerResult{State: string(result.Verdict), ReasonCode: result.ReasonCode, Reason: safeReason(pointerString(result.ReasonDetail))}, s.releaseOwnerWriter(claim, snapshot, item, result, acquisition, &evidence)), nil
}

func (s *Service) loadReleaseSnapshot(ctx context.Context, releaseID string) (releaseSnapshot, error) {
	var snapshot releaseSnapshot
	var verdict, subscriptionState string
	var rawFacts, sourceFacts, parsedFacts, internalFacts []byte
	var reasonDetail, evaluationVersion, downloadTaskID sql.NullString
	var anibtMode sql.NullString
	var bgmWorkID, tmdbWorkID, tmdbMediaKind, mediaKindOverride sql.NullString
	var tmdbSeason sql.NullInt64
	var policyKind sql.NullString
	var policyVersion sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `
	SELECT r.id, r.rss_source_id, src.subscription_id, sub.managed_anime_id,
       COALESCE(NULLIF(a.title_override, ''), a.title),
       r.stable_item_key, r.raw_facts, r.source_facts, r.parsed_facts, r.internal_facts,
       r.verdict, r.reason_code, r.reason_detail, r.evaluation_version,
       r.download_task_id, r.version, src.locator, src.locator_display,
       src.group_name, src.episode_offset, src.version, src.anibt_aux_mode, src.source_kind, src.provider, src.provider_group_ref, sub.state,
       m.bgm_work_id, m.tmdb_work_id, m.tmdb_media_kind, m.tmdb_season, m.media_kind_override,
       m.version, COALESCE(rules.rules_version, 1),
       pp.parser_kind, pp.parser_version,
       COALESCE(lifecycle.lifecycle_state, ''), COALESCE(task.state, ''), COALESCE(acq.state, ''),
       (SELECT count(*) FROM download_tasks owned WHERE owned.release_id = r.id),
       sub.new_releases_only, sub.start_baseline_at, `+ReleasePlacementIssueSQL("sub.managed_anime_id")+`,
       CASE WHEN r.verdict IN ('awaiting_competition', 'subscription_stopped') THEN COALESCE(r.competition_retry_key, '') ELSE '' END
	FROM releases r
	JOIN rss_sources src ON src.id = r.rss_source_id
	JOIN subscriptions sub ON sub.id = src.subscription_id
	JOIN managed_anime a ON a.id = sub.managed_anime_id
JOIN anime_metadata m ON m.managed_anime_id = sub.managed_anime_id
LEFT JOIN anime_business_configs rules
  ON rules.managed_anime_id = sub.managed_anime_id
LEFT JOIN anime_business_configs pp
  ON pp.managed_anime_id = sub.managed_anime_id
LEFT JOIN managed_anime lifecycle ON lifecycle.id = sub.managed_anime_id
LEFT JOIN download_tasks task ON task.id = r.download_task_id
LEFT JOIN acquisitions acq ON acq.id = task.acquisition_id
WHERE r.id = $1`, releaseID).Scan(
		&snapshot.releaseID, &snapshot.sourceID, &snapshot.subscriptionID, &snapshot.managedAnimeID, &snapshot.animeTitle,
		&snapshot.stableItemKey, &rawFacts, &sourceFacts, &parsedFacts, &internalFacts,
		&verdict, &snapshot.reasonCode, &reasonDetail, &evaluationVersion,
		&downloadTaskID, &snapshot.releaseVersion, &snapshot.locator, &snapshot.locatorDisplay,
		&snapshot.groupName, &snapshot.episodeOffset, &snapshot.sourceVersion, &anibtMode, &snapshot.sourceKind, &snapshot.sourceProvider, &snapshot.sourceGroupRef, &subscriptionState,
		&bgmWorkID, &tmdbWorkID, &tmdbMediaKind, &tmdbSeason, &mediaKindOverride,
		&snapshot.metadataVersion, &snapshot.rulesVersion,
		&policyKind, &policyVersion,
		&snapshot.lifecycleState, &snapshot.taskState, &snapshot.acquisitionState, &snapshot.taskCount,
		&snapshot.newReleasesOnly, &snapshot.startBaselineAt, &snapshot.placementMissing, &snapshot.retryKey); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return releaseSnapshot{}, fmt.Errorf("release %q not found: %w", releaseID, err)
		}
		return releaseSnapshot{}, fmt.Errorf("read release snapshot: %w", err)
	}
	policy, err := instanceParserPolicyFromColumns(snapshot.managedAnimeID, policyKind, policyVersion)
	if err != nil {
		snapshot.parserPolicyError = err
	} else {
		snapshot.parserKind = string(policy.Policy.Kind)
		snapshot.parserVersion = policy.Version
	}
	snapshot.rawFacts = append([]byte(nil), rawFacts...)
	snapshot.sourceFacts = append([]byte(nil), sourceFacts...)
	snapshot.parsedFacts = append([]byte(nil), parsedFacts...)
	snapshot.internalFacts = append([]byte(nil), internalFacts...)
	snapshot.verdict = verdict
	snapshot.reasonDetail = reasonDetail
	snapshot.evaluationVersion = evaluationVersion
	snapshot.downloadTaskID = downloadTaskID
	snapshot.subscriptionState = subscriptionState
	// An explicit retry that left the release waiting for the competition
	// stays that retry while the subscription is active. A stopped
	// subscription's re-evaluation keeps the key with the release
	// (subscription_stopped), so the evaluation after the resume is that
	// retry again.
	snapshot.manualRetry = snapshot.retryKey != "" && subscriptionState == "active"
	snapshot.anibtMode = anibtMode.String
	bgm, tmdb, err := metadataBindingsFromColumns(bgmWorkID, tmdbWorkID, tmdbMediaKind, tmdbSeason, snapshot.managedAnimeID)
	if err != nil {
		return releaseSnapshot{}, err
	}
	snapshot.metadataHold, err = metadataHoldFromBindings(bgm, tmdb)
	if err != nil {
		return releaseSnapshot{}, fmt.Errorf("map metadata hold: %w", err)
	}
	snapshot.mediaKind, snapshot.mediaSeason, snapshot.mediaSeasonBound = frozenMediaBinding(tmdb, nullableStringPtr(mediaKindOverride))
	snapshot.bgmWorkID, snapshot.tmdbWorkID = bgmWorkID.String, tmdbWorkID.String
	resolved, effective, err := loadResolvedConfig(ctx, s.db, snapshot.managedAnimeID)
	if err != nil {
		return releaseSnapshot{}, err
	}
	snapshot.resolved = resolved
	snapshot.effective = effective
	snapshot.profileVersion = effective.Versions[animedomain.SectionParser]
	return snapshot, nil
}

type currentReleaseEvaluationInputs struct {
	groupName        string
	sourceProvider   providerdomain.Kind
	sourceGroupRef   string
	episodeOffset    int
	metadataHold     animedomain.MetadataHold
	mediaKind        string
	mediaSeason      int
	mediaSeasonBound bool
	bgmWorkID        string
	tmdbWorkID       string
	effective        animedomain.EffectiveConfig
	resolved         animedomain.ResolvedSections
	metadataVersion  int64
	rulesVersion     int64
	parserKind       string
	parserVersion    int64
	profileVersion   int64
}

func loadCurrentReleaseEvaluationInputs(ctx context.Context, q queryer, snapshot releaseSnapshot) (currentReleaseEvaluationInputs, error) {
	var inputs currentReleaseEvaluationInputs
	var bgmWorkID, tmdbWorkID, tmdbMediaKind, mediaKindOverride sql.NullString
	var tmdbSeason sql.NullInt64
	var policyKind sql.NullString
	var policyVersion sql.NullInt64
	if err := q.QueryRowContext(ctx, `
SELECT src.group_name, COALESCE(src.provider, ''), COALESCE(src.provider_group_ref, ''), src.episode_offset,
       m.bgm_work_id, m.tmdb_work_id, m.tmdb_media_kind, m.tmdb_season, m.media_kind_override,
       m.version, COALESCE(rules.rules_version, 1),
       pp.parser_kind, pp.parser_version
FROM rss_sources src
JOIN subscriptions sub ON sub.id = src.subscription_id
JOIN anime_metadata m ON m.managed_anime_id = sub.managed_anime_id
LEFT JOIN anime_business_configs rules
  ON rules.managed_anime_id = sub.managed_anime_id
LEFT JOIN anime_business_configs pp
  ON pp.managed_anime_id = sub.managed_anime_id
WHERE src.id = $1 AND sub.managed_anime_id = $2`, snapshot.sourceID, snapshot.managedAnimeID).Scan(
		&inputs.groupName, &inputs.sourceProvider, &inputs.sourceGroupRef, &inputs.episodeOffset,
		&bgmWorkID, &tmdbWorkID, &tmdbMediaKind, &tmdbSeason, &mediaKindOverride,
		&inputs.metadataVersion, &inputs.rulesVersion,
		&policyKind, &policyVersion); err != nil {
		return currentReleaseEvaluationInputs{}, fmt.Errorf("read current release evaluation inputs: %w", err)
	}
	policy, err := instanceParserPolicyFromColumns(snapshot.managedAnimeID, policyKind, policyVersion)
	if err != nil {
		return currentReleaseEvaluationInputs{}, err
	}
	inputs.parserKind = string(policy.Policy.Kind)
	inputs.parserVersion = policy.Version
	bgm, tmdb, err := metadataBindingsFromColumns(bgmWorkID, tmdbWorkID, tmdbMediaKind, tmdbSeason, snapshot.managedAnimeID)
	if err != nil {
		return currentReleaseEvaluationInputs{}, err
	}
	inputs.metadataHold, err = metadataHoldFromBindings(bgm, tmdb)
	if err != nil {
		return currentReleaseEvaluationInputs{}, fmt.Errorf("map metadata hold: %w", err)
	}
	inputs.mediaKind, inputs.mediaSeason, inputs.mediaSeasonBound = frozenMediaBinding(tmdb, nullableStringPtr(mediaKindOverride))
	inputs.bgmWorkID, inputs.tmdbWorkID = bgmWorkID.String, tmdbWorkID.String
	inputs.resolved, inputs.effective, err = loadResolvedConfig(ctx, q, snapshot.managedAnimeID)
	if err != nil {
		return currentReleaseEvaluationInputs{}, err
	}
	inputs.profileVersion = inputs.effective.Versions[animedomain.SectionParser]
	return inputs, nil
}

func (s *Service) releaseOwnerWriter(claim workitems.Claim, snapshot releaseSnapshot, item acquisitiondomain.FeedItem, result animedomain.PreviewReleaseResult, acquisition *targetAcquisition, evidence *releaseParserEvidence) workitems.OwnerWriter {
	return func(ctx context.Context, scope txscope.Tx, _ workitems.OwnerResult) (int64, error) {
		tx := scope.SQLTx()
		if tx == nil {
			return 0, errors.New("release owner writer transaction is nil")
		}
		var currentSubscriptionState string
		if err := tx.QueryRowContext(ctx, `
SELECT state
FROM subscriptions
WHERE id = $1
FOR UPDATE`, snapshot.subscriptionID).Scan(&currentSubscriptionState); err != nil {
			return 0, fmt.Errorf("lock subscription for evaluation: %w", err)
		}

		selected, err := sourceScopeSelected(ctx, tx, snapshot.sourceID, true)
		if err != nil {
			return 0, err
		}

		var currentVersion int64
		var currentTask sql.NullString
		var currentVerdict, storedRetryKey string
		if err := tx.QueryRowContext(ctx, `
SELECT version, download_task_id, verdict, COALESCE(competition_retry_key, '')
FROM releases
WHERE id = $1
FOR UPDATE`, snapshot.releaseID).Scan(&currentVersion, &currentTask, &currentVerdict, &storedRetryKey); err != nil {
			return 0, fmt.Errorf("lock release for evaluation: %w", err)
		}
		// The competition owns the admission of an active subscription,
		// decided on the locked state: a release evaluated while the
		// subscription was stopped (an explicit retry for the direct path, or
		// an automatic evaluation that would end subscription_stopped) whose
		// subscription is active now is evaluated again; it then waits for
		// the competition.
		awaitNow := s.competitionOwnsAdmission(snapshot, currentSubscriptionState)
		if awaitNow && (acquisition != nil || snapshot.subscriptionState != "active") {
			return 0, errCompetitionActivated
		}
		if snapshot.manualRetry {
			if !selected {
				return 0, ErrReleaseChanged
			}
			if err := detachRetriedTaskTx(ctx, tx, snapshot, currentVersion, claim.ExpectedOwnerVersion, currentTask); err != nil {
				return 0, err
			}
		} else {
			if currentTask.Valid || isTerminalReleaseVerdict(currentVerdict) {
				return currentVersion, nil
			}
			if !selected {
				return currentVersion, nil
			}
			if currentVersion != claim.ExpectedOwnerVersion {
				return 0, fmt.Errorf("%w: expected %d, got %d", errReleaseVersionConflict, claim.ExpectedOwnerVersion, currentVersion)
			}
		}

		internalFacts := append([]byte(nil), snapshot.internalFacts...)
		// One selected parser/profile governs this owner write: the frozen
		// DownloadTask policy, the frozen Release media facts, and the target
		// provenance all read this single result. The evaluation already resolved
		// the selection once (and the target preflight reused it), so the same
		// evidence is reused here instead of parsing again. It is re-resolved
		// only when an owner input actually changed under this transaction.
		var frozenEvidence *releaseParserEvidence
		if evidence != nil {
			frozenEvidence = evidence
		}
		loadFrozenEvidence := func() (releaseParserEvidence, error) {
			if frozenEvidence != nil {
				return *frozenEvidence, nil
			}
			resolved, evidenceErr := resolveReleaseParserEvidence(ctx, snapshot, item.Title)
			if evidenceErr != nil {
				return releaseParserEvidence{}, evidenceErr
			}
			frozenEvidence = &resolved
			return resolved, nil
		}
		admits := snapshot.admitsWork(currentSubscriptionState)
		if admits && acquisition == nil {
			currentInputs, err := loadCurrentReleaseEvaluationInputs(ctx, tx, snapshot)
			if err != nil {
				return 0, err
			}
			configChanged, err := resolvedDigestChanged(snapshot.resolved, currentInputs.resolved)
			if err != nil {
				return 0, err
			}
			// tag_filters lives outside the six-section digest; a Global
			// tag_filters save changes no owner version, so compare its
			// digest explicitly.
			configChanged = configChanged || currentInputs.effective.TagFilters.Digest != snapshot.effective.TagFilters.Digest
			// download_completion is frozen into the batch plan at admission,
			// so a changed resolved action must not be written under the old
			// value.
			configChanged = configChanged || !currentInputs.effective.DownloadCompletion.Rules.Equal(snapshot.effective.DownloadCompletion.Rules)
			inputsChanged := configChanged || currentInputs.metadataVersion != snapshot.metadataVersion ||
				currentInputs.rulesVersion != snapshot.rulesVersion ||
				currentInputs.parserKind != snapshot.parserKind ||
				currentInputs.parserVersion != snapshot.parserVersion ||
				currentInputs.bgmWorkID != snapshot.bgmWorkID ||
				currentInputs.tmdbWorkID != snapshot.tmdbWorkID ||
				currentInputs.profileVersion != snapshot.profileVersion ||
				currentInputs.groupName != snapshot.groupName ||
				currentInputs.episodeOffset != snapshot.episodeOffset
			snapshot.groupName = currentInputs.groupName
			snapshot.episodeOffset = currentInputs.episodeOffset
			snapshot.metadataHold = currentInputs.metadataHold
			snapshot.mediaKind = currentInputs.mediaKind
			snapshot.bgmWorkID = currentInputs.bgmWorkID
			snapshot.tmdbWorkID = currentInputs.tmdbWorkID
			snapshot.mediaSeason = currentInputs.mediaSeason
			snapshot.mediaSeasonBound = currentInputs.mediaSeasonBound
			snapshot.effective = currentInputs.effective
			snapshot.resolved = currentInputs.resolved
			snapshot.metadataVersion = currentInputs.metadataVersion
			snapshot.rulesVersion = currentInputs.rulesVersion
			snapshot.parserKind = currentInputs.parserKind
			snapshot.parserVersion = currentInputs.parserVersion
			snapshot.profileVersion = currentInputs.profileVersion
			// A missing torrent payload may have become available since the
			// out-of-transaction evaluation; that recovery re-evaluates the item.
			recoverTorrent := item.TorrentErrorCode == "torrent_metadata_unavailable"
			if recoverTorrent {
				input, inputErr := loadReleaseTorrentInputRef(ctx, tx, snapshot.releaseID)
				if inputErr == nil && torrentMetadataMatchesStoredFacts(item, input) {
					item.TorrentErrorCode = ""
					item.TorrentErrorReason = ""
					internalFacts, err = encodeInternalFeedFacts(item)
					if err != nil {
						return 0, fmt.Errorf("encode recovered torrent facts: %w", err)
					}
				}
			}
			if inputsChanged || recoverTorrent || frozenEvidence == nil {
				resolved, evidenceErr := resolveReleaseParserEvidence(ctx, snapshot, item.Title)
				if evidenceErr != nil {
					// The selected parser/profile is unavailable: fail the owner
					// write closed and visibly instead of degrading to another
					// parser.
					return 0, evidenceErr
				}
				frozenEvidence = &resolved
				currentRules, _, rulesErr := attrrules.Current()
				if rulesErr != nil {
					return 0, fmt.Errorf("read active attr rules: %w", rulesErr)
				}
				result = animedomain.EvaluateFeedItemWithIdentity(item, animedomain.SourceEvaluationInput{
					Index:     0,
					GroupName: currentInputs.groupName,
					Group:     providerdomain.GroupRefOrZero(currentInputs.sourceProvider, currentInputs.sourceGroupRef),
					Offset:    currentInputs.episodeOffset,
				}, currentInputs.effective, currentInputs.metadataHold, currentRules, resolved.Identity)
			}
			markTorrentUnavailable(&result, item)
			if awaitNow && result.Verdict == animedomain.ReleaseVerdict("accepted_pending_download") {
				markAwaitingCompetition(&result)
			}
		}
		if admits && acquisition != nil {
			// Target planning and probing happened against the frozen release
			// inputs outside this transaction. If a rules/metadata/source input
			// changed while the probe was running, abort the owner write and let
			// the fenced WorkItem retry with a fresh plan.
			currentInputs, err := loadCurrentReleaseEvaluationInputs(ctx, tx, snapshot)
			if err != nil {
				return 0, err
			}
			configChanged, err := resolvedDigestChanged(snapshot.resolved, currentInputs.resolved)
			if err != nil {
				return 0, err
			}
			configChanged = configChanged || currentInputs.effective.TagFilters.Digest != snapshot.effective.TagFilters.Digest
			configChanged = configChanged || !currentInputs.effective.DownloadCompletion.Rules.Equal(snapshot.effective.DownloadCompletion.Rules)
			if configChanged || currentInputs.metadataVersion != snapshot.metadataVersion || currentInputs.rulesVersion != snapshot.rulesVersion ||
				currentInputs.parserKind != snapshot.parserKind || currentInputs.parserVersion != snapshot.parserVersion || currentInputs.profileVersion != snapshot.profileVersion ||
				currentInputs.groupName != snapshot.groupName || currentInputs.episodeOffset != snapshot.episodeOffset {
				return 0, errReleaseInputsChanged
			}
		}
		if acquisition != nil && acquisition.releaseFiles != nil {
			item.ReleaseFiles = acquisition.releaseFiles
			encodedFacts, encodeErr := encodeInternalFeedFacts(item)
			if encodeErr != nil {
				return 0, fmt.Errorf("encode target projection facts: %w", encodeErr)
			}
			internalFacts = encodedFacts
		}
		if admits && acquisition != nil && len(acquisition.set.Targets) > 0 {
			if err := s.targetStore.ReplaceSet(ctx, txscope.Wrap(tx), acquisition.set); err != nil {
				return 0, fmt.Errorf("persist acquisition target set: %w", err)
			}
			for _, observation := range acquisition.observations {
				if err := s.targetStore.UpsertObservation(ctx, txscope.Wrap(tx), observation); err != nil {
					return 0, fmt.Errorf("persist target observation: %w", err)
				}
			}
		}
		if acquisition != nil && acquisition.batchPlan != nil {
			animedomain.ApplyPlanCoverage(&result, acquisition.batchPlan)
		}
		parsedFacts, err := encodeParsedFacts(result)
		if err != nil {
			return 0, fmt.Errorf("encode evaluated parsed facts: %w", err)
		}
		verdict := string(result.Verdict)
		reasonCode := result.ReasonCode
		reasonDetail := pointerString(result.ReasonDetail)
		taskID := ""
		correlationID := ""
		// Replace decisions a claim of existing files staled; their
		// not_replaced lines follow the release's own entry.
		var claimStaled []staledReplaceDecision
		if !admits {
			verdict = "subscription_stopped"
			reasonCode = "subscription_stopped"
			reasonDetail = "subscription is stopped"
		} else if acquisition != nil {
			switch acquisition.admission.Admission {
			case target.AdmissionAdopted:
				verdict = "adopted_existing"
				reasonCode = acquisition.admission.Reason
				reasonDetail = "all rendered targets already exist with the exact entry sizes"
				// Claiming promotes: a scan-managed file at an adopted target
				// takes this release's group and frozen comparison values.
				_, staled, err := claimScanManagedTargetsTx(ctx, tx, snapshot.managedAnimeID, snapshot.releaseID, acquisition.set)
				if err != nil {
					return 0, err
				}
				claimStaled = staled
			case target.AdmissionFailed:
				verdict = "acquisition_failed"
				reasonCode = acquisition.admission.Reason
				reasonDetail = acquisition.planReason
				if reasonDetail == "" {
					reasonDetail = "target acquisition admission failed"
				}
			case target.AdmissionSkipped:
				verdict = "filtered"
				reasonCode = acquisition.admission.Reason
				reasonDetail = acquisition.planReason
			case target.AdmissionDownload:
				evidence, evidenceErr := loadFrozenEvidence()
				if evidenceErr != nil {
					return 0, evidenceErr
				}
				taskID, correlationID, err = createDownloadTaskAndWorkItem(ctx, tx, s.db, s.workItems, snapshot, item, evidence, acquisition)
			}
			if err != nil {
				if errors.Is(err, errTorrentPayloadMissing) {
					verdict = "parse_failed"
					reasonCode = "torrent_payload_missing"
					reasonDetail = "the release has no program-owned torrent payload"
				} else {
					return 0, err
				}
			}
		} else if result.Verdict == animedomain.ReleaseVerdict("accepted_pending_download") && !snapshot.metadataHold.Held {
			if snapshot.competitionDecision != nil {
				// The competition decided this release without a target
				// admission: never admit it outside the decision.
				return 0, errReleaseInputsChanged
			}
			evidence, evidenceErr := loadFrozenEvidence()
			if evidenceErr != nil {
				return 0, evidenceErr
			}
			taskID, correlationID, err = createDownloadTaskAndWorkItem(ctx, tx, s.db, s.workItems, snapshot, item, evidence)
			if errors.Is(err, errTorrentPayloadMissing) {
				verdict = "parse_failed"
				reasonCode = "torrent_payload_missing"
				reasonDetail = "the release has no program-owned torrent payload"
			} else if err != nil {
				return 0, err
			} else {
				// task remains pending until qB confirms submission
			}
		}
		// An explicit retry that leaves the release waiting keeps its key with
		// the release (the decided stage runs it as that retry); the key of an
		// earlier retry still waiting moves its placement redownload requests
		// to the newer key. A release that leaves the wait drops the key, and
		// when it downloads nothing its retry's placement redownload requests
		// are withdrawn: the placements hold their episodes again.
		retryKey := ""
		if verdict == competition.VerdictAwaiting && snapshot.manualRetry {
			retryKey = strings.TrimSpace(snapshot.retryKey)
		}
		if retryKey == "" && storedRetryKey != "" && verdict == "subscription_stopped" && currentSubscriptionState != "active" && taskID == "" {
			// A waiting retry re-evaluated while its subscription is stopped
			// keeps its key and its placement redownload requests until the
			// resume evaluates it as that retry again.
			retryKey = storedRetryKey
		}
		if storedRetryKey != "" && storedRetryKey != retryKey {
			if retryKey != "" {
				if _, err := tx.ExecContext(ctx, `
UPDATE placements SET redownload_request_key = $3, version = version + 1, updated_at = CURRENT_TIMESTAMP
WHERE managed_anime_id = $1 AND quality_facts_ref = $2 AND state = 'active' AND redownload_request_key = $4`,
					snapshot.managedAnimeID, snapshot.releaseID, retryKey, storedRetryKey); err != nil {
					return 0, fmt.Errorf("move release placement redownload requests: %w", err)
				}
			} else if taskID == "" {
				if err := withdrawPlacementRedownloadsTx(ctx, tx, snapshot.managedAnimeID, snapshot.releaseID, storedRetryKey); err != nil {
					return 0, err
				}
			}
		}
		var updatedVersion int64
		if err := tx.QueryRowContext(ctx, `
UPDATE releases
SET parsed_facts = $2, internal_facts = $3, verdict = $4, reason_code = $5,
    reason_detail = NULLIF($6, ''), evaluation_version = $7,
    evaluated_rules_version = $8,
    download_task_id = NULLIF($9, ''), competition_retry_key = NULLIF($11, ''), version = version + 1,
    updated_at = CURRENT_TIMESTAMP
WHERE id = $1 AND version = $10 AND download_task_id IS NOT DISTINCT FROM NULLIF($9,'')
RETURNING version`, snapshot.releaseID, nullableJSON(parsedFacts), nullableJSON(internalFacts), verdict, reasonCode,
			safeReason(reasonDetail), evaluationVersionString(), snapshot.rulesVersion, taskID, claim.ExpectedOwnerVersion, retryKey).Scan(&updatedVersion); err != nil {
			return 0, fmt.Errorf("update evaluated release: %w", err)
		}
		// The batch plan this evaluation froze: the admission's own plan when
		// target planning ran, otherwise a plain classification of the
		// manifest under the instance unknown-file route. Candidate facts and
		// the media fact snapshot both read this one plan, so their coverage
		// cannot disagree.
		var frozenPlan *batchplan.Plan
		loadFrozenPlan := func(evidence releaseParserEvidence) (*batchplan.Plan, error) {
			if frozenPlan != nil {
				return frozenPlan, nil
			}
			if acquisition != nil && acquisition.batchPlan != nil {
				frozenPlan = acquisition.batchPlan
				return frozenPlan, nil
			}
			route, err := loadUnclassifiedRoute(ctx, tx, snapshot.managedAnimeID)
			if err != nil {
				return nil, err
			}
			plan, err := target.BuildBatchPlan(ctx, target.BatchPlanInput{Evidence: evidence.ParserEvidence, Files: item.ReleaseFiles, FileFilter: snapshot.effective.Rules.FileFilter, UnclassifiedRoute: route, DownloadCompletion: string(snapshot.effective.DownloadCompletion.Rules.Action), DownloadCompletionSeedLimits: snapshot.effective.DownloadCompletion.Rules.SeedLimits})
			if err != nil {
				return nil, fmt.Errorf("classify release files: %w", err)
			}
			frozenPlan = &plan
			return frozenPlan, nil
		}
		// P3.2: versioned candidate facts (admission-neutral; idempotent).
		// Coverage is the batch plan's placeable coverage; the title is never
		// re-parsed, so candidate facts cannot disagree with the plan.
		if s.candidateFacts != nil && item.Title != "" {
			evidence, evidenceErr := loadFrozenEvidence()
			if evidenceErr != nil {
				return 0, evidenceErr
			}
			plan, planErr := loadFrozenPlan(evidence)
			if planErr != nil {
				return 0, planErr
			}
			rules, _, err := attrrules.Current()
			if err != nil {
				return 0, fmt.Errorf("read active attr rules: %w", err)
			}
			candidate := facts.CandidateFromCoverage(item.Title, rules, plan.CoverageKeys)
			raw, _ := json.Marshal(map[string]any{"title": item.Title, "stable_item_key": snapshot.stableItemKey})
			factsJSON, _ := json.Marshal(map[string]any{
				"resolution": candidate.Resolution, "quality": candidate.Quality,
				"language": candidate.Language, "group": candidate.Group,
				"special": candidate.Special, "season": candidate.Season, "episode": candidate.Episode,
				"coverage_unresolved": candidate.CoverageUnresolved, "unresolved_reason": candidate.UnresolvedReason,
			})
			if err := s.candidateFacts.UpsertForRelease(ctx, txscope.Wrap(tx), facts.ReleaseCandidateFacts{
				ReleaseID:        snapshot.releaseID,
				ReleaseVersion:   updatedVersion,
				ParserVersion:    candidate.ParserVersion,
				InputFingerprint: facts.InputFingerprint(item.Title, nil, false),
				RawEvidence:      raw,
				Facts:            factsJSON,
				CoverageKeys:     candidate.Coverage,
				Resolution:       candidate.Resolution,
				Revision:         candidate.Revision,
				Language:         candidate.Language,
				Group:            candidate.Group,
				// The replacement comparison values freeze here, with the
				// accepted candidate facts.
				CompareValues: facts.CompareValues(candidate, facts.CompareSource{
					SourceFacts: snapshot.sourceFacts, RSSSourceID: snapshot.sourceID, SourceKind: snapshot.sourceKind,
					Provider: snapshot.sourceProvider.String, ProviderGroupRef: snapshot.sourceGroupRef.String,
				}),
				// The release-level size comes from the fetched torrent payload
				// (or the feed enclosure as fallback); it powers the
				// replacement size tie-breaker between same-score candidates.
				SizeBytes: releaseSize(item),
			}); err != nil {
				return 0, fmt.Errorf("upsert candidate facts: %w", err)
			}
		}
		// P4: one append-only Release-level media fact snapshot with the
		// frozen batch plan. Per-file identities are resolved independently at
		// Artifact inspection with the parser frozen here and must agree with
		// the plan; the release title identity never becomes a file's.
		if s.mediaFacts != nil && item.Title != "" {
			evidence, evidenceErr := loadFrozenEvidence()
			if evidenceErr != nil {
				return 0, evidenceErr
			}
			plan, planErr := loadFrozenPlan(evidence)
			if planErr != nil {
				return 0, planErr
			}
			if err := persistReleaseMediaSnapshot(ctx, tx, s.mediaFacts, snapshot, item.Title, evidence, plan); err != nil {
				return 0, err
			}
		}
		if err := s.settleReleaseDiscoveryTx(ctx, tx, snapshot, item, verdict, frozenEvidence, frozenPlan); err != nil {
			return 0, err
		}
		// The activity writer masks sensitive fragments in free text but still
		// rejects sensitive detail keys, so keep this envelope to typed,
		// non-sensitive evidence and omit the reason code already stored in
		// its dedicated column.
		activityDetails := map[string]any{"verdict": verdict, "evaluation_version": evaluationVersionString()}
		// A metadata hold names the bindings it waits for, typed, so a reader
		// says which link is missing instead of parsing the free-text detail.
		if verdict == "waiting_metadata" && snapshot.metadataHold.Held {
			missing := make([]string, 0, len(snapshot.metadataHold.Missing))
			for _, value := range snapshot.metadataHold.Missing {
				missing = append(missing, string(value))
			}
			activityDetails["metadata_missing"] = missing
		}
		if evidence, evidenceErr := loadFrozenEvidence(); evidenceErr == nil {
			provenance := evidence.provenance()
			activityDetails["parser"] = provenance
			activityDetails["episode_state"] = evidence.Identity.State
			activityDetails["episode_shape"] = evidence.Identity.Shape
		}
		for key, value := range snapshot.activityRunContext() {
			activityDetails[key] = value
		}
		details, err := marshalActivityDetails(activityDetails)
		if err != nil {
			return 0, err
		}
		if err := appendActivity(ctx, tx, s.activityWriter, observabilityport.ActivityRecord{
			SubscriptionID:  snapshot.subscriptionID,
			ManagedAnimeID:  snapshot.managedAnimeID,
			Kind:            rssLogKindReleaseProcessed,
			Outcome:         rssLogOutcomeInfo,
			ReasonCode:      reasonCode,
			SubjectType:     "release",
			SubjectID:       snapshot.releaseID,
			SubjectVersion:  updatedVersion,
			StableResultKey: evaluationVersionString() + ":" + reasonCode,
			RSSSourceID:     snapshot.sourceID,
			ReleaseID:       snapshot.releaseID,
			GroupRef:        snapshot.groupName,
			MessageCode:     observabilityport.ReleaseVerdictMessage(verdict),
			Details:         details,
			CorrelationID:   correlationID,
		}); err != nil {
			return 0, err
		}
		if err := s.writeClaimNotReplacedTx(ctx, tx, snapshot, claimStaled, correlationID); err != nil {
			return 0, err
		}
		return updatedVersion, nil
	}
}

func (s *Service) releaseAdmissionRetryWriter(claim workitems.Claim, snapshot releaseSnapshot) workitems.OwnerWriter {
	return func(ctx context.Context, scope txscope.Tx, result workitems.OwnerResult) (int64, error) {
		tx := scope.SQLTx()
		if tx == nil {
			return 0, errors.New("release admission retry owner writer transaction is nil")
		}
		var currentVersion int64
		var currentTask sql.NullString
		var currentVerdict string
		if err := tx.QueryRowContext(ctx, `
SELECT version, download_task_id, verdict
FROM releases
WHERE id = $1
FOR UPDATE`, snapshot.releaseID).Scan(&currentVersion, &currentTask, &currentVerdict); err != nil {
			return 0, fmt.Errorf("lock release for admission retry: %w", err)
		}
		if currentTask.Valid || isTerminalReleaseVerdict(currentVerdict) {
			return currentVersion, nil
		}
		if currentVersion != claim.ExpectedOwnerVersion {
			return 0, fmt.Errorf("%w: expected %d, got %d", errReleaseVersionConflict, claim.ExpectedOwnerVersion, currentVersion)
		}
		verdict := currentVerdict
		if result.State == workitems.StateFailed {
			verdict = "acquisition_failed"
		}
		var version int64
		if err := tx.QueryRowContext(ctx, `
UPDATE releases
SET verdict = $2, reason_code = $3, reason_detail = NULLIF($4, ''),
    evaluation_version = $5, evaluated_rules_version = $6,
    version = version + 1, updated_at = CURRENT_TIMESTAMP
WHERE id = $1 AND version = $7 AND download_task_id IS NULL
			RETURNING version`, snapshot.releaseID, verdict, result.ReasonCode, safeReason(result.Reason), evaluationVersionString(), snapshot.rulesVersion, claim.ExpectedOwnerVersion).Scan(&version); err != nil {
			return 0, fmt.Errorf("persist release admission retry: %w", err)
		}
		return version, nil
	}
}

func (s *Service) releaseFailureWriter(claim workitems.Claim, snapshot releaseSnapshot, reasonCode, reason string) workitems.OwnerWriter {
	return func(ctx context.Context, scope txscope.Tx, _ workitems.OwnerResult) (int64, error) {
		tx := scope.SQLTx()
		if tx == nil {
			return 0, errors.New("release failure owner writer transaction is nil")
		}
		var currentVersion int64
		var currentTask sql.NullString
		var currentVerdict string
		if err := tx.QueryRowContext(ctx, `
SELECT version, download_task_id, verdict
FROM releases
WHERE id = $1
FOR UPDATE`, snapshot.releaseID).Scan(&currentVersion, &currentTask, &currentVerdict); err != nil {
			return 0, fmt.Errorf("lock release for evaluation failure: %w", err)
		}
		if snapshot.manualRetry {
			if err := detachRetriedTaskTx(ctx, tx, snapshot, currentVersion, claim.ExpectedOwnerVersion, currentTask); err != nil {
				return 0, err
			}
		} else {
			if currentTask.Valid || isTerminalReleaseVerdict(currentVerdict) {
				return currentVersion, nil
			}
			if currentVersion != claim.ExpectedOwnerVersion {
				return 0, fmt.Errorf("%w: expected %d, got %d", errReleaseVersionConflict, claim.ExpectedOwnerVersion, currentVersion)
			}
		}

		var version int64
		if err := tx.QueryRowContext(ctx, `
UPDATE releases
SET verdict = 'parse_failed', reason_code = $2, reason_detail = $3,
    evaluation_version = $4, version = version + 1, updated_at = CURRENT_TIMESTAMP
WHERE id = $1 AND version = $5 AND download_task_id IS NULL
			RETURNING version`, snapshot.releaseID, reasonCode, safeReason(reason), evaluationVersionString(), claim.ExpectedOwnerVersion).Scan(&version); err != nil {
			return 0, fmt.Errorf("persist release evaluation failure: %w", err)
		}
		// reasonCode is in its dedicated column; detail keys must stay non-sensitive.
		failureDetails := map[string]any{"verdict": "parse_failed", "reason_detail": safeReason(reason)}
		for key, value := range snapshot.activityRunContext() {
			failureDetails[key] = value
		}
		details, err := marshalActivityDetails(failureDetails)
		if err != nil {
			return 0, err
		}
		if err := appendActivity(ctx, tx, s.activityWriter, observabilityport.ActivityRecord{
			SubscriptionID:  snapshot.subscriptionID,
			ManagedAnimeID:  snapshot.managedAnimeID,
			Kind:            rssLogKindReleaseProcessed,
			Outcome:         rssLogOutcomeFailure,
			ReasonCode:      reasonCode,
			SubjectType:     "release",
			SubjectID:       snapshot.releaseID,
			SubjectVersion:  version,
			StableResultKey: evaluationVersionString() + ":" + reasonCode,
			RSSSourceID:     snapshot.sourceID,
			ReleaseID:       snapshot.releaseID,
			GroupRef:        snapshot.groupName,
			MessageCode:     observabilityport.MessageReleaseParseFailed,
			Details:         details,
		}); err != nil {
			return 0, err
		}
		return version, nil
	}
}

// encodeFrozenReleasePolicy freezes the release policy a DownloadTask was
// admitted under, including the parser provenance of the one selection that
// produced the Release evidence. All of it comes from the same evaluation, so a
// retry never has to read current instance parser policy.
func encodeFrozenReleasePolicy(snapshot releaseSnapshot, evidence releaseParserEvidence) (json.RawMessage, error) {
	effectiveSections, err := json.Marshal(snapshot.resolved.Sections)
	if err != nil {
		return nil, fmt.Errorf("encode frozen effective sections: %w", err)
	}
	effectiveDigest, err := snapshot.resolved.EffectiveDigest()
	if err != nil {
		return nil, fmt.Errorf("digest frozen effective sections: %w", err)
	}
	evaluator := evaluationVersionString()
	provenance := evidence.provenance()
	managedAnimeID := snapshot.managedAnimeID
	policy, err := json.Marshal(frozenPolicySnapshot{
		ConfigIdentity:    animedomain.ConfigRevisionIdentity{IdentityKind: "instance_materialized", ManagedAnimeID: &managedAnimeID, EffectiveDigest: effectiveDigest},
		EffectiveDigest:   effectiveDigest,
		EffectiveSections: effectiveSections,
		MetadataVersion:   snapshot.metadataVersion,
		RulesVersion:      snapshot.rulesVersion,
		EvaluatorVersion:  &evaluator,
		Parser:            &provenance,
		SectionSchemas:    snapshot.resolved.SchemaVersions,
		SectionVersions:   snapshot.resolved.Versions,
	})
	if err != nil {
		return nil, fmt.Errorf("encode frozen release policy: %w", err)
	}
	return policy, nil
}

func createDownloadTaskAndWorkItem(ctx context.Context, tx *sql.Tx, db *sql.DB, store *workitems.Store, snapshot releaseSnapshot, item acquisitiondomain.FeedItem, evidence releaseParserEvidence, acquisitions ...*targetAcquisition) (string, string, error) {
	if store == nil {
		return "", "", errors.New("work item store is nil")
	}
	inputRef, err := loadReleaseTorrentInputRef(ctx, tx, snapshot.releaseID)
	if errors.Is(err, errTorrentPayloadMissing) {
		return "", "", errTorrentPayloadMissing
	}
	if err != nil {
		return "", "", err
	}
	correlation := "corr-" + uuid.NewString()
	frozenInput := internalFrozenDownloadInput{
		ReleaseStableItemKey: snapshot.stableItemKey,
		SelectedRelease: selectedReleaseIdentity{
			ReleaseID: snapshot.releaseID, RSSSourceID: snapshot.sourceID, StableItemKey: snapshot.stableItemKey,
			RawFacts: append(json.RawMessage(nil), snapshot.rawFacts...), SourceFacts: append(json.RawMessage(nil), snapshot.sourceFacts...), InternalFacts: append(json.RawMessage(nil), snapshot.internalFacts...),
			ReleaseVersion: snapshot.releaseVersion, SubscriptionID: snapshot.subscriptionID, ManagedAnimeID: snapshot.managedAnimeID,
		},
		SourceLocatorDisplay: animedomain.SanitizeLocatorDisplay(snapshot.locatorDisplay),
		InfoHash:             item.TorrentInfoHash,
		ReleaseFiles:         encodeStoredReleaseFiles(item.ReleaseFiles),
	}
	if len(acquisitions) > 0 && acquisitions[0] != nil {
		acquisition := acquisitions[0]
		frozenInput.TargetSetDigest = acquisition.set.Digest
		frozenInput.TargetFacts = &frozenTargetFacts{
			Admission: string(acquisition.admission.Admission), Reason: acquisition.admission.Reason,
			TargetSetDigest: acquisition.set.Digest, Targets: acquisition.set.Targets, Observations: acquisition.observations,
		}
		if acquisition.downloaderAdmission != nil {
			pathSnapshot := acquisition.downloaderAdmission.PathSnapshot
			frozenInput.PathSnapshot = &pathSnapshot
			frozenInput.AdoptionProvenance = acquisition.downloaderAdmission.Provenance
		}
	}
	frozenInput.TorrentInputID = inputRef.ID
	frozenInput.TorrentPayloadDigest = inputRef.PayloadDigest
	frozenInput.TorrentFilename = inputRef.Filename
	frozenInput.TorrentParserVersion = inputRef.ParserVersion
	frozenInput.TorrentVersion = inputRef.TorrentVersion
	frozenInput.InfoHashV1 = inputRef.InfoHashV1
	frozenInput.InfoHashV2 = inputRef.InfoHashV2
	inputJSON, err := json.Marshal(frozenInput)
	if err != nil {
		return "", "", fmt.Errorf("encode frozen download input: %w", err)
	}
	policyJSON, err := encodeFrozenReleasePolicy(snapshot, evidence)
	if err == nil && len(acquisitions) > 0 && acquisitions[0] != nil && acquisitions[0].vars != nil {
		var policy frozenPolicySnapshot
		if decodeErr := json.Unmarshal(policyJSON, &policy); decodeErr != nil {
			return "", "", decodeErr
		}
		policy.Vars = acquisitions[0].vars
		policyJSON, err = json.Marshal(policy)
	}
	if err != nil {
		return "", "", err
	}
	// The media lineage correlation begins here: the DownloadTask carries the
	// release's correlation id and causally points at the release that
	// admitted it. Downstream owners copy both instead of inventing their own.
	revisions, err := draft.NewService(db, draft.Dependencies{WorkItems: store})
	if err != nil {
		return "", "", err
	}
	acceptor, err := releaseAcceptor(db, revisions, snapshot.manualRetry)
	if err != nil {
		return "", "", err
	}
	resolution, err := terminal.NewOwnerResolution(snapshot.resolved)
	if err != nil {
		return "", "", err
	}
	var replacements []*terminal.ReplacementCapture
	overwriteTargetAbsent := false
	if len(acquisitions) > 0 && acquisitions[0] != nil {
		replacements = acquisitions[0].replacements
		overwriteTargetAbsent = acquisitions[0].overwriteTargetAbsent && len(replacements) == 0 && !acquisitions[0].overwriteDirectPresent
	}
	prepared, err := acceptor.Prepare(ctx, txscope.Wrap(tx), terminal.EntryTask{ReleaseID: snapshot.releaseID, ExpectedVersion: snapshot.releaseVersion, Attempt: snapshot.downloadAttempt(), FrozenInput: inputJSON, FrozenPolicy: policyJSON, CorrelationID: correlation, MaxAttempts: defaultMaxAttempts, ParserEvidence: &evidence.ParserEvidence, Resolution: resolution, Replacements: replacements, OverwriteTargetAbsent: overwriteTargetAbsent})
	if err != nil {
		return "", "", err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE releases SET verdict='accepted_pending_download' WHERE id=$1`, snapshot.releaseID); err != nil {
		return "", "", err
	}
	pending, err := acceptor.Accept(ctx, txscope.Wrap(tx), prepared)
	if err != nil {
		return "", "", err
	}
	return pending.TaskID(), correlation, nil
}

// activityRunContext is the run context of the Release's evaluation entries:
// the subgroup display name and the feed title, the same keys the acquisition
// run entries carry, so the records search finds the Release by either. A feed
// item without a title has no release_title.
func (s releaseSnapshot) activityRunContext() map[string]any {
	var facts storedFeedFacts
	title := ""
	if err := json.Unmarshal(s.rawFacts, &facts); err == nil {
		title = facts.Title
	}
	return businesslog.RunContext(s.groupName, title, sql.NullString{})
}

// persistReleaseMediaSnapshot freezes the Release-level evidence through the
// shared admission writer (acquisition/target FreezeReleaseMediaSnapshotTx),
// from the caller's single releaseParserEvidence so the same selection drives
// the Release verdict, the target/naming plan and this row. Episode identity
// comes only from the instance's one selected parser/profile; the reserved
// AniBT auxiliary mode never resolves an episode and only enters the input
// digest, so a mode edit re-evaluates unsubmitted releases conservatively.
func persistReleaseMediaSnapshot(ctx context.Context, tx *sql.Tx, store facts.MediaRepository, snapshot releaseSnapshot, title string, evidence releaseParserEvidence, plan *batchplan.Plan) error {
	return target.FreezeReleaseMediaSnapshotTx(ctx, txscope.Wrap(tx), store, releaseMediaSnapshotInput(snapshot, title, evidence, plan))
}

func releaseMediaSnapshotInput(snapshot releaseSnapshot, title string, evidence releaseParserEvidence, plan *batchplan.Plan) target.ReleaseMediaSnapshotInput {
	return target.ReleaseMediaSnapshotInput{
		ReleaseID: snapshot.releaseID, Title: title,
		BGMWorkID: snapshot.bgmWorkID, TMDBWorkID: snapshot.tmdbWorkID, AniBTMode: snapshot.anibtMode,
		Evidence: evidence.ParserEvidence, Plan: plan,
	}
}

// markTorrentUnavailable turns a release without a usable torrent file list
// into a visible parse failure. A torrent fetch failure keeps its own code and
// diagnostic (the refresh recovery keys off it); an otherwise accepted release
// whose fetch left no file list is torrent_manifest_missing: the batch plan has
// nothing to classify.
func markTorrentUnavailable(result *animedomain.PreviewReleaseResult, item acquisitiondomain.FeedItem) {
	if item.TorrentErrorCode != "" {
		result.Verdict = animedomain.ReleaseVerdict("parse_failed")
		result.ReasonCode = item.TorrentErrorCode
		detail := safeReason(item.TorrentErrorReason)
		result.ReasonDetail = &detail
		return
	}
	if result.Verdict != animedomain.ReleaseVerdict("accepted_pending_download") || len(item.ReleaseFiles) > 0 {
		return
	}
	result.Verdict = animedomain.ReleaseVerdict("parse_failed")
	result.ReasonCode = batchplan.ReasonTorrentManifestMissing
	detail := "the torrent file list is unavailable"
	result.ReasonDetail = &detail
}

// selectedParserKind is the instance's explicit parser policy kind, already
// validated by the shared loader when the snapshot was read. There is no
// empty-kind fallback: a release whose instance has no usable policy row is
// failed closed before this is ever consulted.
func selectedParserKind(snapshot releaseSnapshot) parser.ParserKind {
	return parser.ParserKind(strings.TrimSpace(snapshot.parserKind))
}

// releaseSize resolves the release-level byte size: the fetched torrent total
// wins, the feed enclosure is the fallback, otherwise unknown (NULL).
func releaseSize(item acquisitiondomain.FeedItem) *int64 {
	if item.TorrentSizeBytes > 0 {
		value := item.TorrentSizeBytes
		return &value
	}
	if item.EnclosureLength > 0 {
		value := item.EnclosureLength
		return &value
	}
	return nil
}
