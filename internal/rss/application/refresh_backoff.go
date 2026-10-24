package application

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	animedomain "bansoko/internal/anime/domain"
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

// circuitAfterCommit opens or closes the provider-scoped pause diagnostic
// according to what the owner writer observed.
func (s *Service) circuitAfterCommit(snapshot sourceSnapshot, effect *circuitEffect, next func(context.Context)) func(context.Context) {
	provider := circuitProvider(snapshot)
	return func(ctx context.Context) {
		if next != nil {
			next(ctx)
		}
		if provider == "" {
			return
		}
		switch {
		case effect.paused:
			if record := observabilityport.DiagnosticFailureAfterCommit(s.diagnostics, s.logger, observabilityport.DiagnosticFailureRequest{
				Identity: observabilityport.RSSProviderPaused(), Severity: observabilityport.SeverityWarning,
				ScopeID: provider, LastError: "automatic RSS refresh for this provider is paused after repeated failures",
			}); record != nil {
				record(ctx)
			}
		case effect.recovered:
			if record := observabilityport.DiagnosticSuccessAfterCommit(s.diagnostics, s.logger, observabilityport.DiagnosticSuccessRequest{
				Identity: observabilityport.RSSProviderPaused(), ScopeID: provider,
			}); record != nil {
				record(ctx)
			}
		}
	}
}
