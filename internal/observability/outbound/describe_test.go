package outbound

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"syscall"
	"testing"
)

func request(t *testing.T, raw string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	return req
}

func TestDescribeHostIsHostnameOnly(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want string
	}{
		{"https://api.example.test/v1/torrents?token=abc", "api.example.test"},
		{"http://user:pass@feed.example.test:8443/rss?key=SECRET#frag", "feed.example.test"},
		{"http://[2001:db8::1]:9000/x", "2001:db8::1"},
		{"http://192.0.2.7:8080/", "192.0.2.7"},
	} {
		got := Describe(errors.New("boom"), request(t, tc.raw), nil, ViaDirect)
		if got.Host != tc.want {
			t.Fatalf("Describe(%q).Host = %q, want %q", tc.raw, got.Host, tc.want)
		}
	}
	if got := Describe(errors.New("boom"), nil, nil, ViaDirect); got.Host != "" {
		t.Fatalf("nil request host = %q, want empty", got.Host)
	}
	long := "https://" + strings.Repeat("a", 300) + ".test/"
	if got := Describe(errors.New("boom"), request(t, long), nil, ViaDirect); got.Host != "" {
		t.Fatalf("overlong host kept: %d bytes", len(got.Host))
	}
}

func TestDescribeClassAndStatus(t *testing.T) {
	req := request(t, "https://api.example.test/")
	refused := &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}
	got := Describe(refused, req, nil, ViaProxy)
	if got.Class != ClassConnectionRefused || got.Status != 0 || got.Via != ViaProxy {
		t.Fatalf("transport failure = %+v", got)
	}
	got = Describe(nil, req, &http.Response{StatusCode: http.StatusBadGateway}, ViaDirect)
	if got.Class != ClassHTTPStatus || got.Status != http.StatusBadGateway {
		t.Fatalf("status failure = %+v", got)
	}
	// A transport error wins over a response the caller also passed.
	got = Describe(refused, req, &http.Response{StatusCode: http.StatusBadGateway}, ViaDirect)
	if got.Class != ClassConnectionRefused || got.Status != 0 {
		t.Fatalf("error must win over response: %+v", got)
	}
	if got = Describe(nil, req, nil, ViaDirect); got.Class != ClassUnknown {
		t.Fatalf("no error and no response = %+v", got)
	}
}

// Acceptance 12: a secret placed in the error text, the URL userinfo, the query
// and the response body-bearing fields never reaches a Failure.
func TestDescribeNeverLeaksErrorText(t *testing.T) {
	const secret = "SUPER-SECRET-TOKEN-9f3"
	req := request(t, "https://user:"+secret+"@api.example.test:8443/hook/"+secret+"?key="+secret)
	cause := &url.Error{
		Op:  "Post",
		URL: req.URL.String(),
		Err: fmt.Errorf("dial %s: %w", secret, &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}),
	}
	for _, err := range []error{cause, errors.New(secret), fmt.Errorf("%s: %w", secret, cause)} {
		failure := Describe(err, req, &http.Response{StatusCode: 500, Status: secret}, ViaDirect)
		rendered := fmt.Sprintf("%v %+v %#v", failure, failure, failure)
		if strings.Contains(rendered, secret) {
			t.Fatalf("failure leaked the secret: %s", rendered)
		}
		carried, ok := FailureOf(WithFailure(err, failure))
		if !ok || carried != failure {
			t.Fatalf("carrier lost the failure: %+v ok=%v", carried, ok)
		}
	}
}

type proxyRoundTripper struct{ via Via }

func (p proxyRoundTripper) RoundTrip(*http.Request) (*http.Response, error) { return nil, nil }
func (p proxyRoundTripper) OutboundVia(*http.Request) Via                   { return p.via }

func TestViaOf(t *testing.T) {
	req := request(t, "https://api.example.test/")
	proxyURL, _ := url.Parse("http://proxy.example.test:3128")
	for _, tc := range []struct {
		name   string
		client *http.Client
		want   Via
	}{
		{"no proxy func", &http.Client{Transport: &http.Transport{}}, ViaDirect},
		{"proxy func returns nil", &http.Client{Transport: &http.Transport{Proxy: func(*http.Request) (*url.URL, error) { return nil, nil }}}, ViaDirect},
		{"proxy func errors", &http.Client{Transport: &http.Transport{Proxy: func(*http.Request) (*url.URL, error) { return nil, errors.New("bad") }}}, ViaDirect},
		{"proxy func returns a proxy", &http.Client{Transport: &http.Transport{Proxy: func(*http.Request) (*url.URL, error) { return proxyURL, nil }}}, ViaProxy},
		{"reporter says proxy", &http.Client{Transport: proxyRoundTripper{via: ViaProxy}}, ViaProxy},
		{"reporter says direct", &http.Client{Transport: proxyRoundTripper{via: ViaDirect}}, ViaDirect},
		{"unknown round tripper", &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) { return nil, nil })}, ViaDirect},
	} {
		if got := ViaOf(tc.client, req); got != tc.want {
			t.Fatalf("%s: ViaOf = %q, want %q", tc.name, got, tc.want)
		}
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestFailureCarrierKeepsTheChain(t *testing.T) {
	if WithFailure(nil, Failure{}) != nil {
		t.Fatal("nil error must stay nil")
	}
	base := &net.DNSError{Err: "no such host", Name: "x.example", IsNotFound: true}
	wrapped := fmt.Errorf("lookup: %w", WithFailure(base, Failure{Host: "x.example", Class: ClassDNS, Via: ViaDirect}))
	if Classify(wrapped) != ClassDNS {
		t.Fatalf("Classify through the carrier = %q", Classify(wrapped))
	}
	failure, ok := FailureOf(wrapped)
	if !ok || failure.Host != "x.example" || failure.Class != ClassDNS {
		t.Fatalf("FailureOf = %+v ok=%v", failure, ok)
	}
	if !strings.Contains(wrapped.Error(), "no such host") {
		t.Fatalf("carrier changed the error text: %q", wrapped.Error())
	}
	if _, ok := FailureOf(errors.New("plain")); ok {
		t.Fatal("plain error carries no failure")
	}
}
