package channel

import (
	"crypto/ed25519"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func testKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	return publicKey, privateKey
}

func validDocument() Document {
	return Document{
		SchemaVersion:   SchemaVersion,
		Channel:         "test",
		ReleaseID:       "ci-231",
		Version:         "0.0.0-ci.231",
		ReleaseSequence: 231,
		ManifestURL:     "https://lab.example.test/owner/repo/packages/generic/bansoko/0.0.0-ci.231/release-manifest.json",
		ManifestDigest:  strings.Repeat("ab", 32),
		SourceCommit:    strings.Repeat("0", 40),
		PublishedAt:     time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC),
	}
}

func TestSignAndVerifyRoundTrip(t *testing.T) {
	publicKey, privateKey := testKey(t)
	document := validDocument()
	if err := document.Sign(privateKey); err != nil {
		t.Fatalf("sign: %v", err)
	}
	if document.Signature == "" {
		t.Fatal("signature was not written")
	}
	data, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if err := decoded.VerifySignature(publicKey); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if err := decoded.Matches(document.ReleaseID, document.Version, document.ManifestDigest, document.ReleaseSequence); err != nil {
		t.Fatalf("match: %v", err)
	}
}

// A verification key the deployment does not trust must never make a document
// authoritative, and a tampered body must not verify with the right key.
func TestVerifyRejectsForeignKeyAndTamperedBody(t *testing.T) {
	publicKey, privateKey := testKey(t)
	otherPublic, _ := testKey(t)
	document := validDocument()
	if err := document.Sign(privateKey); err != nil {
		t.Fatal(err)
	}
	if err := document.VerifySignature(otherPublic); err == nil {
		t.Fatal("a foreign key verified the document")
	}
	tampered := document
	tampered.ReleaseSequence = document.ReleaseSequence + 1
	if err := tampered.VerifySignature(publicKey); err == nil {
		t.Fatal("a tampered sequence verified")
	}
	unsigned := validDocument()
	if err := unsigned.VerifySignature(publicKey); err == nil {
		t.Fatal("an unsigned document verified")
	}
}

func TestDecodeIsStrict(t *testing.T) {
	document := validDocument()
	data, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Decode(append(data, []byte("{}")...)); err == nil {
		t.Fatal("trailing JSON data was accepted")
	}
	if _, err := Decode([]byte(`{"schema_version":"update-channel-1","unknown":1}`)); err == nil {
		t.Fatal("an unknown field was accepted")
	}
	if _, err := Decode([]byte("not json")); err == nil {
		t.Fatal("invalid JSON was accepted")
	}
	oversized := make([]byte, MaxDocumentBytes+1)
	if _, err := Decode(oversized); err == nil {
		t.Fatal("an oversized document was accepted")
	}
}

// The document contract refuses every identity the deployment could not act on.
func TestValidateRejectsUnusableDocuments(t *testing.T) {
	cases := map[string]func(*Document){
		"schema version":    func(d *Document) { d.SchemaVersion = "update-channel-2" },
		"missing channel":   func(d *Document) { d.Channel = "" },
		"missing release":   func(d *Document) { d.ReleaseID = "" },
		"missing version":   func(d *Document) { d.Version = "" },
		"mutable latest":    func(d *Document) { d.Version = "latest" },
		"zero sequence":     func(d *Document) { d.ReleaseSequence = 0 },
		"negative":          func(d *Document) { d.ReleaseSequence = -1 },
		"missing url":       func(d *Document) { d.ManifestURL = "" },
		"short digest":      func(d *Document) { d.ManifestDigest = "abc" },
		"non hex digest":    func(d *Document) { d.ManifestDigest = strings.Repeat("zz", 32) },
		"missing published": func(d *Document) { d.PublishedAt = time.Time{} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			document := validDocument()
			mutate(&document)
			if err := document.Validate(); err == nil {
				t.Fatalf("%s was accepted", name)
			}
		})
	}
	if err := validDocument().Validate(); err != nil {
		t.Fatalf("valid document rejected: %v", err)
	}
}

// A channel that names one release while the signed manifest carries another is
// refused on every identity field, never resolved in favour of one side.
func TestMatchesRejectsAnyIdentityDrift(t *testing.T) {
	document := validDocument()
	if err := document.Matches(document.ReleaseID, document.Version, document.ManifestDigest, document.ReleaseSequence); err != nil {
		t.Fatalf("identical identity rejected: %v", err)
	}
	if err := document.Matches("ci-232", document.Version, document.ManifestDigest, document.ReleaseSequence); err == nil {
		t.Fatal("release id drift accepted")
	}
	if err := document.Matches(document.ReleaseID, "0.0.0-ci.232", document.ManifestDigest, document.ReleaseSequence); err == nil {
		t.Fatal("version drift accepted")
	}
	if err := document.Matches(document.ReleaseID, document.Version, strings.Repeat("cd", 32), document.ReleaseSequence); err == nil {
		t.Fatal("digest drift accepted")
	}
	if err := document.Matches(document.ReleaseID, document.Version, document.ManifestDigest, document.ReleaseSequence+1); err == nil {
		t.Fatal("sequence drift accepted")
	}
}
