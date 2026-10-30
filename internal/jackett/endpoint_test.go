package jackett

import (
	"net/http"
	"net/url"
	"testing"
)

func TestValidateRedirectBoundsAndCredentialPolicy(t *testing.T) {
	request := &http.Request{URL: &url.URL{Scheme: "https", Host: "tracker.example.test"}, Header: make(http.Header)}
	request.Header.Set("Authorization", "Bearer inherited")
	request.Header.Set("Cookie", "session=inherited")
	request.Header.Set("Referer", "https://jackett.example.test/api?apikey=secret")
	if err := ValidateRedirect(request, []*http.Request{{URL: &url.URL{Scheme: "https", Host: "jackett.example.test"}}}); err != nil {
		t.Fatalf("ordinary HTTPS redirect rejected: %v", err)
	}
	for _, name := range []string{"Authorization", "Cookie", "Referer"} {
		if request.Header.Get(name) != "" {
			t.Fatalf("redirect retained %s", name)
		}
	}

	for _, testCase := range []struct {
		name     string
		target   *url.URL
		previous *url.URL
		via      int
	}{
		{name: "non HTTP", target: &url.URL{Scheme: "file", Host: "tracker.example.test"}},
		{name: "missing host", target: &url.URL{Scheme: "https"}},
		{name: "userinfo", target: &url.URL{Scheme: "https", Host: "tracker.example.test", User: url.User("user")}},
		{name: "fragment", target: &url.URL{Scheme: "https", Host: "tracker.example.test", Fragment: "fragment"}},
		{name: "HTTPS downgrade", target: &url.URL{Scheme: "http", Host: "tracker.example.test"}, previous: &url.URL{Scheme: "https", Host: "jackett.example.test"}},
		{name: "redirect limit", target: &url.URL{Scheme: "https", Host: "tracker.example.test"}, via: 5},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			via := make([]*http.Request, testCase.via)
			if testCase.previous != nil {
				via = []*http.Request{{URL: testCase.previous}}
			}
			if err := ValidateRedirect(&http.Request{URL: testCase.target, Header: make(http.Header)}, via); err == nil {
				t.Fatal("unsafe redirect was accepted")
			}
		})
	}
}
