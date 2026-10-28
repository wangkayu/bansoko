package application

import (
	"errors"
	"net/http"
	"net/http/httptrace"
	"sync/atomic"

	"bansoko/internal/observability/outbound"
)

// sendRequest performs the delivery request once and settles what the
// transport produced into an attemptResult. Whether the request was fully
// written is observed with httptrace, never inferred from error text.
func (h *PluginDeliverHandler) sendRequest(req *http.Request, adapterKind string, redactor secretRedactor) (attemptResult, error) {
	var written atomic.Bool
	trace := &httptrace.ClientTrace{
		// The hook runs on the transport's write goroutine. A write error means
		// the request was cut short, so the receiver cannot have processed it.
		WroteRequest: func(info httptrace.WroteRequestInfo) {
			if info.Err == nil {
				written.Store(true)
			}
		},
	}
	resp, err := h.client.Do(req.WithContext(httptrace.WithClientTrace(req.Context(), trace)))
	if err != nil {
		return transportFailureResult(err, written.Load(), redactor), err
	}
	return readResponseResult(resp, adapterKind, redactor), nil
}

// transportFailureResult classifies a failed round trip. The safe transport's
// own refusals come first: a retry cannot change them. Otherwise the request
// being fully written decides: before that, the receiver cannot have acted on
// it, so the failure is definite and may clear (waiting_retry); after it, the
// outcome is unknown.
func transportFailureResult(err error, requestWritten bool, redactor secretRedactor) attemptResult {
	state, code := classifyTransportError(err, requestWritten)
	return attemptResult{state: state, errorCode: code, err: redactor.text(safeTransportError(err).Error(), 4096)}
}

func classifyTransportError(err error, requestWritten bool) (state, code string) {
	switch {
	case errors.Is(err, errNonPublicAddress), errors.Is(err, errMalformedDialAddress):
		return "failed", "address_rejected"
	case errors.Is(err, errInvalidProxy):
		return "failed", "proxy_invalid"
	case errors.Is(err, errCrossOriginRedirect), errors.Is(err, errRedirectLimit), errors.Is(err, errRedirectTarget):
		return "failed", "redirect_rejected"
	}
	class := outbound.Classify(err)
	if requestWritten {
		if class == outbound.ClassTimeout {
			return "unknown", "response_timeout"
		}
		return "unknown", "connection_lost"
	}
	switch {
	case errors.Is(err, errDNSResolution), class == outbound.ClassDNS:
		return "waiting_retry", "dns_failed"
	case class == outbound.ClassConnectionRefused:
		return "waiting_retry", "connection_refused"
	case class == outbound.ClassTimeout:
		return "waiting_retry", "connect_timeout"
	case class == outbound.ClassTLS:
		return "waiting_retry", "tls_failed"
	default:
		return "waiting_retry", "connection_failed"
	}
}
