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

func TestChannelGateSameSequenceRequiresExactSignedBytes(t *testing.T) {
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	public := filepath.Join(root, "public.key")
	old := filepath.Join(root, "old.json")
	next := filepath.Join(root, "next.json")
	if err := os.WriteFile(public, []byte(hex.EncodeToString(pub)), 0600); err != nil {
		t.Fatal(err)
	}
	document := channel.Document{SchemaVersion: channel.SchemaVersion, Channel: "test", ReleaseID: "v0.1.0-test.7", Version: "0.1.0-test.7", ReleaseSequence: 7, ManifestURL: "https://example.com/api/packages/owner/generic/name/7/release-manifest.json", ManifestDigest: string(makeHex('a', 64)), SourceCommit: string(makeHex('b', 40)), PublishedAt: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)}
	if err := document.Sign(key); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(old, data, 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"--published", old, "--candidate", next, "--public-key", public, "--lane", "test"}
	if err := os.WriteFile(next, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := channelGate(args); err != nil {
		t.Fatalf("identical repeat: %v", err)
	}
	for _, change := range []func(*channel.Document){func(d *channel.Document) { d.ManifestURL += "-changed" }, func(d *channel.Document) { d.SourceCommit = string(makeHex('c', 40)) }, func(d *channel.Document) { d.PublishedAt = d.PublishedAt.Add(time.Second) }, func(d *channel.Document) { d.Version = "0.1.1-test.7" }} {
		candidate := document
		change(&candidate)
		if err := candidate.Sign(key); err != nil {
			t.Fatal(err)
		}
		if err := candidate.VerifySignature(pub); err != nil {
			t.Fatal(err)
		}
		altered, err := json.Marshal(candidate)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(next, altered, 0600); err != nil {
			t.Fatal(err)
		}
		if err := channelGate(args); err == nil {
			t.Fatal("same-sequence changed signed document accepted")
		}
	}
	if err := os.WriteFile(next, append(data, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	if err := channelGate(args); err == nil {
		t.Fatal("different serialized bytes accepted")
	}
}

func makeHex(char byte, size int) []byte {
	b := make([]byte, size)
	for i := range b {
		b[i] = char
	}
	return b
}
