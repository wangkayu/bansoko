package application

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/observability/outbound"
	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/redact"
	subscriptiondomain "bansoko/internal/subscription/domain"
	"bansoko/internal/workitems"
)

// circuitEffect carries what the owner writer observed about the provider
// circuit to the after-commit diagnostics (the writer runs first).
type circuitEffect struct {
	paused    bool
	recovered bool

	// outcome carries the confirmed refresh result the owner writer applied.
	// The after-commit log reads it only after the transaction commits.
	outcome refreshLogOutcome
}

// refreshLogOutcome is the writer's confirmed business result. Zero values
// mean the writer did not record a committed result.
type refreshLogOutcome struct {
	committed      bool
	skipReason     string
	notModified    bool
	resumeBaseline bool
	newCount       int
	windowTotal    int
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

// refreshSkipNotSelected and refreshSkipInactive distinguish a pass that read
// the feed but whose owner writer applied nothing.
const (
	refreshSkipNotSelected = "provider_group_not_selected"
	refreshSkipInactive    = "subscription_inactive"
)

func refreshRuntimeEvent(claim workitems.Claim) observabilityport.RuntimeEvent {
	return observabilityport.RuntimeEvent{
		Component:  "rss",
		Operation:  "refresh",
		OwnerType:  claim.Owner.Type,
		OwnerID:    claim.Owner.ID,
		WorkItemID: claim.ID,
		Kind:       claim.Kind,
		Attempt:    claim.Attempt,
	}
}

// refreshLogSubject uses only the source's display labels, never its locator.
// Drop an unsafe label separately so it cannot mask the actual result line.
func refreshLogSubject(snapshot sourceSnapshot) string {
	return rssLogSubject("RSS refresh", snapshot.title, snapshot.groupName)
}

func rssLogSubject(subject, title, group string) string {
	label := func(raw string) string {
		text, sensitive := redact.Apply(raw, 96, redact.Reject, "")
		if sensitive {
			return ""
		}
		return text
	}
	title, group = label(title), label(group)
	switch {
	case title != "" && group != "" && !strings.EqualFold(title, group):
		return fmt.Sprintf("%s for %q (%q)", subject, title, group)
	case title != "":
		return fmt.Sprintf("%s for %q", subject, title)
	case group != "":
		return fmt.Sprintf("%s for group %q", subject, group)
	default:
		return subject
	}
}

func (s *Service) logRefreshSkipped(ctx context.Context, claim workitems.Claim, snapshot sourceSnapshot, reason, message string) {
	if s == nil || s.logger == nil || ctx.Err() != nil {
		return
	}
	event := refreshRuntimeEvent(claim)
	event.Outcome = "skipped"
	event.ReasonCode = reason
	event.Message = refreshLogSubject(snapshot) + ": " + message
	s.logger.Info(ctx, event)
}

func refreshReadFailureMessage(code string, err error) string {
	switch code {
	case acquisitiondomain.FeedFailureNoUsableItems:
		return "the feed contained no usable release entries"
	case acquisitiondomain.FeedFailureSiteFormatChanged:
		return "the feed did not match the expected format for this site"
	}
	failure, _ := outbound.FailureOf(err)
	switch failure.Class {
	case outbound.ClassHTTPStatus:
		if failure.Status >= 100 && failure.Status <= 599 {
			return fmt.Sprintf("the feed request returned HTTP %d", failure.Status)
		}
	case outbound.ClassTimeout:
		return "the feed request timed out"
	case outbound.ClassDNS:
		return "could not resolve the feed host"
	case outbound.ClassTLS:
		return "could not establish a secure connection to the feed"
	case outbound.ClassConnectionRefused:
		return "the feed connection was refused"
	case outbound.ClassConnectionReset:
		return "the feed connection was reset"
	}
	return "could not read the feed"
}

// logRefreshFailure reports the failed stage of this attempt, not the result
// of writing back its state or scheduling a retry. The latter can still fail.
func (s *Service) logRefreshFailure(ctx context.Context, claim workitems.Claim, snapshot sourceSnapshot, stage, code, message string) {
	if s.logger == nil || ctx.Err() != nil {
		return
	}
	event := refreshRuntimeEvent(claim)
	event.Outcome = "failure"
	event.Stage = stage
	event.ReasonCode = code
	event.Message = refreshLogSubject(snapshot) + ": " + message
	s.logger.Warn(ctx, event)
}

// logRefreshOutcome records only what committed. Source display labels are
// screened separately; the result never includes a URL or raw error.
func (s *Service) logRefreshOutcome(ctx context.Context, claim workitems.Claim, snapshot sourceSnapshot, effect *circuitEffect) {
	if s == nil || s.logger == nil || effect == nil {
		return
	}
	result := effect.outcome
	if !result.committed && result.skipReason == "" {
		return
	}
	event := refreshRuntimeEvent(claim)
	switch {
	case result.skipReason != "":
		event.Outcome = "skipped"
		event.ReasonCode = result.skipReason
		if result.skipReason == refreshSkipNotSelected {
			event.Message = "skipped because the provider group is no longer selected"
		} else {
			event.Message = "skipped because the subscription is no longer active"
		}
	case result.notModified:
		event.Message = "the feed did not change (HTTP 304)"
		event.Outcome = "success"
		event.ReasonCode = "rss_refresh_not_modified"
	case result.resumeBaseline:
		event.Message = fmt.Sprintf("saved the resume baseline with %d new entries out of %d checked; no releases were evaluated", result.newCount, result.windowTotal)
		event.Outcome = "success"
		event.ReasonCode = "rss_refresh_resume_baseline"
	case result.newCount > 0:
		event.Message = fmt.Sprintf("checked %d entries and saved %d new releases", result.windowTotal, result.newCount)
		event.Outcome = "success"
		event.ReasonCode = "rss_refresh_new_releases"
	case result.windowTotal > 0:
		event.Message = fmt.Sprintf("checked %d entries and found no new releases", result.windowTotal)
		event.Outcome = "success"
		event.ReasonCode = "rss_refresh_no_new_releases"
	default:
		if result.committed {
			event.Outcome = "success"
			event.ReasonCode = "rss_refresh_empty_feed"
			event.Message = "the feed contained no release entries"
		} else {
			return
		}
	}
	event.Message = refreshLogSubject(snapshot) + ": " + event.Message
	s.logger.Info(ctx, event)
}

// circuitAfterCommit records the site diagnostic of one refresh outcome: a
// failure opens (or merges into) the source's target, as site.paused when the
// provider circuit just tripped; a success closes it. A nil note is a success.
func (s *Service) circuitAfterCommit(claim workitems.Claim, snapshot sourceSnapshot, effect *circuitEffect, note *refreshFailureNote) func(context.Context) {
	return func(ctx context.Context) {
		if note == nil {
			s.logRefreshOutcome(ctx, claim, snapshot, effect)
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
