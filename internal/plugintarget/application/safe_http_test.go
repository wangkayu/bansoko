package application

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func noProxy(*http.Request) (*url.URL, error) { return nil, nil }

type staticResolver struct {
	addresses []net.IPAddr
	err       error
}

func (r staticResolver) LookupIPAddr(context.Context, string) ([]net.IPAddr, error) {
	return r.addresses, r.err
}

type recordingDialer struct {
	addresses []string
	err       error
}

func (d *recordingDialer) DialContext(_ context.Context, _, address string) (net.Conn, error) {
	d.addresses = append(d.addresses, address)
	return nil, d.err
}

func TestSafeHTTPDialRejectsAnyPrivateResolutionWithoutDial(t *testing.T) {
	dialer := &recordingDialer{}
	client := newSafeHTTPClient(safeHTTPHooks{resolver: staticResolver{addresses: []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}, {IP: net.ParseIP("127.0.0.1")}}}, dialer: dialer, proxy: noProxy})
	req, _ := http.NewRequest(http.MethodGet, "https://example.com", nil)
	_, err := client.Do(req)
	if err == nil || len(dialer.addresses) != 0 {
		t.Fatalf("private mixed DNS must fail closed without dial: err=%v dials=%v", err, dialer.addresses)
	}
}

func TestUnsafeIPRejectsSpecialPurposeRanges(t *testing.T) {
	for _, raw := range []string{
		"0.0.0.1", "10.0.0.1", "100.64.0.1", "127.0.0.1", "169.254.1.1",
		"192.0.0.1", "192.0.2.1", "198.18.0.1", "198.51.100.1", "203.0.113.1",
		"224.0.0.1", "240.0.0.1", "255.255.255.255",
		"::", "::1", "64:ff9b::1", "100::1", "2001:db8::1", "2002::1",
		"3fff::1", "5f00::1", "fc00::1", "fe80::1", "ff00::1",
	} {
		t.Run(raw, func(t *testing.T) {
			if !unsafeIP(net.ParseIP(raw)) {
				t.Fatalf("special-purpose address was accepted: %s", raw)
			}
		})
	}
}

func TestUnsafeIPAcceptsPublicAddresses(t *testing.T) {
	for _, raw := range []string{"8.8.8.8", "1.1.1.1", "2606:4700:4700::1111"} {
		t.Run(raw, func(t *testing.T) {
			if unsafeIP(net.ParseIP(raw)) {
				t.Fatalf("public address was rejected: %s", raw)
			}
		})
	}
}

func TestSafeHTTPDialUsesValidatedIPNotHostname(t *testing.T) {
	dialer := &recordingDialer{err: errors.New("stop")}
	client := newSafeHTTPClient(safeHTTPHooks{resolver: staticResolver{addresses: []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}}, dialer: dialer, proxy: noProxy})
	req, _ := http.NewRequest(http.MethodGet, "https://example.com:443", nil)
	_, _ = client.Do(req)
	if len(dialer.addresses) != 1 || dialer.addresses[0] != "93.184.216.34:443" {
		t.Fatalf("dial addresses=%v", dialer.addresses)
	}
}

func fixedProxy(raw string) func(*http.Request) (*url.URL, error) {
	parsed, _ := url.Parse(raw)
	return func(*http.Request) (*url.URL, error) { return parsed, nil }
}

func TestSafeHTTPEnvProxyDialsProxyAfterPublicTargetCheck(t *testing.T) {
	// A container egress proxy may live on a private address (sidecar, LAN
	// gateway); it is a trusted boundary and is dialed as configured.
	dialer := &recordingDialer{err: errors.New("stop")}
	client := newSafeHTTPClient(safeHTTPHooks{
		resolver: staticResolver{addresses: []net.IPAddr{{IP: net.ParseIP("149.154.167.220")}}},
		dialer:   dialer,
		proxy:    fixedProxy("http://192.168.1.2:7890"),
	})
	req, _ := http.NewRequest(http.MethodGet, "https://api.telegram.org", nil)
	_, _ = client.Do(req)
	if len(dialer.addresses) != 1 || dialer.addresses[0] != "192.168.1.2:7890" {
		t.Fatalf("proxied request must dial the proxy: dials=%v", dialer.addresses)
	}
}

func TestSafeHTTPEnvProxyStillRejectsPrivateTargets(t *testing.T) {
	for name, tc := range map[string]struct {
		target    string
		addresses []net.IPAddr
	}{
		"resolved private": {target: "https://internal.example", addresses: []net.IPAddr{{IP: net.ParseIP("10.0.0.5")}}},
		"literal private":  {target: "https://127.0.0.1"},
	} {
		t.Run(name, func(t *testing.T) {
			dialer := &recordingDialer{}
			client := newSafeHTTPClient(safeHTTPHooks{
				resolver: staticResolver{addresses: tc.addresses},
				dialer:   dialer,
				proxy:    fixedProxy("http://192.168.1.2:7890"),
			})
			req, _ := http.NewRequest(http.MethodGet, tc.target, nil)
			_, err := client.Do(req)
			if err == nil || len(dialer.addresses) != 0 {
				t.Fatalf("private target behind proxy must fail closed without dial: err=%v dials=%v", err, dialer.addresses)
			}
		})
	}
}

func TestSafeRedirectPolicy(t *testing.T) {
	client := newSafeHTTPClient(safeHTTPHooks{proxy: noProxy})
	origin, _ := http.NewRequest(http.MethodGet, "https://example.com/start", nil)
	for name, target := range map[string]string{"downgrade": "http://example.com/next", "cross-origin": "https://other.example/next", "private": "https://127.0.0.1/next"} {
		t.Run(name, func(t *testing.T) {
			next, _ := http.NewRequest(http.MethodGet, target, nil)
			if err := client.CheckRedirect(next, []*http.Request{origin}); err == nil {
				t.Fatal("redirect must be rejected")
			}
		})
	}
}

type errorAfterReader struct {
	data []byte
}

func (r *errorAfterReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, errors.New("reader failed with raw secret")
	}
	n := copy(p, r.data)
	r.data = r.data[n:]
	return n, nil
}

func (r *errorAfterReader) Close() error { return nil }

func TestResponseBodyOversizeIsUnknownOnlyWhereTheBodyAcknowledges(t *testing.T) {
	for _, tc := range []struct {
		adapterKind string
		wantState   string
		wantCode    string
	}{
		{"webhook", "delivered", ""},
		{"telegram", "unknown", "response_too_large"},
		{"feishu", "unknown", "response_too_large"},
	} {
		t.Run(tc.adapterKind, func(t *testing.T) {
			resp := &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(strings.Repeat("x", maxResponseBody+1)))}
			result := readResponseResult(resp, tc.adapterKind, newSecretRedactor(""))
			if result.state != tc.wantState || result.errorCode != tc.wantCode || result.responseStatus == nil || *result.responseStatus != http.StatusOK || result.responseBytes != maxResponseBody+1 {
				t.Fatalf("unexpected result: %+v", result)
			}
			if len(result.providerAck) != 0 || strings.Contains(result.err, "xxx") {
				t.Fatalf("raw oversized body escaped: %+v", result)
			}
		})
	}
}

func TestResponseReadErrorPreservesSafeResponseEvidence(t *testing.T) {
	for _, tc := range []struct {
		adapterKind string
		wantState   string
		wantCode    string
	}{
		{"webhook", "delivered", ""},
		{"telegram", "unknown", "response_read_failed"},
		{"feishu", "unknown", "response_read_failed"},
	} {
		t.Run(tc.adapterKind, func(t *testing.T) {
			resp := &http.Response{StatusCode: http.StatusAccepted, Body: &errorAfterReader{data: []byte("partial-secret-body")}}
			result := readResponseResult(resp, tc.adapterKind, newSecretRedactor(""))
			if result.state != tc.wantState || result.errorCode != tc.wantCode || result.responseStatus == nil || *result.responseStatus != http.StatusAccepted {
				t.Fatalf("unexpected result: %+v", result)
			}
			if result.responseBytes != int64(len("partial-secret-body")) || len(result.providerAck) != 0 || strings.Contains(result.err, "secret") {
				t.Fatalf("unsafe or incomplete evidence: %+v", result)
			}
		})
	}
}

// The status code alone decides a non-2xx outcome, whatever the body does.
func TestResponseStatusDecidesWhenBodyIsUnusable(t *testing.T) {
	for _, tc := range []struct {
		status    int
		wantState string
		wantCode  string
	}{
		{http.StatusTooManyRequests, "waiting_retry", "http_429"},
		{http.StatusBadGateway, "waiting_retry", "http_502"},
		{http.StatusForbidden, "failed", "http_403"},
	} {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			resp := &http.Response{StatusCode: tc.status, Body: &errorAfterReader{data: []byte("partial")}}
			result := readResponseResult(resp, "telegram", newSecretRedactor(""))
			if result.state != tc.wantState || result.errorCode != tc.wantCode {
				t.Fatalf("unexpected result: %+v", result)
			}
		})
	}
}

func TestEndpointAndAuthValidation(t *testing.T) {
	for _, raw := range []string{"http://example.com", "https://user@example.com", "https://example.com/#token", "https://127.0.0.1"} {
		if _, err := parseHTTPSURL(raw); err == nil {
			t.Fatalf("unsafe endpoint accepted: %s", raw)
		}
	}
	for _, name := range []string{"Host", "Content-Length", "Cookie", "Connection", "Proxy-Token", "Bad Header"} {
		if safeAuthHeaderName(name) {
			t.Fatalf("unsafe header accepted: %s", name)
		}
	}
	if !safeAuthHeaderName("X-Hook-Token") {
		t.Fatal("safe custom header rejected")
	}
}
