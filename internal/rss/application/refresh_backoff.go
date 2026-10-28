package application

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"

	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/observability/outbound"
	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/persistence/txscope"
	subscriptiondomain "bansoko/internal/subscription/domain"
	"bansoko/internal/workitems"
)

// circuitEffect carries what the owner writer observed about the provider
// circuit to the after-commit diagnostics (the writer runs first).
type circuitEffect struct {
	paused    bool
	recovered bool
}

// circuitProvider is the provider whose circuit a source feeds; custom RSS
// sources have none.
func circuitProvider(snapshot sourceSnapshot) string {
	if snapshot.sourceKind != animedomain.SourceKindProviderGroup {
		return ""
	}
	switch snapshot.provider {
	case "anibt", "mikan":
		return snapshot.provider
	}
	return ""
}

// refreshFailureWriter records one fetch/parse failure: the ordinary degraded
// runtime write, one more consecutive source failure (the backoff input) and
// one more failure on the provider circuit, which pauses automatic dispatch for
// ProviderCircuitCooldown once ProviderCircuitThreshold is reached.
func (s *Service) refreshFailureWriter(claim workitems.Claim, snapshot sourceSnapshot, reason string, effect *circuitEffect) workitems.OwnerWriter {
	base := s.refreshOwnerWriter(claim, snapshot, "degraded", "source_degraded", reason, nil)
	return func(ctx context.Context, scope txscope.Tx, result workitems.OwnerResult) (int64, error) {
		version, err := base(ctx, scope, result)
		if err != nil {
			return 0, err
		}
		tx := scope.SQLTx()
		if _, err := tx.ExecContext(ctx, `UPDATE rss_source_runtime SET consecutive_failures = consecutive_failures + 1 WHERE rss_source_id = $1`, snapshot.sourceID); err != nil {
			return 0, fmt.Errorf("count RSS source failure: %w", err)
		}
		provider := circuitProvider(snapshot)
		if provider == "" {
			return version, nil
		}
		var paused bool
		if err := tx.QueryRowContext(ctx, `
INSERT INTO rss_provider_circuits AS c(provider, consecutive_failures, last_failure_at, paused_until)
VALUES ($1, 1, CURRENT_TIMESTAMP, CASE WHEN 1 >= $2 THEN CURRENT_TIMESTAMP + make_interval(secs => $3) END)
ON CONFLICT (provider) DO UPDATE SET
    consecutive_failures = c.consecutive_failures + 1,
    last_failure_at = CURRENT_TIMESTAMP,
    paused_until = CASE
        WHEN c.consecutive_failures + 1 >= $2 AND (c.paused_until IS NULL OR c.paused_until <= CURRENT_TIMESTAMP)
            THEN CURRENT_TIMESTAMP + make_interval(secs => $3)
        ELSE c.paused_until END,
    version = c.version + 1, updated_at = CURRENT_TIMESTAMP
RETURNING COALESCE(paused_until > CURRENT_TIMESTAMP, FALSE)`, provider, subscriptiondomain.ProviderCircuitThreshold, subscriptiondomain.ProviderCircuitCooldown.Seconds()).Scan(&paused); err != nil {
			return 0, fmt.Errorf("record RSS provider circuit failure: %w", err)
		}
		effect.paused = paused
		return version, nil
	}
}

// resetRefreshFailuresTx clears the source failure streak and closes the
// provider circuit after a successful (or 304) refresh.
func resetRefreshFailuresTx(ctx context.Context, tx *sql.Tx, snapshot sourceSnapshot, effect *circuitEffect) error {
	if _, err := tx.ExecContext(ctx, `UPDATE rss_source_runtime SET consecutive_failures = 0 WHERE rss_source_id = $1 AND consecutive_failures <> 0`, snapshot.sourceID); err != nil {
		return fmt.Errorf("reset RSS source failures: %w", err)
	}
	provider := circuitProvider(snapshot)
	if provider == "" {
		return nil
	}
	var reset bool
	err := tx.QueryRowContext(ctx, `
UPDATE rss_provider_circuits
SET consecutive_failures = 0, paused_until = NULL, last_success_at = CURRENT_TIMESTAMP,
    version = version + 1, updated_at = CURRENT_TIMESTAMP
WHERE provider = $1 AND (consecutive_failures <> 0 OR paused_until IS NOT NULL)
RETURNING TRUE`, provider).Scan(&reset)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("reset RSS provider circuit: %w", err)
	}
	if effect != nil {
		effect.recovered = true
	}
	return nil
}

// refreshFailureNote is what a failed refresh tells the diagnostics: the
// error (for its attached outbound facts only) and the variant the failure
// maps to when the provider circuit did not just trip.
type refreshFailureNote struct {
	err     error
	variant observabilityport.ConditionVariant
}

// siteTarget names the diagnostic target of a source: the provider for the
// two catalog providers, the feed host for a custom RSS source.
func siteTarget(snapshot sourceSnapshot, failure outbound.Failure) (id, name, host string) {
	if provider := circuitProvider(snapshot); provider != "" {
		return provider, provider, ""
	}
	host = failure.Host
	if host == "" {
		if raw, err := animedomain.LocatorURL(snapshot.locator); err == nil {
			if parsed, perr := url.Parse(raw); perr == nil {
				host = parsed.Hostname()
			}
		}
	}
	if host == "" {
		return "", "", ""
	}
	return "host:" + host, host, host
}

// circuitAfterCommit records the site diagnostic of one refresh outcome: a
// failure opens (or merges into) the source's target, as site.paused when the
// provider circuit just tripped; a success closes it. A nil note is a success.
func (s *Service) circuitAfterCommit(snapshot sourceSnapshot, effect *circuitEffect, note *refreshFailureNote) func(context.Context) {
	return func(ctx context.Context) {
		if note == nil {
			if record := observabilityport.DiagnosticSuccessAfterCommit(s.diagnostics, s.logger,
				observabilityport.InternalSuccess(observabilityport.InternalRSSRefresh)); record != nil {
				record(ctx)
			}
			failure := outbound.Failure{}
			id, _, _ := siteTarget(snapshot, failure)
			if id == "" {
				return
			}
			if record := observabilityport.DiagnosticSuccessAfterCommit(s.diagnostics, s.logger, observabilityport.DiagnosticSuccessRequest{
				Kind: observabilityport.KindSite, TargetID: id,
			}); record != nil {
				record(ctx)
			}
			return
		}
		failure, _ := outbound.FailureOf(note.err)
		id, name, host := siteTarget(snapshot, failure)
		if id == "" {
			return
		}
		variant, severity := note.variant, observabilityport.SeverityWarning
		if effect.paused {
			variant = observabilityport.VariantPaused
		}
		if record := observabilityport.DiagnosticFailureAfterCommit(s.diagnostics, s.logger, observabilityport.DiagnosticFailureRequest{
			Condition: observabilityport.Condition{Kind: observabilityport.KindSite, Variant: variant},
			Severity:  severity, TargetID: id, TargetName: name, Failure: withHost(failure, host),
			AffectedID: affectedAnime(variant, snapshot),
		}); record != nil {
			record(ctx)
		}
	}
}

func withHost(failure outbound.Failure, host string) outbound.Failure {
	if failure.Host == "" {
		failure.Host = host
	}
	return failure
}

// affectedAnime is the anime a refresh failure kept from updating; only a
// plain refresh failure counts it, a pause or unreadable feed is source-wide.
func affectedAnime(variant observabilityport.ConditionVariant, snapshot sourceSnapshot) string {
	if variant == observabilityport.VariantRefreshFailed {
		return snapshot.managedAnimeID
	}
	return ""
}
