package transport

import (
	"net/http"
	"testing"
)

func TestDirectTransportUsesClonedConfiguredDefaults(t *testing.T) {
	first := DirectTransport()
	second := DirectTransport()
	if first == nil || first != second {
		t.Fatalf("DirectTransport() must return a stable transport instance")
	}
	if first == http.DefaultTransport {
		t.Fatalf("DirectTransport() must not return http.DefaultTransport directly")
	}
	if first.Proxy == nil || first.DialContext == nil {
		t.Fatalf("DirectTransport() must configure proxy and dialer")
	}
}
