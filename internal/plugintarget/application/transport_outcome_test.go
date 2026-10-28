package application

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	runtimeworker "bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
)

// markRequestWritten fires the WroteRequest hook the delivery handler attached,
// the way the real transport does once it has fully written a request.
func markRequestWritten(req *http.Request) {
	if trace := httptrace.ContextClientTrace(req.Context()); trace != nil && trace.WroteRequest != nil {
		trace.WroteRequest(httptrace.WroteRequestInfo{})
	}
}

// writtenThenFailingClient stands in for a transport that wrote the request
// and then failed before any response, so the outcome is unknown.
type writtenThenFailingClient struct{ err error }

func (c writtenThenFailingClient) Do(req *http.Request) (*http.Response, error) {
	markRequestWritten(req)
	return nil, c.err
}

// forwardingDialer ignores the validated address and dials a fixed one (a test
// server, or a closed port), or fails with err.
type forwardingDialer struct {
	address string
	err     error
	dials   int
}

func (d *forwardingDialer) DialContext(ctx context.Context, network, _ string) (net.Conn, error) {
	d.dials++
	if d.err != nil {
		return nil, d.err
	}
	return (&net.Dialer{}).DialContext(ctx, network, d.address)
}

var publicResolver = staticResolver{addresses: []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}}

func newTLSTestServer(t *testing.T, http2 bool, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	server := httptest.NewUnstartedServer(handler)
	server.EnableHTTP2 = http2
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.StartTLS()
	t.Cleanup(server.Close)
	return server
}

func newDeliveryRequest(t *testing.T, target string) *http.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, target, strings.NewReader(`{"text":"hello"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	return req
}

func sendThrough(t *testing.T, client HTTPDoer, adapterKind, target string) attemptResult {
	t.Helper()
	handler := NewPluginDeliverHandler(nil, nil, nil, client, nil)
	result, _ := handler.sendRequest(newDeliveryRequest(t, target), adapterKind, newSecretRedactor(""))
	return result
}

func closedLoopbackAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return address
}

func wantResult(t *testing.T, result attemptResult, state, code string) {
	t.Helper()
	if result.state != state || result.errorCode != code {
		t.Fatalf("result state=%q code=%q err=%q, want state=%q code=%q", result.state, result.errorCode, result.err, state, code)
	}
}

// A failure before the request was written is a definite failure that may
// clear; the safe transport's own refusals are definite and never retry.
func TestSendRequestSafeTransportFailures(t *testing.T) {
	untrusted := newTLSTestServer(t, false, func(http.ResponseWriter, *http.Request) {})
	for _, tc := range []struct {
		name      string
		resolver  ipResolver
		dialer    *forwardingDialer
		proxy     func(*http.Request) (*url.URL, error)
		target    string
		wantState string
		wantCode  string
		wantDials int
	}{
		{
			name:      "dns failure",
			resolver:  staticResolver{err: &net.DNSError{Err: "no such host", Name: "hook.example", IsNotFound: true}},
			dialer:    &forwardingDialer{},
			target:    "https://hook.example/send",
			wantState: "waiting_retry", wantCode: "dns_failed",
		},
		{
			name:      "dns answer without addresses",
			resolver:  staticResolver{},
			dialer:    &forwardingDialer{},
			target:    "https://hook.example/send",
			wantState: "waiting_retry", wantCode: "dns_failed",
		},
		{
			name:      "connection refused",
			resolver:  publicResolver,
			dialer:    &forwardingDialer{address: closedLoopbackAddress(t)},
			target:    "https://hook.example/send",
			wantState: "waiting_retry", wantCode: "connection_refused", wantDials: 1,
		},
		{
			name:      "connect timeout",
			resolver:  publicResolver,
			dialer:    &forwardingDialer{err: &net.OpError{Op: "dial", Net: "tcp", Err: os.ErrDeadlineExceeded}},
			target:    "https://hook.example/send",
			wantState: "waiting_retry", wantCode: "connect_timeout", wantDials: 1,
		},
		{
			name:      "other dial failure",
			resolver:  publicResolver,
			dialer:    &forwardingDialer{err: errors.New("network is unreachable")},
			target:    "https://hook.example/send",
			wantState: "waiting_retry", wantCode: "connection_failed", wantDials: 1,
		},
		{
			name:      "tls handshake failure",
			resolver:  publicResolver,
			dialer:    &forwardingDialer{address: untrusted.Listener.Addr().String()},
			target:    "https://example.com/send",
			wantState: "waiting_retry", wantCode: "tls_failed", wantDials: 1,
		},
		{
			name:      "resolved non-public address",
			resolver:  staticResolver{addresses: []net.IPAddr{{IP: net.ParseIP("10.0.0.5")}}},
			dialer:    &forwardingDialer{},
			target:    "https://internal.example/send",
			wantState: "failed", wantCode: "address_rejected",
		},
		{
			name:      "literal non-public address",
			resolver:  publicResolver,
			dialer:    &forwardingDialer{},
			target:    "https://127.0.0.1/send",
			wantState: "failed", wantCode: "address_rejected",
		},
		{
			name:     "invalid egress proxy",
			resolver: publicResolver,
			dialer:   &forwardingDialer{},
			proxy: func(*http.Request) (*url.URL, error) {
				return nil, errors.New(`parse "http://proxy-user:proxy-pass@proxy.example:bad": invalid port`)
			},
			target:    "https://hook.example/send",
			wantState: "failed", wantCode: "proxy_invalid",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			proxy := tc.proxy
			if proxy == nil {
				proxy = noProxy
			}
			client := newSafeHTTPClient(safeHTTPHooks{resolver: tc.resolver, dialer: tc.dialer, proxy: proxy})
			result := sendThrough(t, client, "webhook", tc.target)
			wantResult(t, result, tc.wantState, tc.wantCode)
			if tc.dialer.dials != tc.wantDials {
				t.Fatalf("dials=%d, want %d", tc.dialer.dials, tc.wantDials)
			}
			if result.responseStatus != nil {
				t.Fatalf("a failed transport carries no response status: %+v", result)
			}
			if strings.Contains(result.err, "proxy-pass") || strings.Contains(result.err, "https://") {
				t.Fatalf("unsafe error text: %q", result.err)
			}
		})
	}
}

// A rejected redirect is a refusal even though the endpoint answered and the
// request was fully written: the endpoint did not accept, and a retry meets
// the same redirect.
func TestSendRequestRejectedRedirectFailsAtOnce(t *testing.T) {
	for name, location := range map[string]string{
		"cross-origin":     "https://other.example/send",
		"redirect limit":   "/send",
		"scheme downgrade": "http://example.com/send",
	} {
		t.Run(name, func(t *testing.T) {
			hits := 0
			server := newTLSTestServer(t, false, func(w http.ResponseWriter, r *http.Request) {
				hits++
				http.Redirect(w, r, location, http.StatusFound)
			})
			// The safe client's redirect policy, over a transport that trusts the
			// test server and dials it directly.
			client := newSafeHTTPClient(safeHTTPHooks{resolver: publicResolver, dialer: &forwardingDialer{}, proxy: noProxy})
			client.Transport = server.Client().Transport
			result := sendThrough(t, client, "webhook", server.URL+"/send")
			wantResult(t, result, "failed", "redirect_rejected")
			if hits == 0 {
				t.Fatal("the endpoint never answered the redirect")
			}
		})
	}
}

// After the request was fully written, an error without a response leaves the
// outcome unknown: the receiver may have acted on it.
func TestSendRequestFailureAfterWriteIsUnknown(t *testing.T) {
	for _, protocol := range []struct {
		name  string
		http2 bool
	}{{"http1", false}, {"http2", true}} {
		t.Run(protocol.name+" response timeout", func(t *testing.T) {
			release := make(chan struct{})
			server := newTLSTestServer(t, protocol.http2, func(w http.ResponseWriter, r *http.Request) {
				select {
				case <-release:
				case <-r.Context().Done():
				}
			})
			t.Cleanup(func() { close(release) })
			client := &http.Client{Transport: server.Client().Transport, Timeout: 200 * time.Millisecond}
			result := sendThrough(t, client, "webhook", server.URL)
			wantResult(t, result, "unknown", "response_timeout")
			if result.responseStatus != nil {
				t.Fatalf("timeout carries no response status: %+v", result)
			}
		})
		t.Run(protocol.name+" connection lost", func(t *testing.T) {
			server := newTLSTestServer(t, protocol.http2, func(http.ResponseWriter, *http.Request) {
				panic(http.ErrAbortHandler)
			})
			result := sendThrough(t, server.Client(), "webhook", server.URL)
			wantResult(t, result, "unknown", "connection_lost")
		})
	}
}

// A received status code decides the outcome even when the body is unusable;
// only adapters that confirm delivery in the body need it for a 2xx.
func TestSendRequestOversizedBodyByAdapter(t *testing.T) {
	for _, protocol := range []struct {
		name  string
		http2 bool
	}{{"http1", false}, {"http2", true}} {
		oversized := strings.Repeat("x", maxResponseBody+1)
		for _, tc := range []struct {
			adapter   string
			status    int
			wantState string
			wantCode  string
		}{
			{"webhook", http.StatusOK, "delivered", ""},
			{"telegram", http.StatusOK, "unknown", "response_too_large"},
			{"feishu", http.StatusOK, "unknown", "response_too_large"},
			{"webhook", http.StatusServiceUnavailable, "waiting_retry", "http_503"},
			{"telegram", http.StatusBadRequest, "failed", "http_400"},
		} {
			t.Run(fmt.Sprintf("%s %s %d", protocol.name, tc.adapter, tc.status), func(t *testing.T) {
				server := newTLSTestServer(t, protocol.http2, func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(tc.status)
					_, _ = io.WriteString(w, oversized)
				})
				result := sendThrough(t, server.Client(), tc.adapter, server.URL)
				wantResult(t, result, tc.wantState, tc.wantCode)
				if result.responseStatus == nil || *result.responseStatus != tc.status || result.responseBytes != maxResponseBody+1 {
					t.Fatalf("response evidence lost: %+v", result)
				}
				if len(result.providerAck) != 0 || strings.Contains(result.err, "xxx") {
					t.Fatalf("raw oversized body escaped: %+v", result)
				}
			})
		}
	}
}

func TestClassifyTransportError(t *testing.T) {
	cancelled := fmt.Errorf("post: %w", context.Canceled)
	for _, tc := range []struct {
		name      string
		err       error
		written   bool
		wantState string
		wantCode  string
	}{
		{"malformed dial address", errMalformedDialAddress, false, "failed", "address_rejected"},
		{"refusal outranks the written request", fmt.Errorf("round trip: %w", errNonPublicAddress), true, "failed", "address_rejected"},
		{"redirect limit after the request was written", errRedirectLimit, true, "failed", "redirect_rejected"},
		{"dns sentinel without a resolver error", errDNSResolution, false, "waiting_retry", "dns_failed"},
		{"dns wrapped resolver error", fmt.Errorf("%w: %w", errDNSResolution, &net.DNSError{Err: "no such host"}), false, "waiting_retry", "dns_failed"},
		{"cancelled before the write", cancelled, false, "waiting_retry", "connection_failed"},
		{"reset after the write", &net.OpError{Op: "read", Err: os.NewSyscallError("read", errors.New("reset"))}, true, "unknown", "connection_lost"},
		{"deadline after the write", fmt.Errorf("post: %w", context.DeadlineExceeded), true, "unknown", "response_timeout"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state, code := classifyTransportError(tc.err, tc.written)
			if state != tc.wantState || code != tc.wantCode {
				t.Fatalf("state=%q code=%q, want state=%q code=%q", state, code, tc.wantState, tc.wantCode)
			}
		})
	}
}

// settleAttempt walks the same path for a pre-write failure as for a retryable
// status: retry on the bounded backoff, then failed once the budget is spent.
// An unknown outcome stays unknown, and a refusal is failed at once.
func TestSettleAttemptTransportOutcomes(t *testing.T) {
	handler := NewPluginDeliverHandler(nil, nil, nil, nil, nil)
	settle := func(kind string, attemptNumber int, result attemptResult) runtimeworker.Outcome {
		claim := workitems.Claim{Attempt: 1, MaxAttempts: deliveryWorkItemMaxAttempts}
		row := deliveryRow{deliveryKind: kind}
		return handler.settleAttempt(claim, row, durableAttempt{number: attemptNumber}, result, newSecretRedactor(""))
	}
	for _, code := range []string{"dns_failed", "connection_refused", "connect_timeout", "tls_failed", "connection_failed"} {
		t.Run(code, func(t *testing.T) {
			result := attemptResult{state: "waiting_retry", errorCode: code}
			out := settle("business", 1, result)
			if out.Kind != runtimeworker.OutcomeRetry || out.RetryAfter != deliveryRetryDelays[0] || out.ReasonCode != code {
				t.Fatalf("first attempt outcome=%+v, want retry after %s", out, deliveryRetryDelays[0])
			}
			out = settle("business", MaxDeliveryAttempts, result)
			if out.Kind != runtimeworker.OutcomeFinish || out.OwnerResult.State != "failed" || out.OwnerResult.ReasonCode != code {
				t.Fatalf("last attempt outcome=%+v, want failed", out)
			}
		})
	}
	for _, code := range []string{"address_rejected", "proxy_invalid", "redirect_rejected"} {
		t.Run(code, func(t *testing.T) {
			out := settle("business", 1, attemptResult{state: "failed", errorCode: code})
			if out.Kind != runtimeworker.OutcomeFinish || out.OwnerResult.State != "failed" || out.OwnerResult.ReasonCode != code {
				t.Fatalf("refusal outcome=%+v, want failed at once", out)
			}
		})
	}
	t.Run("unknown stays unknown", func(t *testing.T) {
		result := attemptResult{state: "unknown", errorCode: "response_timeout"}
		if out := settle("business", 1, result); out.Kind != runtimeworker.OutcomeRetry || out.RetryAfter != deliveryRetryDelays[0] {
			t.Fatalf("first attempt outcome=%+v, want retry", out)
		}
		out := settle("business", MaxDeliveryAttempts, result)
		if out.Kind != runtimeworker.OutcomeFinish || out.OwnerResult.State != "unknown" || out.OwnerResult.ReasonCode != "response_timeout" {
			t.Fatalf("last attempt outcome=%+v, want unknown", out)
		}
	})
	t.Run("test delivery", func(t *testing.T) {
		if out := settle("test", 1, attemptResult{state: "waiting_retry", errorCode: "dns_failed"}); out.Kind != runtimeworker.OutcomeRetry || out.RetryAfter != testDeliveryRetryDelay {
			t.Fatalf("pre-write failure outcome=%+v, want retry after %s", out, testDeliveryRetryDelay)
		}
		if out := settle("test", 1, attemptResult{state: "unknown", errorCode: "response_timeout"}); out.Kind != runtimeworker.OutcomeFinish || out.OwnerResult.State != "unknown" {
			t.Fatalf("unknown outcome=%+v, want settled at once", out)
		}
	})
}

// The handler's error text never reaches past the redactor, whichever class
// the failure takes.
func TestSendRequestRedactsTransportErrorText(t *testing.T) {
	const secret = "bot-token-0123456789"
	for name, client := range map[string]HTTPDoer{
		"before the write": &fakeHTTPClient{err: fmt.Errorf("dial %s: %w", secret, errors.New("refused"))},
		"after the write":  writtenThenFailingClient{err: fmt.Errorf("read %s: %w", secret, context.DeadlineExceeded)},
	} {
		t.Run(name, func(t *testing.T) {
			handler := NewPluginDeliverHandler(nil, nil, nil, client, nil)
			result, _ := handler.sendRequest(newDeliveryRequest(t, "https://hook.example/send"), "webhook", newSecretRedactor(secret))
			if strings.Contains(result.err, secret) {
				t.Fatalf("secret leaked into the attempt error: %q", result.err)
			}
		})
	}
}
