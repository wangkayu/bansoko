package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"bansoko/internal/update/channel"
)

func signedChannel(t *testing.T, key ed25519.PrivateKey, path, name, version string, sequence int64) {
	t.Helper()
	document := channel.Document{SchemaVersion: channel.SchemaVersion, Channel: name, ReleaseID: "r-" + version, Version: version, ReleaseSequence: sequence, ManifestURL: "https://raw.githubusercontent.com/o/r/update-channel/" + version + "/release-manifest.json", ManifestDigest: string(makeHex('a', 64)), SourceCommit: string(makeHex('b', 40)), PublishedAt: time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC)}
	if err := document.Sign(key); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func writePublicKey(t *testing.T, path string, key ed25519.PublicKey) {
	t.Helper()
	if err := os.WriteFile(path, []byte(hex.EncodeToString(key)), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestChannelGateRequiresTheVersionToAdvance(t *testing.T) {
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	public := filepath.Join(root, "public.key")
	writePublicKey(t, public, pub)
	published, candidate := filepath.Join(root, "published.json"), filepath.Join(root, "candidate.json")
	gate := func() error {
		return channelGate([]string{"--published", published, "--candidate", candidate, "--public-key", public, "--lane", "stable"})
	}
	// The retired test channel moves to the formal stable channel once.
	signedChannel(t, key, published, "test", "0.0.0-test.120", 120)
	signedChannel(t, key, candidate, "stable", "0.1.0", 130)
	if err := gate(); err != nil {
		t.Fatalf("first formal release over the test channel: %v", err)
	}
	signedChannel(t, key, published, "stable", "0.1.0", 130)
	signedChannel(t, key, candidate, "stable", "0.1.1", 140)
	if err := gate(); err != nil {
		t.Fatalf("patch release: %v", err)
	}
	for name, version := range map[string]string{"same version": "0.1.0", "older version": "0.0.9", "test identity": "0.0.0-test.141"} {
		signedChannel(t, key, candidate, "stable", version, 141)
		if err := gate(); err == nil {
			t.Fatalf("%s accepted with a larger sequence", name)
		}
	}
	signedChannel(t, key, candidate, "test", "0.2.0", 150)
	if err := gate(); err == nil {
		t.Fatal("stable moved back to test")
	}
	signedChannel(t, key, candidate, "beta", "0.2.0", 150)
	if err := gate(); err == nil {
		t.Fatal("stable renamed to another channel")
	}
}

func TestVersionGateRefusesANonAdvancingRelease(t *testing.T) {
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	other, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	public, wrong := filepath.Join(root, "public.key"), filepath.Join(root, "other.key")
	writePublicKey(t, public, pub)
	writePublicKey(t, wrong, other)
	published := filepath.Join(root, "published.json")
	signedChannel(t, key, published, "stable", "0.2.0", 200)
	gate := func(version, key string) error {
		return versionGate([]string{"--published", published, "--version", version, "--public-key", key, "--lane", "stable"})
	}
	if err := gate("0.2.1", public); err != nil {
		t.Fatalf("advancing version refused: %v", err)
	}
	for _, version := range []string{"0.2.0", "0.1.9", "0.2.1-rc.1", "v0.3.0", ""} {
		if err := gate(version, public); err == nil {
			t.Fatalf("%q accepted", version)
		}
	}
	if err := gate("0.3.0", wrong); err == nil {
		t.Fatal("a channel signed by another key was trusted")
	}
	if err := versionGate([]string{"--published", "", "--version", "0.1.0", "--public-key", public, "--lane", "stable"}); err != nil {
		t.Fatalf("first publication refused: %v", err)
	}
	// A rerun of the run that already published 0.2.0 continues; any other
	// sequence or commit under the same version is refused.
	commit := string(makeHex('b', 40))
	rerun := func(sequence, sourceCommit string) error {
		return versionGate([]string{"--published", published, "--version", "0.2.0", "--public-key", public, "--sequence", sequence, "--source-commit", sourceCommit, "--lane", "stable"})
	}
	if err := rerun("200", commit); err != nil {
		t.Fatalf("idempotent rerun refused: %v", err)
	}
	for name, args := range map[string][2]string{"other sequence": {"201", commit}, "other commit": {"200", string(makeHex('c', 40))}, "no commit": {"200", ""}} {
		if err := rerun(args[0], args[1]); err == nil {
			t.Fatalf("%s accepted as a rerun", name)
		}
	}
}
