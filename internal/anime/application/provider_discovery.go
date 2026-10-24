package application

import (
	"context"
	"reflect"

	observabilityport "bansoko/internal/observability/port"
)

// The bounded runtime event an after-commit discovery degradation emits. The
// reason code is a stable identifier, so the owning package emits it through
// the kernel's Discovery holder and asserts the same vocabulary in tests.
const (
	ProviderDiscoveryOperation  = "group_discovery_after_commit"
	ProviderDiscoveryMessage    = "provider group discovery after commit degraded"
	ProviderDiscoveryComponent  = "anime"
	ProviderDiscoveryErrorClass = "group_discovery"
	ProviderDiscoveryReasonCode = "discovery_failed"
)

func providerOriginRequiresDiscovery(origin string) bool {
	return origin == "anibt" || origin == "mikan"
}

// IsTypedNil reports whether value is nil, including a typed nil held in an interface.
func IsTypedNil(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Ptr, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

func isTypedNil(value any) bool { return IsTypedNil(value) }

func safeRuntimeLogger(logger observabilityport.RuntimeLogger) observabilityport.RuntimeLogger {
	if isTypedNil(logger) {
		return observabilityport.NopRuntimeLogger{}
	}
	return logger
}

func SafeProviderDiscoveryWarning(ctx context.Context, logger observabilityport.RuntimeLogger, managedAnimeID string) {
	if ctx == nil {
		ctx = context.Background()
	}
	logger = safeRuntimeLogger(logger)
	defer func() {
		_ = recover()
	}()
	logger.Warn(ctx, providerDiscoveryWarning(managedAnimeID))
}

func providerDiscoveryWarning(managedAnimeID string) observabilityport.RuntimeEvent {
	return observabilityport.RuntimeEvent{
		Message:    ProviderDiscoveryMessage,
		Component:  ProviderDiscoveryComponent,
		Operation:  ProviderDiscoveryOperation,
		Outcome:    "warning",
		ReasonCode: ProviderDiscoveryReasonCode,
		OwnerType:  "managed_anime",
		OwnerID:    managedAnimeID,
		ErrorClass: ProviderDiscoveryErrorClass,
	}
}

// SetProvider wires the per-anime group discovery backend. It is the kernel
// capability behind Service.SetGroupDiscoveryProvider, so a use-case package
// that owns discovery-triggering commands can wire the same shared holder
// without reaching into its fields.
func (d *Discovery) SetProvider(provider GroupDiscoveryProvider) {
	if d == nil {
		return
	}
	d.provider = provider
}

// SetLogger wires the diagnostics sink used by the after-commit discovery
// warning. A nil logger restores the no-op default.
func (d *Discovery) SetLogger(logger observabilityport.RuntimeLogger) {
	if d == nil {
		return
	}
	d.logger = safeRuntimeLogger(logger)
}

func (d *Discovery) DiscoverAfterCommit(ctx context.Context, managedAnimeID, origin string) {
	if d == nil || !providerOriginRequiresDiscovery(origin) {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	logger := safeRuntimeLogger(d.logger)
	if isTypedNil(d.provider) {
		SafeProviderDiscoveryWarning(ctx, logger, managedAnimeID)
		return
	}

	var result DiscoverResult
	var err error
	panicked := true
	func() {
		defer func() { _ = recover() }()
		result, err = d.provider.DiscoverGroups(ctx, managedAnimeID)
		panicked = false
	}()
	if panicked || err != nil {
		SafeProviderDiscoveryWarning(ctx, logger, managedAnimeID)
		return
	}
	switch result.Outcome {
	case "discovered", "no_groups":
		return
	default:
		SafeProviderDiscoveryWarning(ctx, logger, managedAnimeID)
	}
}
