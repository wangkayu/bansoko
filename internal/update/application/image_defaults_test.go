package application

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The image no longer bakes an update source (owner 2026-11-01): a deployment
// names a lane and the lane names this project's own channel branch. The risk
// this test guards is the reverse of the old one — a stale Dockerfile that
// still bakes one of the retired URLs would look configured while the client
// ignores it entirely.
func TestImageDoesNotBakeARetiredUpdateSource(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	dockerfile, err := os.ReadFile(filepath.Join(root, "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{EnvChannelURL, EnvPackageRoot, EnvManifestURL, "BANSOKO_UPDATE_PUBLIC_KEY_PATH"} {
		if strings.Contains(string(dockerfile), name) {
			t.Fatalf("the image still bakes %s, which the update client no longer reads", name)
		}
	}
	documentURL, packageRoot, err := LaneSource("", "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(documentURL, "https://raw.githubusercontent.com/wangkayu/bansoko/") ||
		!strings.HasPrefix(packageRoot, "https://raw.githubusercontent.com/wangkayu/bansoko/") {
		t.Fatalf("the stable lane is not this project's own channel: %s %s", documentURL, packageRoot)
	}
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
