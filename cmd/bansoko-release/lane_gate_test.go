package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The test lane advances its own channel with prerelease identities numbered
// by the run, and the two lanes can never take over each other's channel even
// though they share the signing key and the run counter.
func TestChannelGateKeepsTheTwoLanesApart(t *testing.T) {
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	public := filepath.Join(root, "public.key")
	writePublicKey(t, public, pub)
	published, candidate := filepath.Join(root, "published.json"), filepath.Join(root, "candidate.json")
	gate := func(lane string) error {
		return channelGate([]string{"--published", published, "--candidate", candidate, "--public-key", public, "--lane", lane})
	}
	firstPublication := func(lane string) error {
		return channelGate([]string{"--published", "", "--candidate", candidate, "--public-key", public, "--lane", lane})
	}

	// Test lane: first publication, then advancing runs, including a core bump.
	signedChannel(t, key, candidate, "test", "0.1.0-test.120", 120)
	if err := firstPublication("test"); err != nil {
		t.Fatalf("first test publication: %v", err)
	}
	signedChannel(t, key, published, "test", "0.1.0-test.120", 120)
	for _, next := range []struct {
		version  string
		sequence int64
	}{{"0.1.0-test.121", 121}, {"0.1.1-test.135", 135}} {
		signedChannel(t, key, candidate, "test", next.version, next.sequence)
		if err := gate("test"); err != nil {
			t.Fatalf("test lane advance to %s: %v", next.version, err)
		}
	}
	refusals := map[string]struct {
		channel, version string
		sequence         int64
	}{
		"older run":                  {"test", "0.1.0-test.119", 119},
		"lower core, larger run":     {"test", "0.0.9-test.130", 130},
		"run differs from sequence":  {"test", "0.1.0-test.130", 131},
		"formal version on test":     {"test", "0.1.0", 130},
		"other prerelease shape":     {"test", "0.1.0-rc.130", 130},
		"stable document on test":    {"stable", "0.1.0", 130},
		"stable-named test identity": {"stable", "0.1.0-test.130", 130},
	}
	for name, c := range refusals {
		signedChannel(t, key, candidate, c.channel, c.version, c.sequence)
		if err := gate("test"); err == nil {
			t.Fatalf("test lane accepted %s", name)
		}
	}
	// A test lane never takes the stable branch's one-way rename, and never
	// accepts a stable document as its predecessor.
	signedChannel(t, key, published, "stable", "0.1.0", 120)
	signedChannel(t, key, candidate, "test", "0.1.1-test.130", 130)
	if err := gate("test"); err == nil {
		t.Fatal("test lane advanced over a stable document")
	}

	// Stable lane: a test-lane candidate is refused even with a larger
	// sequence and a newer version than the published stable release.
	signedChannel(t, key, published, "stable", "0.1.0", 120)
	for name, c := range map[string]struct {
		channel, version string
	}{
		"test document on stable":    {"test", "0.1.1-test.130"},
		"test identity named stable": {"stable", "0.1.1-test.130"},
	} {
		signedChannel(t, key, candidate, c.channel, c.version, 130)
		if err := gate("stable"); err == nil {
			t.Fatalf("stable lane accepted %s", name)
		}
		if err := firstPublication("stable"); err == nil {
			t.Fatalf("stable lane first publication accepted %s", name)
		}
	}
	signedChannel(t, key, candidate, "stable", "0.1.1", 130)
	if err := gate("stable"); err != nil {
		t.Fatalf("stable advance: %v", err)
	}
	if err := gate(""); err == nil || !strings.Contains(err.Error(), "--lane") {
		t.Fatalf("a missing lane was not refused: %v", err)
	}
}

func TestVersionGateOnTheTestLane(t *testing.T) {
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	public := filepath.Join(root, "public.key")
	writePublicKey(t, public, pub)
	published := filepath.Join(root, "published.json")
	commit := string(makeHex('b', 40))
	gate := func(path, version, sequence, sourceCommit, lane string) error {
		return versionGate([]string{"--published", path, "--version", version, "--public-key", public, "--sequence", sequence, "--source-commit", sourceCommit, "--lane", lane})
	}
	if err := gate("", "0.1.0-test.120", "120", commit, "test"); err != nil {
		t.Fatalf("first test publication refused: %v", err)
	}
	signedChannel(t, key, published, "test", "0.1.0-test.120", 120)
	if err := gate(published, "0.1.0-test.121", "121", commit, "test"); err != nil {
		t.Fatalf("advancing test build refused: %v", err)
	}
	if err := gate(published, "0.1.0-test.120", "120", commit, "test"); err != nil {
		t.Fatalf("idempotent rerun refused: %v", err)
	}
	for name, args := range map[string][2]string{
		"older run":               {"0.1.0-test.119", "119"},
		"formal version":          {"0.1.1", "121"},
		"run is not the sequence": {"0.1.0-test.125", "124"},
		"no sequence":             {"0.1.0-test.125", "0"},
	} {
		if err := gate(published, args[0], args[1], commit, "test"); err == nil {
			t.Fatalf("test lane version gate accepted %s", name)
		}
	}
	// A stable document is not the test lane's predecessor, and a test
	// document other than the retired legacy one is not the stable lane's.
	stable := filepath.Join(root, "stable.json")
	signedChannel(t, key, stable, "stable", "0.1.0", 110)
	if err := gate(stable, "0.1.1-test.121", "121", commit, "test"); err == nil {
		t.Fatal("test lane gated against a stable document")
	}
	if err := gate(stable, "0.1.0-test.121", "121", commit, "stable"); err == nil {
		t.Fatal("stable lane accepted a test version")
	}
	if err := gate(published, "0.1.1-test.121", "121", commit, "beta"); err == nil {
		t.Fatal("an unknown lane was accepted")
	}
}

func TestTestVersionCommandPrintsTheDerivedVersion(t *testing.T) {
	root := t.TempDir()
	versionFile := filepath.Join(root, "VERSION")
	notes := filepath.Join(root, "notes")
	if err := os.WriteFile(versionFile, []byte("0.1.0-dev\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(notes, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(notes, "versions.txt"), []byte("0.1.0 2026-10-28\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := testVersion([]string{"--version-file", versionFile, "--notes-dir", notes, "--run", "0"}); err == nil {
		t.Fatal("a zero run was accepted")
	}
	if err := testVersion([]string{"--version-file", versionFile, "--notes-dir", notes, "--run", "130"}); err != nil {
		t.Fatal(err)
	}
}
