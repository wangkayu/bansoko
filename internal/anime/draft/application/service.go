// Package application implements the anime candidate-draft use case: plan a
// candidate, save immutable effective-configuration revisions, and activate one
// exact saved revision as the complete anime owner set in the atomic Subscribe
// transaction. Candidate CRUD, revision storage and verification, the
// candidate lock/CAS/idempotency/replay rules and that transaction all live
// here.
//
// The anime kernel package supplies only the shared caller-scoped machinery:
// owner-shape vocabulary, owner provisioning inside the caller's transaction,
// lifecycle fences and the canonical digest helpers. This service stores their
// narrow ports and never embeds the kernel service.
package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	animeapplication "bansoko/internal/anime/application"
	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/apperrors"
	"bansoko/internal/events"
	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/persistence/txscope"
	plugintargetport "bansoko/internal/plugintarget/port"
	subscriptiondomain "bansoko/internal/subscription/domain"
	"bansoko/internal/workitems"
)

// CandidateRevision is the kernel's immutable saved-revision shape; the draft
// owner is the package that stores and reads it.
type CandidateRevision = animeapplication.CandidateRevision

// Kernel owner-shape vocabulary the draft projections carry. These are aliases,
// so one shape crosses every boundary instead of a second definition.
type (
	ManagedAnime  = animeapplication.ManagedAnime
	Subscription  = animeapplication.Subscription
	AnimeMetadata = animeapplication.AnimeMetadata
	BGMBinding    = animeapplication.BGMBinding
	TMDBBinding   = animeapplication.TMDBBinding
	RSSSource     = animeapplication.RSSSource
)

// Kernel ports and vocabulary the draft commands consume.
type (
	FrozenParserPolicyPort = animeapplication.FrozenParserPolicyPort
	GroupDiscoveryProvider = animeapplication.GroupDiscoveryProvider
	DiscoverResult         = animeapplication.DiscoverResult
)

// Dependencies are the construction-time collaborators of the draft commands.
// The policy-snapshot ports, the activity journal and the discovery backend are
// wired after construction because the file-maintenance graph that owns them is
// built after this service; a nil port degrades only the path that needs it.
type Dependencies struct {
	WorkItems *workitems.Store
}

// Service owns anime candidate drafts and the Subscribe/activation transaction.
// The kernel supplies the shared owner provisioning, fences and digest helpers;
// the one-shot flow and the read model receive this service behind their own
// narrow ports instead of reaching into it.
type Service struct {
	db                 *sql.DB
	workItems          *workitems.Store
	provisioner        *animeapplication.Provisioner
	frozenParserPolicy FrozenParserPolicyPort
	observability      observabilityport.Recorder
	activityWriter     observabilityport.ActivityWriter
	discovery          *animeapplication.Discovery
	logger             observabilityport.RuntimeLogger
	invalidator        *animeapplication.Invalidator
	candidateLockHook  func(operation string)
	// notifications admits subscription_changed notices; nil admits none.
	notifications plugintargetport.BusinessDeliveryEnqueuer
}

// SetBusinessDeliveryEnqueuer wires the subscription_changed admission of a
// new subscription.
func (s *Service) SetBusinessDeliveryEnqueuer(enqueuer plugintargetport.BusinessDeliveryEnqueuer) {
	if s != nil {
		s.notifications = enqueuer
	}
}

// NewService validates and creates the draft application service.
func NewService(db *sql.DB, deps Dependencies) (*Service, error) {
	if db == nil {
		return nil, errors.New("draft service database is nil")
	}
	if deps.WorkItems == nil {
		return nil, errors.New("draft service work item store is nil")
	}
	logger := observabilityport.RuntimeLogger(observabilityport.NopRuntimeLogger{})
	discovery := &animeapplication.Discovery{}
	discovery.SetLogger(logger)
	provisioner := animeapplication.NewProvisioner(nil)
	return &Service{
		db:            db,
		workItems:     deps.WorkItems,
		provisioner:   &provisioner,
		discovery:     discovery,
		logger:        logger,
		invalidator:   animeapplication.NewInvalidator(nil),
		observability: nil,
	}, nil
}

// SetLogger wires the optional runtime diagnostics sink. A nil logger restores
// the no-op default so existing callers remain source-compatible.
func (s *Service) SetLogger(logger observabilityport.RuntimeLogger) {
	if s == nil {
		return
	}
	if animeapplication.IsTypedNil(logger) {
		logger = observabilityport.NopRuntimeLogger{}
	}
	s.logger = logger
	s.discovery.SetLogger(logger)
}

// SetObservabilityRecorder wires the compact routine/diagnostic projection for
// the activation transaction.
func (s *Service) SetObservabilityRecorder(recorder observabilityport.Recorder) {
	if s != nil {
		s.observability = recorder
	}
}

// SetActivityWriter wires the central Activity journal for the Subscribe
// transaction.
func (s *Service) SetActivityWriter(writer observabilityport.ActivityWriter) {
	if s != nil {
		s.activityWriter = writer
	}
}

// SetEventHub wires the read-model invalidation boundary published after a
// Subscribe transaction commits.
func (s *Service) SetEventHub(hub *events.Hub) {
	if s != nil {
		s.invalidator = animeapplication.NewInvalidator(hub)
	}
}

// SetGroupDiscoveryProvider wires the per-anime group discovery backend the
// activation path resumes after commit.
func (s *Service) SetGroupDiscoveryProvider(provider animeapplication.GroupDiscoveryProvider) {
	if s == nil {
		return
	}
	s.discovery.SetProvider(provider)
}

// SetFrozenParserPolicy wires the port that persists revision-frozen parser policy.
func (s *Service) SetFrozenParserPolicy(port FrozenParserPolicyPort) {
	if s != nil {
		s.frozenParserPolicy = port
		s.configureProvisioner()
	}
}

func (s *Service) configureProvisioner() {
	if s != nil {
		provisioner := animeapplication.NewProvisioner(s.frozenParserPolicy)
		s.provisioner = &provisioner
	}
}

func (s *Service) publishInvalidations(targets ...string) {
	if s != nil && s.invalidator != nil {
		s.invalidator.Publish(targets...)
	}
}

// The kernel exports the shared machinery the moved commands call. Each entry
// forwards to the kernel implementation of the same behaviour, so the draft
// commands keep their original bodies and the kernel keeps one implementation
// of every helper.
func canonicalJSONHash(value any) (string, error) { return animeapplication.CanonicalJSONHash(value) }

func canonicalAnimeDraftHash(draft animedomain.AnimeDraft) (string, error) {
	return animeapplication.CanonicalAnimeDraftHash(draft)
}

func draftInvalidError(violations []animedomain.FieldViolation) error {
	return animeapplication.DraftInvalidError(violations)
}

func sectionResolutionError(err error) error { return animeapplication.SectionResolutionError(err) }

func namingVarsSnapshot(field string) (map[string]map[string]string, error) {
	return animeapplication.NamingVarsSnapshot(field)
}

func validateIdempotencyKey(key string) error { return animeapplication.ValidateIdempotencyKey(key) }

func candidateDigestMismatchError() error { return animeapplication.CandidateDigestMismatchError() }

func candidateNotWorkingError() error { return animeapplication.CandidateNotWorkingError() }

func candidateRevisionMismatchError(current, requested int64) error {
	return animeapplication.CandidateRevisionMismatchError(current, requested)
}

func candidateRevisionRequiredError() error { return animeapplication.CandidateRevisionRequiredError() }

func cloneString(value *string) *string { return animeapplication.CloneString(value) }

func cloneInt(value *int) *int { return animeapplication.CloneInt(value) }

func cloneTime(value *time.Time) *time.Time { return animeapplication.CloneTime(value) }

func nullableOptionalText(value *string) any { return animeapplication.NullableOptionalText(value) }

// providerTitleTx reads the title the provider catalog most recently observed
// for the subscription's entry. It returns nil for a custom/oneshot
// subscription, an entry the catalog does not carry, and any observed title the
// column cannot hold (blank, invalid UTF-8, or longer than 512 characters). The
// draft title is never a substitute: the provider title is what the source
// actually showed at subscribe time.
//
// Two snapshots can observe the same entry at the same instant, so the order
// breaks the tie on id. The draft preview reads the title with the same order
// (readmodel draftProviderTitle), and a subscription must freeze the row the
// preview showed.
func providerTitleTx(ctx context.Context, tx *sql.Tx, origin string, entryRef *string) (*string, error) {
	if origin != string(animedomain.SubscriptionOriginAnibt) && origin != string(animedomain.SubscriptionOriginMikan) {
		return nil, nil
	}
	if entryRef == nil || strings.TrimSpace(*entryRef) == "" {
		return nil, nil
	}
	var title string
	err := tx.QueryRowContext(ctx, `
SELECT title FROM provider_catalog_entries
WHERE provider=$1 AND provider_entry_ref=$2
ORDER BY observed_at DESC, id DESC LIMIT 1`, origin, strings.TrimSpace(*entryRef)).Scan(&title)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return animedomain.ProviderCatalogTitle(title), nil
}

func upsertOverride(ctx context.Context, tx *sql.Tx, animeID, ownerType, section string, override animedomain.OverrideValue) error {
	return animeapplication.UpsertOverride(ctx, tx, animeID, ownerType, section, override)
}

func writeSubscriptionCreatedLog(ctx context.Context, tx *sql.Tx, writer observabilityport.ActivityWriter, subscriptionID, managedAnimeID, title string) error {
	return animeapplication.WriteSubscriptionCreatedLog(ctx, tx, writer, subscriptionID, managedAnimeID, title)
}

const defaultMaxAttempts = animeapplication.DefaultMaxAttempts

var rulesSectionOrder = animeapplication.RulesSectionOrder

// ReadVerifiedAnimeConfigCandidateRevision returns a saved revision only after
// verifying its persisted draft, effective-section and parser-policy digests.
// The read model consumes it through its own revision port.
func (s *Service) ReadVerifiedAnimeConfigCandidateRevision(ctx context.Context, candidateID string, revision int64) (CandidateRevision, error) {
	value, err := s.GetAnimeConfigCandidateRevision(ctx, candidateID, revision)
	if err != nil {
		return CandidateRevision{}, err
	}
	if err := value.VerifyIntegrity(); err != nil {
		return CandidateRevision{}, err
	}
	return value, nil
}

// rejectOneShotOnlyOrigin fails closed before any owner write when a draft
// carries the oneshot origin. That origin exists only for one-shot acquisition
// (an agent candidate snapshot feeds AcquireSeedOnce, which creates its own
// temporary mount); a persistent subscription is never created from it.
func rejectOneShotOnlyOrigin(draft animedomain.AnimeDraft) error {
	if draft.Origin == animedomain.SubscriptionOriginOneshot {
		return &apperrors.Business{Status: 422, Code: "ORIGIN_NOT_SUBSCRIBABLE", Message: "Drafts with the oneshot origin are for one-shot acquisition only and cannot create a subscription."}
	}
	return nil
}

// SubscribeAnimeApplication consumes the complete draft and commits the full owner set.
func (s *Service) SubscribeAnimeApplication(ctx context.Context, input SubscribeAnimeInput, idempotencyKey string) (SubscribeResult, error) {
	if err := validateIdempotencyKey(idempotencyKey); err != nil {
		return SubscribeResult{}, err
	}
	if violations := animedomain.ValidateDraft(input.Draft); len(violations) > 0 {
		return SubscribeResult{}, draftInvalidError(violations)
	}
	if err := rejectOneShotOnlyOrigin(input.Draft); err != nil {
		return SubscribeResult{}, err
	}
	// Subscribe persists the submitted draft's owner set, so it validates naming
	// vars exactly like a candidate save. Every section must carry its value.
	vars, err := namingVarsSnapshot("rules." + animedomain.SectionNamingRules)
	if err != nil {
		return SubscribeResult{}, err
	}
	if _, err := animedomain.ResolveDraftEffectiveWithVars(input.Draft, vars); err != nil {
		return SubscribeResult{}, sectionResolutionError(err)
	}
	initialState := subscriptiondomain.SubscriptionPending
	if input.Start {
		initialState = subscriptiondomain.SubscriptionActive
	}
	requestHash := input.RequestHash
	if requestHash == "" {
		// A started subscribe is a different request than a pending one for
		// the same draft; the pending hash stays the plain draft hash.
		if input.Start {
			requestHash, err = canonicalJSONHash(struct {
				Draft animedomain.AnimeDraft `json:"draft"`
				Start bool                   `json:"start"`
			}{Draft: input.Draft, Start: true})
		} else {
			requestHash, err = canonicalJSONHash(input.Draft)
		}
		if err != nil {
			return SubscribeResult{}, fmt.Errorf("hash subscribe request: %w", err)
		}
	}
	return s.subscribeOnce(ctx, SubscribeCommand{
		Draft:          input.Draft,
		IdempotencyKey: idempotencyKey,
		RequestHash:    requestHash,
		InitialState:   initialState,
	})
}

func (s *Service) activateCandidateOnce(ctx context.Context, candidateID string, expectedVersion int64, idempotencyKey string, input CandidateSubscribeInput) (SubscribeResult, error) {
	if s == nil || s.db == nil {
		return SubscribeResult{}, errors.New("anime service database is nil")
	}
	if err := validateIdempotencyKey(idempotencyKey); err != nil {
		return SubscribeResult{}, err
	}
	if input.Revision < 1 {
		return SubscribeResult{}, candidateRevisionRequiredError()
	}
	initialState := subscriptiondomain.SubscriptionPending
	if input.Start {
		initialState = subscriptiondomain.SubscriptionActive
	}
	if input.InitialState != nil {
		if !input.InitialState.Valid() {
			return SubscribeResult{}, &apperrors.BadRequest{Code: "REQUEST_INVALID", Message: "initial_state must be pending, active or stopped."}
		}
		if input.Start && *input.InitialState != subscriptiondomain.SubscriptionActive {
			return SubscribeResult{}, &apperrors.BadRequest{Code: "REQUEST_INVALID", Message: "start requires initial_state active or no initial_state."}
		}
		initialState = *input.InitialState
	}
	result, err := s.subscribeOnceWithPostCommit(ctx, SubscribeCommand{
		IdempotencyKey:  idempotencyKey,
		CandidateID:     &candidateID,
		ExpectedVersion: &expectedVersion,
		Revision:        &input.Revision,
		InitialState:    initialState,
	}, func(ctx context.Context, result SubscribeResult) {
		s.discovery.DiscoverAfterCommit(ctx, result.ManagedAnime.ID, result.Subscription.Origin)
	})
	if err != nil {
		return SubscribeResult{}, err
	}
	return result, nil
}

// subscribeOnce creates the whole owner set in one transaction.
func (s *Service) subscribeOnce(ctx context.Context, command SubscribeCommand) (SubscribeResult, error) {
	return s.subscribeOnceWithPostCommit(ctx, command, nil)
}

// subscribeOnceWithPostCommit creates the owner set in one transaction and runs
// an optional callback only after a new owner transaction has committed. Replay
// paths deliberately skip the callback so an idempotent activation never repeats
// provider discovery.
func (s *Service) subscribeOnceWithPostCommit(ctx context.Context, command SubscribeCommand, postCommit func(context.Context, SubscribeResult)) (SubscribeResult, error) {
	// Candidate activation locks candidate -> advisory key -> idempotency row.
	// Direct Subscribe has no candidate and starts with the advisory key lock.
	// The initial state is pending (not started) unless the caller starts the
	// subscription at once; candidate activation may also request a stopped
	// one-off instance. expectedVersion is the caller-supplied
	// candidate version (If-Match); it is part of the activation identity and the
	// CAS target for a new activation. A candidate activation materializes the
	// candidate's current saved revision as instance-owned configuration: every
	// section as the complete value the revision froze. Direct Subscribe stores
	// the draft's complete section values. No section follows Global.
	initialState := command.InitialState
	if !initialState.Valid() {
		// Fail closed: an unset state never starts work.
		initialState = subscriptiondomain.SubscriptionPending
	}
	candidateID := ""
	if command.CandidateID != nil {
		candidateID = *command.CandidateID
	}
	expectedVersion := int64(0)
	if command.ExpectedVersion != nil {
		expectedVersion = *command.ExpectedVersion
	}
	key := command.IdempotencyKey
	requestHash := command.RequestHash
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SubscribeResult{}, fmt.Errorf("begin subscribe transaction: %w", err)
	}
	defer tx.Rollback()

	var frozenBusinessRevision *CandidateRevision
	var candidateActivated sql.NullString
	var candidateVersion int64
	var candidateStatus string
	var candidateCurrentRevision sql.NullInt64
	var candidateCurrentRevisionDigest sql.NullString
	candidateRevisionMatchesPointer := true
	// materializedSections is set once a candidate activation has locked its
	// saved revision. It switches instance configuration to the revision's
	// section values in the same transaction.
	var materializedSections *animedomain.ResolvedSections
	if candidateID != "" {
		if command.Revision == nil || *command.Revision < 1 {
			return SubscribeResult{}, candidateRevisionRequiredError()
		}
		err := tx.QueryRowContext(ctx, `
SELECT current_revision, current_revision_digest, version, activated_managed_anime_id, status
FROM anime_config_candidates
WHERE id = $1
FOR UPDATE`, candidateID).Scan(&candidateCurrentRevision, &candidateCurrentRevisionDigest, &candidateVersion, &candidateActivated, &candidateStatus)
		if errors.Is(err, sql.ErrNoRows) {
			return SubscribeResult{}, &apperrors.NotFound{Resource: "anime_config_candidate", ID: candidateID}
		}
		if err != nil {
			return SubscribeResult{}, fmt.Errorf("lock anime config candidate: %w", err)
		}
		if s.candidateLockHook != nil {
			s.candidateLockHook("activate")
		}
		if !candidateActivated.Valid && candidateVersion != expectedVersion {
			return SubscribeResult{}, &apperrors.VersionConflict{Owner: "anime_config_candidate", CurrentVersion: candidateVersion}
		}
		if !candidateActivated.Valid && candidateStatus != "working" {
			return SubscribeResult{}, candidateNotWorkingError()
		}
		// Execution identity is the saved revision, never the mutable draft. A
		// pointer-less candidate is a legacy row that must be re-saved: deriving a
		// revision from the current globals is forbidden, so this fails closed
		// before any owner row is written.
		if !candidateCurrentRevision.Valid {
			return SubscribeResult{}, candidateRevisionRequiredError()
		}
		requestedRevision := *command.Revision
		revision, databaseDigest, err := loadCandidateRevisionTx(ctx, tx, candidateID, requestedRevision)
		if errors.Is(err, sql.ErrNoRows) {
			return SubscribeResult{}, candidateRevisionMismatchError(candidateCurrentRevision.Int64, requestedRevision)
		}
		if err != nil {
			return SubscribeResult{}, fmt.Errorf("read anime config candidate revision: %w", err)
		}
		resolved, err := revision.Resolved()
		if err != nil {
			return SubscribeResult{}, candidateDigestMismatchError()
		}
		// Recompute draft/effective/parser digests from the stored body and fail
		// closed on any disagreement. This also proves the frozen parser envelope
		// is well-formed before it becomes an instance policy.
		if err := revision.VerifyIntegrity(); err != nil {
			return SubscribeResult{}, err
		}
		goDigest, err := canonicalAnimeDraftHash(revision.Draft)
		if err != nil {
			return SubscribeResult{}, candidateDigestMismatchError()
		}
		if revision.DraftDigest == "" || revision.DraftDigest != databaseDigest || databaseDigest != goDigest {
			return SubscribeResult{}, candidateDigestMismatchError()
		}
		candidateRevisionMatchesPointer = candidateCurrentRevision.Int64 == requestedRevision && candidateCurrentRevisionDigest.Valid && candidateCurrentRevisionDigest.String == revision.EffectiveDigest
		if err := rejectOneShotOnlyOrigin(revision.Draft); err != nil {
			return SubscribeResult{}, err
		}
		command.Draft = revision.Draft
		if revision.Business != nil {
			frozenBusinessRevision = &revision
		}
		materializedSections = &resolved
		// The activation identity carries the CALLER's expected version, never a
		// database-derived current/consumed version. Replaying the same key with
		// the same expected version must hit the same identity even after the
		// first activation bumped the candidate version; changing any identity
		// component (including expected version or idempotency key) must never replay.
		requestHash, err = canonicalJSONHash(struct {
			Kind            string `json:"kind"`
			CandidateID     string `json:"candidate_id"`
			IdempotencyKey  string `json:"idempotency_key"`
			ExpectedVersion int64  `json:"expected_version"`
			Revision        int64  `json:"revision"`
			EffectiveDigest string `json:"effective_digest"`
			InitialState    string `json:"initial_state"`
		}{Kind: "candidate_activation", CandidateID: candidateID, IdempotencyKey: key, ExpectedVersion: expectedVersion, Revision: requestedRevision, EffectiveDigest: revision.EffectiveDigest, InitialState: string(initialState)})
		if err != nil {
			return SubscribeResult{}, fmt.Errorf("hash candidate activation identity: %w", err)
		}
	}

	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, key); err != nil {
		return SubscribeResult{}, fmt.Errorf("lock subscribe idempotency key: %w", err)
	}

	var existingHash string
	var existingSnapshot []byte
	var existingManagedAnimeID sql.NullString
	err = tx.QueryRowContext(ctx, `
SELECT request_hash, result_snapshot, managed_anime_id
FROM idempotency_records
WHERE kind = 'subscribe' AND idempotency_key = $1
FOR UPDATE`, key).Scan(&existingHash, &existingSnapshot, &existingManagedAnimeID)
	if err == nil {
		var result SubscribeResult
		if err := json.Unmarshal(existingSnapshot, &result); err != nil {
			return SubscribeResult{}, fmt.Errorf("decode subscribe idempotency snapshot: %w", err)
		}
		if existingHash != requestHash {
			return SubscribeResult{}, idempotencyKeyReusedError()
		}
		// Replay integrity: the persisted managed_anime_id must match the
		// result's managed anime id and subscription link. A mismatch means
		// the idempotency row was corrupted or reused for a different anime.
		if !existingManagedAnimeID.Valid || existingManagedAnimeID.String != result.ManagedAnime.ID || result.Subscription.ManagedAnimeID != result.ManagedAnime.ID {
			return SubscribeResult{}, idempotencyKeyReusedError()
		}
		sanitizeRSSSourceDisplays(result.RssSources)
		if candidateID != "" {
			// Candidate activation replay: the locked candidate's
			// activated_managed_anime_id must match the result, the saved
			// revision must still be the exact revision in the identity, and
			// the candidate version must have been bumped exactly once from
			// the caller's expected version.
			if !candidateActivated.Valid || candidateActivated.String != result.ManagedAnime.ID || !candidateRevisionMatchesPointer {
				return SubscribeResult{}, idempotencyKeyReusedError()
			}
			if candidateVersion != expectedVersion+1 {
				return SubscribeResult{}, idempotencyKeyReusedError()
			}
			if err := tx.Commit(); err != nil {
				return SubscribeResult{}, fmt.Errorf("commit subscribe replay: %w", err)
			}
			return result, nil
		}
		if err := tx.Commit(); err != nil {
			return SubscribeResult{}, fmt.Errorf("commit subscribe replay: %w", err)
		}
		return result, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return SubscribeResult{}, fmt.Errorf("read subscribe idempotency: %w", err)
	}
	if candidateActivated.Valid {
		return SubscribeResult{}, candidateAlreadyActivatedError()
	}
	if !candidateRevisionMatchesPointer {
		return SubscribeResult{}, candidateRevisionMismatchError(candidateCurrentRevision.Int64, *command.Revision)
	}

	draft := command.Draft
	animeID := uuid.NewString()
	title := strings.TrimSpace(draft.Title)
	providerTitle, err := providerTitleTx(ctx, tx, string(draft.Origin), draft.OriginEntryRef)
	if err != nil {
		return SubscribeResult{}, fmt.Errorf("read provider catalog title: %w", err)
	}
	fixedTitle := animeapplication.FixedTitle(draft.Title, draft.TitleOverride)
	// The anime's title is fixed at subscribe: the draft's title override, else
	// its title (prefilled with the provider title). It never follows a later
	// provider or metadata title; only an owner edit changes it.
	if _, err := tx.ExecContext(ctx, `
INSERT INTO managed_anime(id, title, title_override, provider_title)
VALUES ($1, $2, $3, $4)`, animeID, title, fixedTitle, nullableOptionalText(providerTitle)); err != nil {
		return SubscribeResult{}, fmt.Errorf("create managed anime: %w", err)
	}
	subscriptionID := uuid.NewString()
	startBaselineAt, err := insertSubscriptionTx(ctx, tx, subscriptionID, animeID, string(draft.Origin), nullableOptionalText(draft.OriginEntryRef), nullableOptionalText(draft.InstanceLabel), string(initialState), draft.NewReleasesOnly)
	if err != nil {
		return SubscribeResult{}, err
	}
	metadata, err := s.provisioner.InitializeAnimeOwnerRowsTx(ctx, tx, animeID, draft.Metadata)
	if err != nil {
		return SubscribeResult{}, err
	}
	if frozenBusinessRevision != nil {
		if err := verifyBusinessProgramsTx(ctx, tx, *frozenBusinessRevision.Business); err != nil {
			return SubscribeResult{}, err
		}
		if err := s.provisioner.ProvisionFrozenBusinessTx(ctx, tx, animeID, *frozenBusinessRevision); err != nil {
			return SubscribeResult{}, err
		}
	} else if materializedSections != nil {
		// Every saved revision carries the business snapshot it froze; a
		// revision without one cannot provision its axes and fails closed.
		return SubscribeResult{}, businessInputError("candidate revision must carry a complete business snapshot")
	} else {
		// Direct Subscribe provisions every axis from the draft's own business
		// snapshot through the same path activation uses: the retention,
		// parser and file-effect values the draft froze when it was created.
		// A Global change made after that never reaches the instance, and a
		// draft without a snapshot fails closed instead of reading Global.
		if draft.Business == nil {
			return SubscribeResult{}, businessInputError("draft must carry a complete business snapshot")
		}
		if err := animeapplication.VerifySubmittedBusinessDigest(draft); err != nil {
			return SubscribeResult{}, err
		}
		if err := verifyBusinessProgramsTx(ctx, tx, *draft.Business); err != nil {
			return SubscribeResult{}, err
		}
		if _, err := s.provisioner.ProvisionBusinessAxesTx(ctx, tx, animeID, *draft.Business); err != nil {
			return SubscribeResult{}, err
		}
	}
	var subgroupsOverride animedomain.OverrideValue
	var rulesOverrides map[string]animedomain.OverrideValue
	var effectiveVersions EffectiveConfigVersions
	if materializedSections == nil {
		subgroupsOverride, err = subgroupsOverrideForDraft(draft)
		if err != nil {
			return SubscribeResult{}, err
		}
		if err := upsertOverride(ctx, tx, animeID, "subgroups", animedomain.SectionSubgroups, subgroupsOverride); err != nil {
			return SubscribeResult{}, err
		}
		rulesOverrides, _ = animedomain.RulesSectionOverrides(draft.Rules.Sections)
		for _, section := range rulesSectionOrder {
			if err := upsertOverride(ctx, tx, animeID, "rules", section, rulesOverrides[section]); err != nil {
				return SubscribeResult{}, err
			}
		}
	}
	effectiveVersions = ownedConfigVersions()
	// tag_filters is rules configuration the draft carries. It is not one of
	// the frozen sections a saved revision materializes, so both paths write
	// the draft's value (the revision's integrity-checked draft body on
	// activation) as the new instance's value: exactly the value the draft's
	// preview resolves (ResolveDraftTagFilters), so a draft without the section
	// stores the code default. Global is never read.
	resolvedTagFilters, err := animedomain.ResolveDraftTagFilters(draft)
	if err != nil {
		return SubscribeResult{}, draftInvalidError([]animedomain.FieldViolation{{Field: "rules." + animedomain.SectionTagFilters, Code: "OVERRIDE_MODE_INVALID", Message: err.Error()}})
	}
	tagFiltersValue, err := json.Marshal(resolvedTagFilters.Rules)
	if err != nil {
		return SubscribeResult{}, err
	}
	if err := upsertOverride(ctx, tx, animeID, "rules", animedomain.SectionTagFilters, animedomain.OverrideValue{Value: tagFiltersValue}); err != nil {
		return SubscribeResult{}, err
	}

	sources := make([]RSSSource, 0, len(draft.RssSources))
	for _, sourceDraft := range draft.RssSources {
		source, err := insertRSSSource(ctx, tx, subscriptionID, sourceDraft)
		if err != nil {
			return SubscribeResult{}, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO rss_source_runtime(rss_source_id) VALUES ($1)`, source.ID); err != nil {
			return SubscribeResult{}, fmt.Errorf("create rss source runtime: %w", err)
		}
		// Only a started subscription refreshes at once. A pending one does
		// no work until its first start schedules this refresh; a stopped
		// one-off instance is driven by explicit manual refreshes instead.
		if initialState == subscriptiondomain.SubscriptionActive {
			if _, err := s.workItems.Create(ctx, tx, workitems.WorkItemSpec{
				Owner:                workitems.OwnerRef{Type: workitems.OwnerRSSSourceRuntime, ID: source.ID},
				Kind:                 workitems.KindRSSRefresh,
				IdempotencyKey:       "rss_refresh:" + source.ID,
				InputSnapshot:        json.RawMessage(`{"trigger":"subscribe_initial"}`),
				MaxAttempts:          defaultMaxAttempts,
				ExpectedOwnerVersion: 1,
			}); err != nil {
				return SubscribeResult{}, fmt.Errorf("schedule initial refresh: %w", err)
			}
		}
		sources = append(sources, source)
	}

	result := SubscribeResult{
		ManagedAnime: ManagedAnime{ID: animeID, Title: title, TitleOverride: cloneString(fixedTitle), Version: 1},
		Subscription: Subscription{
			ID:              subscriptionID,
			ManagedAnimeID:  animeID,
			Origin:          string(draft.Origin),
			OriginEntryRef:  cloneString(draft.OriginEntryRef),
			InstanceLabel:   cloneString(draft.InstanceLabel),
			State:           initialState,
			LifecycleState:  "active",
			LifecycleEpoch:  1,
			Version:         1,
			NewReleasesOnly: draft.NewReleasesOnly,
			StartBaselineAt: startBaselineAt,
		},
		Metadata:                metadata,
		RssSources:              sources,
		EffectiveConfigVersions: effectiveVersions,
	}
	snapshot, err := json.Marshal(result)
	if err != nil {
		return SubscribeResult{}, fmt.Errorf("encode subscribe result: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO idempotency_records(kind, idempotency_key, request_hash, managed_anime_id, result_snapshot)
VALUES ('subscribe', $1, $2, $3, $4)`, key, requestHash, animeID, snapshot); err != nil {
		return SubscribeResult{}, fmt.Errorf("record subscribe idempotency: %w", err)
	}
	if candidateID != "" {
		if err := linkActivatedCandidate(ctx, tx, candidateID, animeID, expectedVersion, candidateVersion); err != nil {
			return SubscribeResult{}, err
		}
	}
	if err := writeSubscriptionCreatedLog(ctx, tx, s.activityWriter, subscriptionID, animeID, title); err != nil {
		return SubscribeResult{}, err
	}
	if s.notifications != nil && initialState != subscriptiondomain.SubscriptionPending {
		// A replayed subscribe returns before this point, so the new
		// subscription admits exactly one started notice. A pending
		// subscription admits it at its first start instead.
		noticeTitle := title
		if fixedTitle != nil {
			noticeTitle = *fixedTitle
		}
		request, err := plugintargetport.SubscriptionChangedRequest(subscriptionID, 1, animeID, noticeTitle, plugintargetport.SubscriptionStarted)
		if err != nil {
			return SubscribeResult{}, fmt.Errorf("encode subscription notice: %w", err)
		}
		if err := s.notifications.EnqueueBusinessDelivery(ctx, txscope.Wrap(tx), request); err != nil {
			return SubscribeResult{}, fmt.Errorf("enqueue subscription notice: %w", err)
		}
	}
	if s.observability != nil && !metadataCompleteApplication(metadata) {
		if err := s.observability.UpsertRoutineTx(ctx, txscope.Wrap(tx), observabilityport.RoutineRequest{
			Identity: observabilityport.MetadataMatchRoutine(observabilityport.RoutineOutcomeWarning, observabilityport.RoutineResultMetadataPending),
			ScopeID:  animeID, ManagedAnimeID: animeID, Summary: "Waiting for complete metadata.",
		}); err != nil {
			return SubscribeResult{}, fmt.Errorf("record metadata hold: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return SubscribeResult{}, fmt.Errorf("commit subscribe transaction: %w", err)
	}
	s.publishInvalidations(events.TargetSubscribed, events.TargetCatalog)
	if postCommit != nil {
		postCommit(ctx, result)
	}
	return result, nil
}

// insertRSSSource persists one source draft as an opaque RSSSource
// configuration row. Safety and transport checks happen when the source is
// actually refreshed, not while the Subscribe transaction writes the owner.
func insertRSSSource(ctx context.Context, tx *sql.Tx, subscriptionID string, sourceDraft animedomain.RSSSourceDraft) (RSSSource, error) {
	sourceID := uuid.NewString()
	locator := sourceDraft.Locator
	locatorDisplay := animedomain.LocatorDisplay(locator)
	var provenance any
	if sourceDraft.ProviderProvenance != nil {
		raw, err := json.Marshal(sourceDraft.ProviderProvenance)
		if err != nil {
			return RSSSource{}, fmt.Errorf("encode rss source provenance: %w", err)
		}
		provenance = raw
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO rss_sources(
    id, subscription_id, source_kind, locator, locator_display, group_name, title,
    episode_offset, anibt_aux_mode, provenance, http_feed_connection_ref, network_route_ref
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, NULLIF($11, ''), NULLIF($12, ''))`,
		sourceID, subscriptionID, string(sourceDraft.SourceKind), locator, locatorDisplay,
		strings.TrimSpace(sourceDraft.GroupName), strings.TrimSpace(sourceDraft.Title), sourceDraft.EpisodeOffset,
		nullableAnibtMode(sourceDraft.AnibtAuxMode), provenance,
		nullableOptionalText(sourceDraft.HttpFeedConnectionRef), nullableOptionalText(sourceDraft.NetworkRouteRef)); err != nil {
		return RSSSource{}, fmt.Errorf("create rss source: %w", err)
	}
	return RSSSource{
		ID:                    sourceID,
		SubscriptionID:        subscriptionID,
		SourceKind:            sourceDraft.SourceKind,
		LocatorDisplay:        locatorDisplay,
		GroupName:             strings.TrimSpace(sourceDraft.GroupName),
		Title:                 strings.TrimSpace(sourceDraft.Title),
		EpisodeOffset:         sourceDraft.EpisodeOffset,
		AnibtAuxMode:          cloneAnibtModeApplication(sourceDraft.AnibtAuxMode),
		ProviderProvenance:    cloneProviderProvenance(sourceDraft.ProviderProvenance),
		HttpFeedConnectionRef: cloneString(sourceDraft.HttpFeedConnectionRef),
		NetworkRouteRef:       cloneString(sourceDraft.NetworkRouteRef),
		Version:               1,
	}, nil
}

// subgroupsOverrideForDraft stores the draft's own complete subgroups section
// for the new subscription (one-time input; an empty entry list is a value).
func subgroupsOverrideForDraft(draft animedomain.AnimeDraft) (animedomain.OverrideValue, error) {
	override, violations := animedomain.SubgroupsSectionOverride(draft.Subgroups)
	if len(violations) > 0 {
		return animedomain.OverrideValue{}, errors.New(violations[0].Message)
	}
	return override, nil
}

func sanitizeRSSSourceDisplays(sources []RSSSource) {
	for index := range sources {
		sources[index].LocatorDisplay = animedomain.SanitizeLocatorDisplay(sources[index].LocatorDisplay)
	}
}

func idempotencyKeyReusedError() error {
	return &apperrors.Business{Status: 409, Code: "IDEMPOTENCY_KEY_REUSED", Message: "The idempotency key was already used with a different activation identity."}
}

func candidateAlreadyActivatedError() error {
	return &apperrors.Business{Status: 409, Code: "CANDIDATE_ALREADY_ACTIVATED", Message: "候选已激活，不能创建第二个实例。"}
}

// CandidateNotWorkingError returns the stable conflict used when a saved draft
// can no longer be used by an execution request.
func (s *Service) CandidateNotWorkingError() error { return candidateNotWorkingError() }

// CandidateRevisionMismatchError returns the stable conflict when a requested
// immutable revision differs from the current saved candidate revision.
func (s *Service) CandidateRevisionMismatchError(current, requested int64) error {
	return candidateRevisionMismatchError(current, requested)
}

// CandidateDigestMismatchError returns the stable integrity conflict for a
// saved candidate revision whose persisted identity does not verify.
func (s *Service) CandidateDigestMismatchError() error { return candidateDigestMismatchError() }

func metadataCompleteApplication(metadata AnimeMetadata) bool {
	return metadata.BGM != nil && metadata.BGM.WorkID != "" &&
		metadata.TMDB != nil && metadata.TMDB.WorkID != "" &&
		metadata.TMDB.MediaKind != "" && metadata.TMDB.Season >= 0
}

// cloneAnibtMode converts the draft aux mode into the read model type.
func cloneAnibtModeApplication(mode *animedomain.RSSSourceDraftAnibtAuxMode) *animedomain.RSSSourceDraftAnibtAuxMode {
	if mode == nil {
		return nil
	}
	value := *mode
	return &value
}

func cloneProviderProvenance(value *animedomain.ProviderProvenance) *animedomain.ProviderProvenance {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

// nullableAnibtMode converts the optional AniBT aux mode into a DB value.
func nullableAnibtMode(mode *animedomain.RSSSourceDraftAnibtAuxMode) any {
	if mode == nil {
		return nil
	}
	return string(*mode)
}
