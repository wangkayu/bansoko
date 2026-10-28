package outbound

import (
	"errors"
	"net/http"
	"net/url"
)

// ClassHTTPStatus is a call that reached the peer and got a failing HTTP status
// back. Failure.Status carries the code; the response body is never read here.
const ClassHTTPStatus Class = "http_status"

// maxHostLength bounds Failure.Host to the DNS name limit.
const maxHostLength = 253

// Via is the route one outbound call took: straight out, or through a proxy.
type Via string

const (
	ViaDirect Via = "direct"
	ViaProxy  Via = "proxy"
)

// Failure is the structured, secret-free description of one failed outbound
// call: which host, what class of failure, the HTTP status when the peer
// answered, and which route the call took. Every field is derived from typed
// values (the request URL's hostname, error types and sentinels, the status
// code, the transport's proxy decision), never from error text, so nothing in
// it can carry a URL, a credential or upstream response text.
type Failure struct {
	Host   string
	Class  Class
	Status int
	Via    Via
}

// Describe builds the Failure of one call. A non-nil err is classified by
// Classify; otherwise a non-nil resp is an http_status failure carrying its
// status code (the caller decides which statuses are failures). Host is the
// request URL's hostname only: no port, path, query or userinfo. via is what
// ViaOf reported for the same request.
func Describe(err error, req *http.Request, resp *http.Response, via Via) Failure {
	failure := Failure{Host: hostOf(req), Via: via, Class: ClassUnknown}
	switch {
	case err != nil:
		failure.Class = Classify(err)
	case resp != nil:
		failure.Class = ClassHTTPStatus
		failure.Status = resp.StatusCode
	}
	return failure
}

func hostOf(req *http.Request) string {
	if req == nil || req.URL == nil {
		return ""
	}
	host := req.URL.Hostname()
	if len(host) > maxHostLength {
		return ""
	}
	return host
}

// ViaReporter is implemented by a custom RoundTripper that makes its own proxy
// decision, so ViaOf can ask it instead of looking for a *http.Transport.
type ViaReporter interface {
	OutboundVia(req *http.Request) Via
}

// ViaOf reports the route client would take for req: ViaProxy when its
// transport's proxy function returns a proxy for the request, ViaDirect
// otherwise (including when it cannot tell).
func ViaOf(client *http.Client, req *http.Request) Via {
	var transport http.RoundTripper = http.DefaultTransport
	if client != nil && client.Transport != nil {
		transport = client.Transport
	}
	switch typed := transport.(type) {
	case ViaReporter:
		return typed.OutboundVia(req)
	case *http.Transport:
		return ViaFromProxy(typed.Proxy, req)
	}
	return ViaDirect
}

// ViaFromProxy is ViaProxy when proxy picks a proxy for req.
func ViaFromProxy(proxy func(*http.Request) (*url.URL, error), req *http.Request) Via {
	if proxy == nil || req == nil {
		return ViaDirect
	}
	if target, err := proxy(req); err == nil && target != nil {
		return ViaProxy
	}
	return ViaDirect
}

// FailureError carries a Failure through an error chain so the code that
// records a diagnostic can read the structured facts without ever reading the
// error text. Error and Unwrap delegate to the wrapped error, so callers that
// already inspect the chain keep working.
type FailureError struct {
	Failure Failure
	Err     error
}

func (e *FailureError) Error() string { return e.Err.Error() }
func (e *FailureError) Unwrap() error { return e.Err }

// WithFailure wraps err with failure. A nil err stays nil.
func WithFailure(err error, failure Failure) error {
	if err == nil {
		return nil
	}
	return &FailureError{Failure: failure, Err: err}
}

// FailureOf returns the Failure attached anywhere in err's chain.
func FailureOf(err error) (Failure, bool) {
	var carrier *FailureError
	if errors.As(err, &carrier) {
		return carrier.Failure, true
	}
	return Failure{}, false
}
