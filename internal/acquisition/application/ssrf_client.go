package application

import (
	"context"
	"net"
	"net/http"

	hardeningtransport "bansoko/internal/hardening/transport"
)

// SSRFClientForProvider clones base into a client whose transport enforces the
// shared provider HTTP rules: the target address must be public (unless the host
// is explicitly fixture-allowed), a redirect is validated by check, and the
// configured network route wins over the environment proxy.
//
// It exists so a new provider reader cannot accidentally dial an internal host:
// the SSRF-safe transport is built in exactly one package, and every provider
// HTTP surface — RSS, torrents, catalog search — goes through it.
func SSRFClientForProvider(base *http.Client, resolver func(context.Context, string) ([]net.IP, error), allowPrivateHosts []string, check func(*http.Request, []*http.Request) error) (*http.Client, error) {
	client := base
	if client == nil {
		client = &http.Client{Timeout: rssHTTPRequestTimeout, Transport: hardeningtransport.DirectTransport()}
	}
	copyClient := *client
	transport, err := (HTTPRSSFetcher{Client: &copyClient, Resolver: resolver, AllowPrivateHosts: allowPrivateHosts}).secureTransport(copyClient.Transport)
	if err != nil {
		return nil, err
	}
	copyClient.Transport = transport
	if check != nil {
		copyClient.CheckRedirect = check
	}
	return &copyClient, nil
}
