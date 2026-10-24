package application

import (
	"errors"
	"testing"

	"bansoko/internal/apperrors"
)

// SEC-033: a connection base URL is returned on every read, so it may not carry
// credentials, a query or a fragment; those belong in the write-only secret.
func TestConnectionEndpointMustBeCredentialFree(t *testing.T) {
	for _, endpoint := range []string{"http://qb.local:8080", "https://api.themoviedb.org/3", "https://mikanani.me/"} {
		if err := validateConnectionEndpointCredentialFree(endpoint); err != nil {
			t.Fatalf("%s must be accepted: %v", endpoint, err)
		}
	}
	for _, endpoint := range []string{
		"http://admin:secret@qb.local:8080",
		"http://admin@qb.local:8080",
		"https://api.themoviedb.org/3?api_key=abc",
		"https://feeds.example.test/rss?",
		"https://feeds.example.test/rss#passkey",
		"http://%zz",
	} {
		err := validateConnectionEndpointCredentialFree(endpoint)
		var business *apperrors.Business
		if !errors.As(err, &business) || business.Code != "CONNECTION_ENDPOINT_INVALID" || business.Status != 422 {
			t.Fatalf("%s must be refused with CONNECTION_ENDPOINT_INVALID, got %v", endpoint, err)
		}
	}
}
