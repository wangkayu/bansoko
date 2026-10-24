// Package application implements the single-anime kernel: the shared
// owner-shape vocabulary, provider/torrent ports, transaction-scoped helpers
// and lifecycle fences every anime bucket builds on, plus the manual-retry
// boundary retired releases use. Candidate drafts and the atomic Subscribe
// transaction live in internal/anime/draft/application, the owner mutations
// in .../ownership/application, finite acquisition in .../oneshot/application
// and the read models in .../readmodel/application; each is injected as an
// independent owner and none of them embeds another's service.
//
// The kernel exposes only caller-scoped machinery: every `...Tx` helper reads
// or writes through a transaction the caller owns, so no bucket needs the
// kernel to run a transaction on its behalf.
package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/events"
	"bansoko/internal/metadata/matcher"
	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/policyowner"
	"bansoko/internal/workitems"
)

type MetadataMatcher interface {
	Match(ctx context.Context, origin, entryRef, title string, skipBgm, skipTmdb bool) (*matcher.MatchResult, error)
}

// GroupDiscoveryProvider runs per-anime group discovery synchronously. It is
// implemented by the automation Dispatcher and wired through the owning
// service's SetGroupDiscoveryProvider.
type GroupDiscoveryProvider interface {
	DiscoverGroups(ctx context.Context, managedAnimeID string) (DiscoverResult, error)
}

// DiscoverResult is the outcome of a per-anime group discovery run.
type DiscoverResult struct {
	Outcome         string // "discovered", "no_groups", "provider_error", "config_error"
	Reason          string
	DiscoveredCount int
}

// RetentionSnapshotPort is the shared caller-owned policy snapshot port.
type RetentionSnapshotPort = policyowner.SnapshotPort

// FrozenParserPolicyPort writes a revision-frozen parser policy as the new
// instance's complete policy snapshot. Activation and one-shot use it instead
// of copying the current global creation default, so a global parser change
// between save and execution cannot alter what the revision froze.
type FrozenParserPolicyPort interface {
	CopyFrozenParserPolicyToInstanceTx(ctx context.Context, scope txscope.Tx, animeID, kind string, sourceVersion int64) error
}

// ReplacementTriggerPort schedules the async replacement reevaluation after a
// Subscription resume committed. It is implemented by the filemaintenance
// after-commit trigger owner; the trigger itself checks the active gate,
// creates a fresh reevaluation epoch and records failures observably. A nil
// port means no reevaluation is scheduled (degraded, never fails resume).
type ReplacementTriggerPort interface {
	TriggerSubscriptionResumed(ctx context.Context, animeID string) error
}

// SourceOffsetMigrationAdmission admits a durable source generation inside the
// RSSSource owner's existing CAS transaction.
type SourceOffsetMigrationAdmission interface {
	AdmitRSSSourceTx(ctx context.Context, scope txscope.Tx, animeID, rssSourceID string, sourceVersion int64, oldEffectiveOffset, newEffectiveOffset int) (generationID string, created bool, err error)
}

// Service holds the kernel ports the anime owners share: the lifecycle fences,
// the manual-injection torrent reader and the owner-resume trigger. Every
// product command lives in its own owner package and receives its own narrow
// ports; nothing here runs a bucket's transaction.
type Service struct {
	db                       *sql.DB
	workItems                *workitems.Store
	metadataMatcher          MetadataMatcher
	fences                   *Fences
	replacement              ReplacementTriggerPort
	offsetMigrationAdmission SourceOffsetMigrationAdmission
}

func NewService(db *sql.DB, workItems *workitems.Store, _ FeedFetcher) (*Service, error) {
	if db == nil {
		return nil, errors.New("anime service database is nil")
	}
	if workItems == nil {
		return nil, errors.New("anime service work item store is nil")
	}
	service := &Service{db: db, workItems: workItems}
	service.fences = &Fences{db: db}
	return service, nil
}

// SetMetadataMatcher configures the provider-backed one-click metadata action.
// It is separate from construction so existing service call sites remain stable.
func (s *Service) SetMetadataMatcher(metadataMatcher MetadataMatcher) {
	s.metadataMatcher = metadataMatcher
}

// SetOffsetMigrationAdmission wires durable generation admission for effective
// RSSSource offset changes.
func (s *Service) SetOffsetMigrationAdmission(admission SourceOffsetMigrationAdmission) {
	if s != nil {
		s.offsetMigrationAdmission = admission
	}
}

// SetReplacementTrigger wires the after-commit replacement trigger port (the
// filemaintenance trigger owner). Resume dispatches a fresh reevaluation epoch
// through it; a nil port degrades to no reevaluation, never a resume failure.
func (s *Service) SetReplacementTrigger(port ReplacementTriggerPort) {
	if s != nil {
		s.replacement = port
	}
}

// InstanceConfigOverride is one complete instance-owned section write produced
// when a saved revision is materialized into a locked instance.
type InstanceConfigOverride struct {
	OwnerType string
	Section   string
	Value     json.RawMessage
}

// MaterializedOverrides orders the complete instance-owned writes for a saved
// revision. Every canonical section must be present and typed; a missing or
// invalid section fails closed instead of silently falling back to a Global
// value. It is exported because the draft owner's activation
// tests assert the materialization contract the kernel's Provisioner applies.
func MaterializedOverrides(resolved animedomain.ResolvedSections) ([]InstanceConfigOverride, error) {
	overrides := make([]InstanceConfigOverride, 0, len(animedomain.EffectiveSectionOrder))
	for _, section := range animedomain.EffectiveSectionOrder {
		raw, ok := resolved.Sections[section]
		if !ok || len(raw) == 0 {
			return nil, fmt.Errorf("materialize instance config: resolved section %s is missing", section)
		}
		if err := animedomain.ValidateSectionValue(section, raw); err != nil {
			return nil, fmt.Errorf("materialize instance config: section %s is not a valid typed value: %w", section, err)
		}
		ownerType := "rules"
		if section == animedomain.SectionSubgroups {
			ownerType = "subgroups"
		}
		overrides = append(overrides, InstanceConfigOverride{OwnerType: ownerType, Section: section, Value: raw})
	}
	return overrides, nil
}

// materializeResolvedOverrides writes one complete instance-owned value per
// canonical section from a saved revision.
func materializeResolvedOverrides(ctx context.Context, tx *sql.Tx, animeID string, resolved animedomain.ResolvedSections) error {
	overrides, err := MaterializedOverrides(resolved)
	if err != nil {
		return err
	}
	for _, override := range overrides {
		if err := upsertOverride(ctx, tx, animeID, override.OwnerType, override.Section, animedomain.OverrideValue{Value: override.Value}); err != nil {
			return err
		}
	}
	return nil
}

type Invalidator struct {
	hub *events.Hub
}

// NewInvalidator returns the shared read-model invalidation publisher for one
// event hub. A nil hub makes Publish a no-op.
func NewInvalidator(hub *events.Hub) *Invalidator {
	return &Invalidator{hub: hub}
}

func (i *Invalidator) Publish(targets ...string) {
	if i == nil || i.hub == nil {
		return
	}
	for _, target := range targets {
		i.hub.Publish(events.Event{Target: target})
	}
}

type Discovery struct {
	provider GroupDiscoveryProvider
	logger   observabilityport.RuntimeLogger
}
