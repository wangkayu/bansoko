// Package jackett owns the single Jackett aggregate endpoint used by connection
// tests and keyword searches. Tracker selection and aggregation stay in Jackett.
package jackett

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
)

const AggregateTorznabPath = "/api/v2.0/indexers/all/results/torznab/api"

// AggregateURL builds a credential-bearing request URL for the configured `all`
// aggregate. The returned URL is request-only: callers must never put it in a
// snapshot identity, a read model, an error or a log.
func AggregateURL(rawEndpoint, apiKey string, parameters url.Values) (*url.URL, error) {
	base, err := parseEndpoint(rawEndpoint)
	if err != nil {
		return nil, err
	}
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return nil, errors.New("Jackett API key is not configured")
	}
	aggregatePath := strings.TrimRight(base.Path, "/")
	if !strings.HasSuffix(aggregatePath, AggregateTorznabPath) {
		aggregatePath += AggregateTorznabPath
	}
	base.Path = aggregatePath
	values := make(url.Values, len(parameters)+1)
	for key, entries := range parameters {
		values[key] = append([]string(nil), entries...)
	}
	values.Set("apikey", apiKey)
	base.RawQuery = values.Encode()
	return base, nil
}

func parseEndpoint(rawEndpoint string) (*url.URL, error) {
	base, err := url.Parse(strings.TrimRight(strings.TrimSpace(rawEndpoint), "/"))
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Hostname() == "" || base.User != nil || base.RawQuery != "" || base.ForceQuery || base.Fragment != "" {
		return nil, errors.New("Jackett endpoint must be an absolute HTTP URL without credentials, query or fragment")
	}
	return base, nil
}

// ValidateRedirect applies the same bounded HTTP(S)-only redirect policy to
// searches and connection tests. Credential-bearing query URLs must never be
// propagated through Referer or ambient authorization headers.
func ValidateRedirect(request *http.Request, via []*http.Request) error {
	if len(via) >= 5 {
		return errors.New("Torznab redirect limit exceeded")
	}
	redirect := request.URL
	if (redirect.Scheme != "http" && redirect.Scheme != "https") || redirect.Hostname() == "" || redirect.User != nil || redirect.Fragment != "" {
		return errors.New("Torznab redirect rejected")
	}
	if len(via) > 0 && strings.EqualFold(via[len(via)-1].URL.Scheme, "https") && strings.EqualFold(redirect.Scheme, "http") {
		return errors.New("Torznab redirect rejected: HTTPS downgrade is not allowed")
	}
	request.Header.Del("Authorization")
	request.Header.Del("Cookie")
	request.Header.Del("Referer")
	return nil
}
