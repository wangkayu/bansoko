package application

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

const maxRedirects = 3

type ipResolver interface {
	LookupIPAddr(context.Context, string) ([]net.IPAddr, error)
}

type contextDialer interface {
	DialContext(context.Context, string, string) (net.Conn, error)
}

type safeHTTPHooks struct {
	resolver ipResolver
	dialer   contextDialer
	// proxy selects the egress proxy for one request; nil means the standard
	// HTTP_PROXY/HTTPS_PROXY/NO_PROXY environment (http.ProxyFromEnvironment).
	proxy func(*http.Request) (*url.URL, error)
}

// newSafeHTTPClient builds the delivery client. Without an egress proxy every
// connection dials an address the transport itself resolved and validated as
// public (DNS answers from /etc/hosts, e.g. compose extra_hosts, included).
// With a container-level proxy (HTTP_PROXY/HTTPS_PROXY) the proxy is a trusted
// egress boundary: the request target must still resolve to public addresses
// before the request is handed to the proxy, and the proxy itself is dialed as
// configured.
func newSafeHTTPClient(hooks safeHTTPHooks) *http.Client {
	resolver := hooks.resolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	dialer := hooks.dialer
	if dialer == nil {
		dialer = &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	}
	proxy := hooks.proxy
	if proxy == nil {
		proxy = http.ProxyFromEnvironment
	}
	direct := &http.Transport{
		Proxy:                 nil,
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 15 * time.Second,
		IdleConnTimeout:       30 * time.Second,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, errors.New("safe transport rejected malformed dial address")
			}
			addresses, err := resolvePublic(ctx, resolver, host)
			if err != nil {
				return nil, err
			}
			// Dial the validated address, never the hostname, to close DNS-rebind TOCTOU.
			return dialer.DialContext(ctx, network, net.JoinHostPort(addresses[0].IP.String(), port))
		},
	}
	proxied := &http.Transport{
		Proxy:                 proxy,
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 15 * time.Second,
		IdleConnTimeout:       30 * time.Second,
		DialContext:           dialer.DialContext,
	}
	transport := safeRoundTripper{direct: direct, proxied: proxied, proxy: proxy, resolver: resolver}
	client := &http.Client{Transport: transport, Timeout: handlerRequestTimeout}
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) > maxRedirects {
			return errors.New("safe transport redirect limit exceeded")
		}
		if err := validateHTTPSURL(req.URL); err != nil {
			return err
		}
		origin := via[0].URL
		if !strings.EqualFold(req.URL.Scheme, origin.Scheme) || !strings.EqualFold(req.URL.Host, origin.Host) {
			return errors.New("safe transport rejected cross-origin redirect")
		}
		return nil
	}
	return client
}

// safeRoundTripper routes a request either directly (validated dial) or, when
// the environment selects an egress proxy for it, through that proxy after the
// target host has been validated as public.
type safeRoundTripper struct {
	direct   http.RoundTripper
	proxied  http.RoundTripper
	proxy    func(*http.Request) (*url.URL, error)
	resolver ipResolver
}

func (t safeRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	proxyURL, err := t.proxy(request)
	if err != nil {
		return nil, errors.New("safe transport egress proxy is invalid")
	}
	if proxyURL == nil {
		return t.direct.RoundTrip(request)
	}
	if _, err := resolvePublic(request.Context(), t.resolver, request.URL.Hostname()); err != nil {
		return nil, err
	}
	return t.proxied.RoundTrip(request)
}

// resolvePublic resolves host and fails closed unless every answer is public.
func resolvePublic(ctx context.Context, resolver ipResolver, host string) ([]net.IPAddr, error) {
	if ip := net.ParseIP(host); ip != nil {
		if unsafeIP(ip) {
			return nil, errors.New("safe transport rejected a non-public address")
		}
		return []net.IPAddr{{IP: ip}}, nil
	}
	addresses, err := resolver.LookupIPAddr(ctx, host)
	if err != nil || len(addresses) == 0 {
		return nil, errors.New("safe transport DNS resolution failed")
	}
	for _, candidate := range addresses {
		if unsafeIP(candidate.IP) {
			return nil, errors.New("safe transport rejected a non-public address")
		}
	}
	return addresses, nil
}

func validateHTTPSURL(candidate *url.URL) error {
	if candidate == nil || !candidate.IsAbs() || !strings.EqualFold(candidate.Scheme, "https") || candidate.Host == "" {
		return errors.New("endpoint must be an absolute HTTPS URL")
	}
	if candidate.User != nil || candidate.Fragment != "" {
		return errors.New("endpoint must not contain userinfo or fragment")
	}
	host := candidate.Hostname()
	if host == "" {
		return errors.New("endpoint host is required")
	}
	if parsed := net.ParseIP(host); parsed != nil && unsafeIP(parsed) {
		return errors.New("endpoint host must be public")
	}
	return nil
}

func parseHTTPSURL(raw string) (*url.URL, error) {
	candidate, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, errors.New("endpoint URL is invalid")
	}
	if err := validateHTTPSURL(candidate); err != nil {
		return nil, err
	}
	return candidate, nil
}

var nonPublicPrefixes = []netip.Prefix{
	// IPv4 special-purpose space. Keep this explicit because net.IP.IsPrivate and
	// IsGlobalUnicast do not reject ranges such as CGNAT and documentation nets.
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.31.196.0/24"),
	netip.MustParsePrefix("192.52.193.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("192.175.48.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("240.0.0.0/4"),

	// IPv6 local, translation, documentation, benchmarking, transition, and
	// other protocol-assignment space that must never be an outbound target.
	netip.MustParsePrefix("::/128"),
	netip.MustParsePrefix("::1/128"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:20::/28"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("3fff::/20"),
	netip.MustParsePrefix("5f00::/16"),
	netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("ff00::/8"),
}

func unsafeIP(ip net.IP) bool {
	address, ok := netip.AddrFromSlice(ip)
	if !ok {
		return true
	}
	address = address.Unmap()
	if !address.IsGlobalUnicast() {
		return true
	}
	for _, prefix := range nonPublicPrefixes {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}

func safeTransportError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("delivery transport failed: %w", err)
}
