package matcher

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"bansoko/internal/observability/outbound"
	observabilityport "bansoko/internal/observability/port"
)

const (
	// diagnosticFailureThreshold is how many failures of one source inside
	// diagnosticFailureWindow open its episode; a single blip stays silent.
	diagnosticFailureThreshold = 2
	diagnosticFailureWindow    = 10 * time.Minute

	sourceBGM  = "bgm"
	sourceTMDB = "tmdb"
)

type affectedAnimeKey struct{}

// WithAffectedAnime tags ctx with the anime a match is for, so a metadata
// episode can count the anime a provider outage kept from being matched.
func WithAffectedAnime(ctx context.Context, managedAnimeID string) context.Context {
	return context.WithValue(ctx, affectedAnimeKey{}, managedAnimeID)
}

func affectedAnime(ctx context.Context) string {
	id, _ := ctx.Value(affectedAnimeKey{}).(string)
	return id
}

// requestFailure attaches the structured outbound fact to a transport error:
// no answer (network, timeout). A cancelled request carries no fact.
func requestFailure(client *http.Client, req *http.Request, err error) error {
	if errors.Is(err, context.Canceled) {
		return err
	}
	return outbound.WithFailure(err, outbound.Describe(err, req, nil, outbound.ViaOf(client, req)))
}

// statusFailure wraps a non-2xx answer with its fact when the user can act on
// it: a server error or a refused credential. A 404 or any other answer
// carries no fact and is never diagnosed.
func statusFailure(base error, client *http.Client, req *http.Request, resp *http.Response) error {
	if resp.StatusCode >= 500 || resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return outbound.WithFailure(base, outbound.Describe(nil, req, resp, outbound.ViaOf(client, req)))
	}
	return base
}

// diagnosticTracker keeps the per-source failure window. It lives in memory:
// the window is ten minutes, and the episode it opens is durable.
type diagnosticTracker struct {
	mu       sync.Mutex
	recorder observabilityport.Recorder
	now      func() time.Time
	failures map[string][]time.Time
	// mayBeOpen is true until the source succeeds once after start, and again
	// after each failure: an episode opened by a previous process is closed by
	// the first success, without a database call per ordinary success.
	mayBeOpen map[string]bool
	checked   map[string]bool
}

// SetDiagnostics wires the recorder that receives metadata.unreachable and
// metadata.auth_failed. Without one the matcher only logs.
func (m *Matcher) SetDiagnostics(recorder observabilityport.Recorder) {
	m.diag = &diagnosticTracker{recorder: recorder, now: func() time.Time { return time.Now().UTC() }, failures: map[string][]time.Time{}, mayBeOpen: map[string]bool{}, checked: map[string]bool{}}
}

var sourceNames = map[string]string{sourceBGM: "Bangumi", sourceTMDB: "TMDB"}

// observe feeds the outcome of one provider layer into the detection. err nil
// is a success of source and closes its episode; an error without an outbound
// fact (configuration, no result) is neither.
func (m *Matcher) observe(ctx context.Context, source string, err error) {
	if m == nil || m.diag == nil || m.diag.recorder == nil {
		return
	}
	t := m.diag
	if err == nil {
		t.mu.Lock()
		due := t.mayBeOpen[source] || !t.checked[source]
		t.checked[source] = true
		delete(t.failures, source)
		t.mayBeOpen[source] = false
		t.mu.Unlock()
		if due {
			observabilityport.DiagnosticSuccessAfterCommit(t.recorder, m.logger, observabilityport.DiagnosticSuccessRequest{Kind: observabilityport.KindMetadata, TargetID: source})(ctx)
		}
		return
	}
	failure, ok := outbound.FailureOf(err)
	if !ok {
		return
	}
	now := t.now()
	t.mu.Lock()
	window := t.failures[source][:0:0]
	for _, at := range t.failures[source] {
		if now.Sub(at) < diagnosticFailureWindow {
			window = append(window, at)
		}
	}
	window = append(window, now)
	t.failures[source] = window
	t.mayBeOpen[source] = true
	open := len(window) >= diagnosticFailureThreshold
	t.mu.Unlock()
	if !open {
		return
	}
	variant := observabilityport.VariantUnreachable
	if failure.Class == outbound.ClassHTTPStatus && (failure.Status == http.StatusUnauthorized || failure.Status == http.StatusForbidden) {
		variant = observabilityport.VariantAuthFailed
	}
	observabilityport.DiagnosticFailureAfterCommit(t.recorder, m.logger, observabilityport.DiagnosticFailureRequest{
		Condition: observabilityport.Condition{Kind: observabilityport.KindMetadata, Variant: variant},
		Severity:  observabilityport.SeverityWarning, TargetID: source, TargetName: sourceNames[source],
		Failure: failure, AffectedID: affectedAnime(ctx),
	})(ctx)
}
