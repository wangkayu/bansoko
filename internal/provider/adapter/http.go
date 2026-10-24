package adapter

import (
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	hardeningtransport "bansoko/internal/hardening/transport"
)

const maxGroupResponseSize = 5 << 20

func providerEndpoint(raw, path string) (*url.URL, error) {
	endpoint, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.Hostname() == "" || endpoint.User != nil || endpoint.Fragment != "" {
		return nil, errors.New("provider endpoint must be an absolute HTTP URL without credentials or fragment")
	}
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + path
	endpoint.RawQuery = ""
	return endpoint, nil
}

func httpClient(client *http.Client) *http.Client {
	if client != nil {
		return client
	}
	return &http.Client{Timeout: 20 * time.Second, Transport: hardeningtransport.DirectTransport()}
}

// noRedirectClient returns a copy of client that reports the first response
// instead of following redirects. The caller's client is never mutated: an
// injected client stays usable for its other callers. Mikan answers a retired
// subgroup id with a 302 to the site root, and following it would turn "no
// such group" into "200 + the site's default page".
func noRedirectClient(client *http.Client) *http.Client {
	clone := *client
	clone.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &clone
}

func readBoundedResponse(response *http.Response) ([]byte, error) {
	return readBoundedBody(response, maxGroupResponseSize)
}

// readBoundedBody reads a response body up to limit bytes and rejects anything
// larger instead of truncating it.
func readBoundedBody(response *http.Response, limit int64) ([]byte, error) {
	payload, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(payload)) > limit {
		return nil, errors.New("provider group response exceeds size limit")
	}
	return payload, nil
}
