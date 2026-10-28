package qbittorrent

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"bansoko/internal/observability/outbound"
)

// ErrAuthFailed means qBittorrent answered but refused the credentials: a wrong
// password (login body "Fails."), a banned client address (login 403), or an
// API call answered 403 after a login that said "Ok.". It is retryable: waiting
// work resumes once the owner fixes the password in qBittorrent or settings.
var ErrAuthFailed = errors.New("qBittorrent refused the credentials")

const loginPathSuffix = "/api/v2/auth/login"

// doOutbound performs one request and, on a transport error, attaches the
// structured outbound facts (host, failure class, route) to the returned error
// so a diagnostic can be recorded without reading error text.
func doOutbound(client *http.Client, request *http.Request) (*http.Response, error) {
	response, err := client.Do(request)
	if err != nil {
		return nil, outbound.WithFailure(err, outbound.Describe(err, request, nil, outbound.ViaOf(client, request)))
	}
	return response, nil
}

// doRequest is doOutbound for qB API calls: a 403 after a successful login is
// the credentials being refused, so it surfaces as ErrAuthFailed.
func doRequest(client *http.Client, request *http.Request) (*http.Response, error) {
	response, err := doOutbound(client, request)
	if err != nil {
		return nil, err
	}
	if response.StatusCode == http.StatusForbidden && !strings.HasSuffix(request.URL.Path, loginPathSuffix) {
		response.Body.Close()
		return nil, fmt.Errorf("qB request refused with HTTP 403: %w", ErrAuthFailed)
	}
	return response, nil
}
