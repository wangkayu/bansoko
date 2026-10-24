//go:build soakscale

package soak

import (
	"context"
	"crypto/tls"
	"database/sql"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	observabilityport "bansoko/internal/observability/port"
	rssapplication "bansoko/internal/rss/application"
	runtimeworker "bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
)

// counters counts handled work items per kind.
type counters struct {
	mu sync.Mutex
	n  map[string]int64
}

func newCounters() *counters { return &counters{n: map[string]int64{}} }

func (c *counters) add(kind string) {
	c.mu.Lock()
	c.n[kind]++
	c.mu.Unlock()
}

func (c *counters) get(kind string) int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n[kind]
}

func (c *counters) sum(kinds []string) int64 {
	var t int64
	for _, k := range kinds {
		t += c.get(k)
	}
	return t
}

func (c *counters) total() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	var t int64
	for _, v := range c.n {
		t += v
	}
	return t
}

// countingHandler counts a handler's completions; inner == nil is the no-op
// handler of the claim/settle measurements (it succeeds immediately).
type countingHandler struct {
	kind  string
	inner runtimeworker.Handler
	c     *counters
}

func (h countingHandler) Handle(ctx context.Context, claim workitems.Claim) (runtimeworker.Outcome, error) {
	if h.inner != nil {
		out, err := h.inner.Handle(ctx, claim)
		if err == nil {
			h.c.add(h.kind)
		}
		return out, err
	}
	h.c.add(h.kind)
	return runtimeworker.Finish(workitems.StateSucceeded, workitems.OwnerResult{State: "ok"}, nil), nil
}

func newRegistry(t *testing.T) *runtimeworker.Registry {
	t.Helper()
	reg, err := runtimeworker.NewRegistry(runtimeworker.DefaultPolicies()...)
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

// startRuntime starts the real worker Runtime (both lanes, the production
// policies) with counting handlers; overrides replace the no-op handler of a kind.
func startRuntime(t *testing.T, store *workitems.Store, c *counters, overrides map[string]runtimeworker.Handler) (stop func()) {
	t.Helper()
	reg := newRegistry(t)
	handlers := map[string]runtimeworker.Handler{}
	for _, kind := range reg.Kinds() {
		handlers[kind] = countingHandler{kind: kind, inner: overrides[kind], c: c}
	}
	logger := &captureLogger{events: map[string]*capturedEvent{}}
	rt, err := runtimeworker.NewRuntime(store, reg, handlers, "soak-worker", logger)
	if err != nil {
		t.Fatalf("worker runtime: %v", err)
	}
	rt.Start()
	var once sync.Once
	return func() {
		once.Do(func() {
			_ = rt.Drain(30 * time.Second)
			logger.report(t)
		})
	}
}

// startNoopRuntime is startRuntime with no-op handlers; it returns the running
// count of handled items.
func startNoopRuntime(t *testing.T, store *workitems.Store) (stop func(), processed func() int64) {
	c := newCounters()
	stop = startRuntime(t, store, c, nil)
	return stop, c.total
}

// lightKinds / heavyKinds are the kinds of each lane in the production policies.
func laneKinds(t *testing.T, lane runtimeworker.Lane) []string {
	return newRegistry(t).KindsForLane(lane)
}

// deferBacklog pushes every available work item (optionally except some kinds)
// two days into the future, so a measurement sees exactly the backlog it sets up.
func deferBacklog(t *testing.T, owner *sql.DB, keep ...string) int64 {
	t.Helper()
	res, err := owner.ExecContext(context.Background(), `UPDATE work_items SET available_at = now() + interval '2 days' WHERE state = 'available' AND kind <> ALL($1::text[]) AND available_at < now() + interval '1 day'`, pgArray(keep))
	if err != nil {
		t.Fatalf("defer backlog: %v", err)
	}
	n, _ := res.RowsAffected()
	return n
}

// makeDue pulls the live items of a kind to "now" so they are claimable.
func makeDue(t *testing.T, owner *sql.DB, kind string) int64 {
	t.Helper()
	res, err := owner.ExecContext(context.Background(), `UPDATE work_items SET available_at = now() - interval '1 minute' WHERE state = 'available' AND kind = $1`, kind)
	if err != nil {
		t.Fatalf("make due: %v", err)
	}
	n, _ := res.RowsAffected()
	return n
}

func pgArray(items []string) string {
	if len(items) == 0 {
		return "{}"
	}
	parts := make([]string, len(items))
	for i, s := range items {
		parts[i] = `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func liveCount(t *testing.T, owner *sql.DB, kind string) int64 {
	t.Helper()
	var n int64
	if err := owner.QueryRowContext(context.Background(), `SELECT count(*) FROM work_items WHERE kind = $1 AND ((state = 'available' AND attempt < max_attempts AND NOT recovery_blocked AND NOT cancel_requested) OR state = 'leased') AND available_at < now() + interval '1 day' AND (kind <> 'rss_refresh' OR owner_id LIKE 'r%')`, kind).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// replicaExec runs statements with triggers off (backlog arrangement only: the
// rows are shaped like the ones the product writes, same as the generator).
func replicaExec(t *testing.T, owner *sql.DB, query string, args ...any) int64 {
	t.Helper()
	ctx := context.Background()
	conn, err := owner.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `SET session_replication_role = replica`); err != nil {
		t.Fatal(err)
	}
	defer conn.ExecContext(ctx, `SET session_replication_role = origin`)
	res, err := conn.ExecContext(ctx, query, args...)
	if err != nil {
		t.Fatalf("backlog statement: %v", err)
	}
	n, _ := res.RowsAffected()
	return n
}

// ---- local fixtures -----------------------------------------------------------------------

// feedServer serves an RSS document per source: the first `items` releases of the
// source's owner (same guids as the stored releases, so a refresh dedupes against
// them), over local HTTP.
func newFeedServer(items int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/"), ".xml") // r00001-0
		owner := strings.TrimPrefix(strings.SplitN(id, "-", 2)[0], "r")
		var b strings.Builder
		b.WriteString(`<?xml version="1.0" encoding="UTF-8"?><rss version="2.0"><channel><title>soak feed</title><link>http://feed.local/</link><description>fixture</description>`)
		for k := 1; k <= items; k++ {
			guid := fmt.Sprintf("rl%s-%d", owner, k)
			title := fmt.Sprintf("[Group%s] Soak Anime %s - %02d [WebRip 1080p HEVC AAC][CHS]", strings.SplitN(id, "-", 2)[1], strings.TrimLeft(owner, "0"), k)
			fmt.Fprintf(&b, `<item><title>%s</title><guid isPermaLink="false">%s</guid><link>http://feed.local/item/%s</link><description>fixture item</description><enclosure url="http://feed.local/dl/%s.torrent" length="500000000" type="application/x-bittorrent"/><pubDate>Wed, 14 Oct 2026 08:00:00 GMT</pubDate></item>`, title, guid, guid, guid)
		}
		b.WriteString(`</channel></rss>`)
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = io.WriteString(w, b.String())
	}))
}

var generatedSourcePath = regexp.MustCompile(`^/r[0-9]{5}-[0-9]\.xml$`)

// fixtureFetcher is the RSS transport of the chain measurement: an HTTP GET to
// the local feed server and the production feed parser (no external provider).
type fixtureFetcher struct {
	base   string
	client *http.Client
	calls  atomic.Int64
}

func (f *fixtureFetcher) FetchSource(ctx context.Context, source rssapplication.SourceLocator) ([]acquisitiondomain.FeedItem, error) {
	f.calls.Add(1)
	path := source.URL[strings.LastIndex(source.URL, "/"):]
	if !generatedSourcePath.MatchString(path) {
		return nil, nil // the template owner's source is not part of the generated feed set
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.base+path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := f.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return acquisitiondomain.ParseFeed(body)
}

// webhookSink is the local notification receiver: it counts deliveries.
type webhookSink struct {
	srv   *httptest.Server
	calls atomic.Int64
}

func newWebhookSink() *webhookSink {
	s := &webhookSink{}
	s.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		s.calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	return s
}

// sinkClient is the HTTP client of the delivery chain: the handler refuses a
// non-public endpoint host, so the secret names a public-looking host and this
// client dials every connection to the local TLS sink instead.
func (s *webhookSink) client() *http.Client {
	addr := s.srv.Listener.Addr().String()
	return &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		},
		TLSClientConfig:     &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // local fixture sink
		MaxIdleConnsPerHost: 8,
	}}
}

// endpoint is the URL stored in the webhook secret (a public-looking name).
func (s *webhookSink) endpoint() string {
	_, port, _ := net.SplitHostPort(s.srv.Listener.Addr().String())
	return "https://soak-sink.example.com:" + port + "/hook"
}

// staticSecrets resolves any secret ref to the webhook secret document (the
// endpoint URL lives in the secret and must be HTTPS), so every delivery goes to the sink.
type staticSecrets struct{ value string }

func (s staticSecrets) Resolve(string) (string, bool) { return s.value, true }

// fixedRenderer renders a constant message: template rendering is CPU-only and
// is not what the delivery chain measurement is about.
type fixedRenderer struct{}

func (fixedRenderer) RenderNotification(_, _, _, _ string) (string, string, error) {
	return "Soak notification", "A new release is available.", nil
}

// errorLog records the first error a wrapped handler returned per owner, so a
// source the real handler cannot process is named in the log instead of retrying
// silently.
type errorLog struct {
	inner runtimeworker.Handler
	mu    sync.Mutex
	first map[string]string
	count int
	// calls and slowest describe every run of the wrapped handler.
	calls   int
	slowest time.Duration
}

func newErrorLog(inner runtimeworker.Handler) *errorLog {
	return &errorLog{inner: inner, first: map[string]string{}}
}

func (e *errorLog) Handle(ctx context.Context, claim workitems.Claim) (runtimeworker.Outcome, error) {
	start := time.Now()
	out, err := e.inner.Handle(ctx, claim)
	e.mu.Lock()
	if d := time.Since(start); d > e.slowest {
		e.slowest = d
	}
	e.calls++
	e.mu.Unlock()
	if err != nil {
		e.mu.Lock()
		e.count++
		if _, ok := e.first[claim.Owner.ID]; !ok && len(e.first) < 10 {
			e.first[claim.Owner.ID] = err.Error()
		}
		e.mu.Unlock()
	} else if out.Kind == runtimeworker.OutcomeRetry {
		e.mu.Lock()
		e.count++
		if _, ok := e.first[claim.Owner.ID]; !ok && len(e.first) < 10 {
			e.first[claim.Owner.ID] = "retry: " + out.ReasonCode + " " + out.Reason
		}
		e.mu.Unlock()
	}
	return out, err
}

func (e *errorLog) report(t *testing.T, what string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	t.Logf("%s: %d handler runs, slowest %v, %d failing/retrying; first per owner: %v", what, e.calls, e.slowest.Round(time.Millisecond), e.count, e.first)
}

// captureLogger keeps the distinct warning/error events of the worker runtime
// (component/operation/reason) with a count and one sample, so a transition the
// runtime could not apply is named in the log instead of retrying silently.
type captureLogger struct {
	mu     sync.Mutex
	events map[string]*capturedEvent
}

type capturedEvent struct {
	count  int
	sample observabilityport.RuntimeEvent
}

func (l *captureLogger) record(level string, ev observabilityport.RuntimeEvent) {
	key := level + " " + ev.Component + "/" + ev.Operation + " " + ev.ReasonCode + " " + ev.ErrorClass
	l.mu.Lock()
	defer l.mu.Unlock()
	if c, ok := l.events[key]; ok {
		c.count++
		return
	}
	l.events[key] = &capturedEvent{count: 1, sample: ev}
}

func (l *captureLogger) Debug(context.Context, observabilityport.RuntimeEvent) {}
func (l *captureLogger) Info(context.Context, observabilityport.RuntimeEvent)  {}
func (l *captureLogger) Warn(_ context.Context, ev observabilityport.RuntimeEvent) {
	l.record("warn", ev)
}
func (l *captureLogger) Error(_ context.Context, ev observabilityport.RuntimeEvent) {
	l.record("error", ev)
}

func (l *captureLogger) report(t *testing.T) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for key, c := range l.events {
		t.Logf("worker runtime event x%d: %s | %s", c.count, key, c.sample.Message)
	}
}
