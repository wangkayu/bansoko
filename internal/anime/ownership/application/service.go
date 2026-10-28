// Package application implements the anime ownership use cases: subscription
// retirement and local cleanup, owner CAS writes and readers, RSS source
// actions, provider-group discovery, metadata mutation/matching, release
// reevaluation, and manual release injection. Shared owner provisioning and
// lifecycle fences stay in the anime kernel package; this service stores only
// their narrow ports and never embeds the root service.
package application

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	animeapplication "bansoko/internal/anime/application"
	animeport "bansoko/internal/anime/port"
	"bansoko/internal/events"
	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/persistence/txscope"
	plugintargetport "bansoko/internal/plugintarget/port"
	facts "bansoko/internal/release/facts"
	"bansoko/internal/subscription/groupsource"
	"bansoko/internal/workitems"
)

// Kernel owner-shape vocabulary the ownership projections carry. These are
// aliases, so one shape crosses every boundary instead of a second definition.
type (
	ManagedAnime            = animeapplication.ManagedAnime
	Subscription            = animeapplication.Subscription
	AnimeMetadata           = animeapplication.AnimeMetadata
	MetadataHold            = animeapplication.MetadataHold
	BGMBinding              = animeapplication.BGMBinding
	TMDBBinding             = animeapplication.TMDBBinding
	RSSSource               = animeapplication.RSSSource
	RSSSourceRuntime        = animeapplication.RSSSourceRuntime
	RSSCursor               = animeapplication.RSSCursor
	RSSLastGoodFacts        = animeapplication.RSSLastGoodFacts
	FrozenDownloadInput     = animeapplication.FrozenDownloadInput
	FrozenPolicySnapshot    = animeapplication.FrozenPolicySnapshot
	ConfigRevisionIdentity  = animeapplication.ConfigRevisionIdentity
	SelectedReleaseIdentity = animeapplication.SelectedReleaseIdentity
)

// Kernel-owned ports and vocabulary the ownership commands consume.
type (
	MetadataMatcher                = animeapplication.MetadataMatcher
	GroupDiscoveryProvider         = animeapplication.GroupDiscoveryProvider
	DiscoverResult                 = animeapplication.DiscoverResult
	ReplacementTriggerPort         = animeapplication.ReplacementTriggerPort
	SourceOffsetMigrationAdmission = animeapplication.SourceOffsetMigrationAdmission
)

// Dependencies are the explicit collaborators of the ownership commands. Every
// one is optional at construction except WorkItems; a nil port degrades only
// the command that needs it, and the product owners still fail closed where the
// original service did.
type Dependencies struct {
	WorkItems                *workitems.Store
	OneShotMarkers           animeport.OneShotMarkerRepository
	MetadataMatcher          MetadataMatcher
	EventHub                 *events.Hub
	ActivityWriter           observabilityport.ActivityWriter
	Observability            observabilityport.Recorder
	ReplacementTrigger       ReplacementTriggerPort
	OffsetMigrationAdmission SourceOffsetMigrationAdmission
	// Connections resolves the provider endpoint of a link created before discovery.
	Connections groupsource.ConnectionSource
}

// Service owns the anime ownership mutations and owner-set readers. The kernel
// package supplies the shared fences, invalidation hub, and after-commit
// discovery holder; the draft transaction and the one-shot flow are injected
// into the other owners instead of being reached through this service.
type Service struct {
	db                       *sql.DB
	workItems                *workitems.Store
	oneShotMarkers           animeport.OneShotMarkerRepository
	metadataMatcher          MetadataMatcher
	groupDiscovery           GroupDiscoveryProvider
	discovery                *animeapplication.Discovery
	logger                   observabilityport.RuntimeLogger
	fences                   *animeapplication.Fences
	invalidator              *animeapplication.Invalidator
	replacement              ReplacementTriggerPort
	offsetMigrationAdmission SourceOffsetMigrationAdmission
	connections              groupsource.ConnectionSource
	observability            observabilityport.Recorder
	activityWriter           observabilityport.ActivityWriter
	// mediaFacts writes the Release media fact snapshot a forced acquisition
	// freezes.
	mediaFacts facts.MediaRepository
	// notifications admits subscription_changed notices; nil admits none.
	notifications plugintargetport.BusinessDeliveryEnqueuer
	// metadataRefresher re-pulls bound works bypassing the positive caches
	// (the 刷新元数据 action); nil fails the action closed.
	metadataRefresher MetadataWorkRefresher
}

// SetBusinessDeliveryEnqueuer wires the subscription_changed admission of
// stop, resume and retirement transitions.
func (s *Service) SetBusinessDeliveryEnqueuer(enqueuer plugintargetport.BusinessDeliveryEnqueuer) {
	if s != nil {
		s.notifications = enqueuer
	}
}

// admitSubscriptionChangeTx admits one subscription_changed notice inside the
// transition's transaction. version is the subscription version the
// transition commits.
func (s *Service) admitSubscriptionChangeTx(ctx context.Context, tx *sql.Tx, subscriptionID string, version int64, managedAnimeID, animeTitle, action string) error {
	if s.notifications == nil {
		return nil
	}
	request, err := plugintargetport.SubscriptionChangedRequest(subscriptionID, version, managedAnimeID, animeTitle, action)
	if err != nil {
		return fmt.Errorf("encode subscription notice: %w", err)
	}
	if err := s.notifications.EnqueueBusinessDelivery(ctx, txscope.Wrap(tx), request); err != nil {
		return fmt.Errorf("enqueue subscription notice: %w", err)
	}
	return nil
}

// NewService validates and creates the ownership application service.
func NewService(db *sql.DB, deps Dependencies) (*Service, error) {
	if db == nil {
		return nil, errors.New("ownership service database is nil")
	}
	if deps.WorkItems == nil {
		return nil, errors.New("ownership service work item store is nil")
	}
	logger := observabilityport.RuntimeLogger(observabilityport.NopRuntimeLogger{})
	discovery := &animeapplication.Discovery{}
	discovery.SetLogger(logger)
	service := &Service{
		db:                       db,
		workItems:                deps.WorkItems,
		oneShotMarkers:           deps.OneShotMarkers,
		metadataMatcher:          deps.MetadataMatcher,
		discovery:                discovery,
		logger:                   logger,
		fences:                   animeapplication.NewFences(db),
		invalidator:              animeapplication.NewInvalidator(deps.EventHub),
		replacement:              deps.ReplacementTrigger,
		offsetMigrationAdmission: deps.OffsetMigrationAdmission,
		connections:              deps.Connections,
		observability:            deps.Observability,
		activityWriter:           deps.ActivityWriter,
		mediaFacts:               facts.NewStore(db),
	}
	return service, nil
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

// SetActivityWriter wires the central Activity journal for owner transactions.
func (s *Service) SetActivityWriter(writer observabilityport.ActivityWriter) {
	if s != nil {
		s.activityWriter = writer
	}
}

// SetObservabilityRecorder wires the compact routine/diagnostic projection for
// single-anime owner commands.
func (s *Service) SetObservabilityRecorder(recorder observabilityport.Recorder) {
	if s != nil {
		s.observability = recorder
	}
}

// SetEventHub wires the read-model invalidation boundary.
func (s *Service) SetEventHub(hub *events.Hub) {
	if s != nil {
		s.invalidator = animeapplication.NewInvalidator(hub)
	}
}

// SetMetadataMatcher configures the provider-backed one-click metadata action.
func (s *Service) SetMetadataMatcher(metadataMatcher MetadataMatcher) {
	if s != nil {
		s.metadataMatcher = metadataMatcher
	}
}

// SetGroupDiscoveryProvider wires the per-anime group discovery backend for the
// Subgroups Tab one-click action and the after-commit resume discovery.
func (s *Service) SetGroupDiscoveryProvider(provider GroupDiscoveryProvider) {
	if s == nil {
		return
	}
	s.groupDiscovery = provider
	s.discovery.SetProvider(provider)
}

// SetOneShotMarkerRepository wires the one-shot marker persistence port used by
// explicit retirement protection. Retirement fails closed when it is not
// configured.
func (s *Service) SetOneShotMarkerRepository(repository animeport.OneShotMarkerRepository) {
	if s != nil {
		s.oneShotMarkers = repository
	}
}

// SetReplacementTrigger wires the after-commit replacement trigger port. Resume
// dispatches a fresh reevaluation epoch through it; a nil port degrades to no
// reevaluation, never a resume failure.
func (s *Service) SetReplacementTrigger(port ReplacementTriggerPort) {
	if s != nil {
		s.replacement = port
	}
}

// SetOffsetMigrationAdmission wires durable generation admission for effective
// RSSSource offset changes.
func (s *Service) SetOffsetMigrationAdmission(admission SourceOffsetMigrationAdmission) {
	if s != nil {
		s.offsetMigrationAdmission = admission
	}
}

func (s *Service) publishInvalidations(targets ...string) {
	if s != nil && s.invalidator != nil {
		s.invalidator.Publish(targets...)
	}
}
