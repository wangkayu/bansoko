package application

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The published image bakes BANSOKO_UPDATE_CHANNEL_URL and
// BANSOKO_UPDATE_PACKAGE_ROOT so an unconfigured deployment can update itself.
// Nothing in the Go code would notice them drifting into a shape this package
// refuses: the image would build, boot, and report itself permanently
// unavailable. This test reads the Dockerfile and checks both halves of that
// shape — that the resolver accepts the pair, and that neither address is one
// of the two GitHub web forms documented to answer with a cross-host redirect.
//
// The redirect itself is refused at fetch time by this package's own
// CheckRedirect policy (see source_http_test coverage), which no offline test
// can observe on a real URL, so it is asserted here as a shape: these two
// github.com paths are the ones that 302, and the direct host forms are not.
func TestImageBakedUpdateDefaultsAreAcceptedByTheResolver(t *testing.T) {
	baked := imageBakedUpdateDefaults(t)
	if _, err := newChannelResolver(
		baked[EnvChannelURL],
		baked[EnvPackageRoot],
		UpdateHTTPSourceConfig{
			Production: true,
		},
		make([]byte, 32),
	); err != nil {
		t.Fatalf("the baked update defaults are refused at startup: %v", err)
	}
	for _, name := range []string{EnvChannelURL, EnvPackageRoot} {
		for _, redirecting := range []string{
			"https://github.com/",
		} {
			if strings.HasPrefix(baked[name], redirecting) {
				t.Fatalf(
					"%s is baked as %q, a github.com web address that answers 302 to another host; the update client refuses that redirect",
					name, baked[name],
				)
			}
		}
		if strings.Contains(baked[name], "/raw/") || strings.Contains(baked[name], "/releases/download/") {
			t.Fatalf("%s is baked as %q, a GitHub redirect form", name, baked[name])
		}
	}
}

func imageBakedUpdateDefaults(t *testing.T) map[string]string {
	t.Helper()
	root := filepath.Join("..", "..", "..")
	dockerfile, err := os.ReadFile(filepath.Join(root, "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	baked := map[string]string{}
	for _, line := range strings.Split(string(dockerfile), "\n") {
		for _, name := range []string{EnvChannelURL, EnvPackageRoot} {
			prefix := name + "="
			if index := strings.Index(line, prefix); index >= 0 {
				value := strings.TrimSpace(line[index+len(prefix):])
				baked[name] = strings.TrimSpace(strings.TrimSuffix(value, "\\"))
			}
		}
	}
	for _, name := range []string{EnvChannelURL, EnvPackageRoot} {
		if baked[name] == "" {
			t.Fatalf("the image no longer bakes %s", name)
		}
	}
	return baked
}

// A channel document's manifest URL is whatever the publisher signed into it,
// and the resolver proves it sits inside the package root. With the package
// root baked to a branch directory, the publisher has to use that same shape.
func TestBakedPackageRootContainsThePublishedManifestURL(t *testing.T) {
	root, err := parseUpdateURL("https://raw.githubusercontent.com/wangkayu/bansoko/update-channel/")
	if err != nil {
		t.Fatal(err)
	}
	for _, manifest := range []string{
		"https://raw.githubusercontent.com/wangkayu/bansoko/update-channel/0.0.0-test.53/release-manifest.json",
	} {
		target, err := parseUpdateURL(manifest)
		if err != nil {
			t.Fatalf("%s is refused: %v", manifest, err)
		}
		if err := packageRootContains(root, target); err != nil {
			t.Fatalf("%s is outside the baked package root: %v", manifest, err)
		}
	}
	// Shapes that must stay refused: the redirect-bearing host form and a
	// release asset carry a different host or a query.
	for _, manifest := range []string{
		"https://github.com/wangkayu/bansoko/raw/update-channel/channel-v1.json",
	} {
		target, err := parseUpdateURL(manifest)
		if err != nil {
			continue
		}
		if err := packageRootContains(root, target); err == nil {
			t.Fatalf("%s was accepted inside the baked package root", manifest)
		}
	}
}
