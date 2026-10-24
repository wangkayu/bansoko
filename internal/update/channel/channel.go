// Package channel defines the release-discovery document an already configured
// deployment reads to learn which signed CI release is the newest one, without
// the deployment naming a version itself.
//
// The document is a locator, never a release identity: it names one immutable
// release (`release_id`, `version`, positive `release_sequence`) and the exact
// immutable manifest URL that carries it. A deployment that resolves the
// channel still fetches the named manifest through its own source policy and
// re-verifies the signature, digest, payload signature, schema and release
// sequence; the durable fence floor, not the channel, authorizes an admission.
// The channel's own signature exists so an operator and the runtime can tell a
// deliberate publication from tampered or truncated metadata; a document that
// does not verify fails closed and is never silently replaced by another
// candidate.
//
// The document is shared by the CI signer (`cmd/bansoko-release channel-sign`) and
// the runtime verifier, so the canonical form exists exactly once.
package channel

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

const (
	// SchemaVersion is the only channel document layout this package accepts.
	SchemaVersion = "update-channel-1"

	// MaxDocumentBytes bounds the discovery document. It carries one release
	// identity and nothing large, so anything bigger is hostile input.
	MaxDocumentBytes = 64 << 10

	// MaxFieldLength bounds free-text fields so a hostile document cannot carry
	// unbounded values into logs or the journal.
	MaxFieldLength = 512
)

var (
	// ErrDocument reports a channel document the local binary cannot consume:
	// too large, not valid JSON, unknown fields, trailing data, or an identity
	// that violates the contract. All of them fail closed.
	ErrDocument = errors.New("update channel document rejected")
	// ErrSignature reports a channel document whose signature does not verify.
	ErrSignature = errors.New("update channel signature rejected")
)

// Document is one published channel pointer. Field order is the canonical
// serialization order, so Signature must stay last.
type Document struct {
	SchemaVersion   string    `json:"schema_version"`
	Channel         string    `json:"channel"`
	ReleaseID       string    `json:"release_id"`
	Version         string    `json:"version"`
	ReleaseSequence int64     `json:"release_sequence"`
	ManifestURL     string    `json:"manifest_url"`
	ManifestDigest  string    `json:"manifest_digest"`
	SourceCommit    string    `json:"source_commit"`
	PublishedAt     time.Time `json:"published_at"`
	Signature       string    `json:"signature"`
}

// signingCopy returns the document without its signature: the exact byte set
// both the signer and the verifier canonicalize.
func (d Document) signingCopy() Document {
	d.Signature = ""
	return d
}

// CanonicalBytes returns the deterministic document bytes that are signed.
func (d Document) CanonicalBytes() ([]byte, error) {
	return json.Marshal(d.signingCopy())
}

// Decode strictly decodes one bounded channel document. Unknown fields,
// trailing data and oversized documents are refused rather than tolerated, so a
// future document layout cannot be half-understood by an older binary.
func Decode(data []byte) (Document, error) {
	if len(data) > MaxDocumentBytes {
		return Document{}, fmt.Errorf("%w: document exceeds the bounded size", ErrDocument)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var document Document
	if err := decoder.Decode(&document); err != nil {
		return Document{}, fmt.Errorf("%w: document is not valid JSON", ErrDocument)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return Document{}, fmt.Errorf("%w: document contains trailing JSON data", ErrDocument)
		}
		return Document{}, fmt.Errorf("%w: document contains trailing data", ErrDocument)
	}
	return document, nil
}

// Validate proves the document's own contract. It deliberately does not
// validate the manifest URL beyond "present": the deployment-owned source
// policy owns URL shape and namespace containment, so an unusable URL is
// refused by the same code that refuses any other unusable update URL.
func (d Document) Validate() error {
	if strings.TrimSpace(d.SchemaVersion) != SchemaVersion {
		return fmt.Errorf("%w: unsupported schema version", ErrDocument)
	}
	if strings.TrimSpace(d.Channel) == "" || len(d.Channel) > MaxFieldLength {
		return fmt.Errorf("%w: channel name is required and bounded", ErrDocument)
	}
	if strings.TrimSpace(d.ReleaseID) == "" || len(d.ReleaseID) > MaxFieldLength {
		return fmt.Errorf("%w: release id is required and bounded", ErrDocument)
	}
	if strings.TrimSpace(d.Version) == "" || len(d.Version) > MaxFieldLength {
		return fmt.Errorf("%w: version is required and bounded", ErrDocument)
	}
	if strings.Contains(strings.ToLower(d.Version), "latest") {
		return fmt.Errorf("%w: a mutable latest identity is not a release", ErrDocument)
	}
	if d.ReleaseSequence < 1 {
		return fmt.Errorf("%w: release sequence must be a positive integer", ErrDocument)
	}
	if strings.TrimSpace(d.ManifestURL) == "" {
		return fmt.Errorf("%w: manifest url is required", ErrDocument)
	}
	if !isSHA256(strings.TrimSpace(d.ManifestDigest)) {
		return fmt.Errorf("%w: manifest digest must be a sha256 digest", ErrDocument)
	}
	if len(d.SourceCommit) > MaxFieldLength {
		return fmt.Errorf("%w: source commit is bounded", ErrDocument)
	}
	if d.PublishedAt.IsZero() {
		return fmt.Errorf("%w: published_at is required", ErrDocument)
	}
	return nil
}

// Sign validates the document, then signs its canonical bytes with the release
// key. The signature is base64 raw-std, matching the release manifest and
// artifact signatures.
func (d *Document) Sign(privateKey ed25519.PrivateKey) error {
	if d == nil {
		return errors.New("channel document is required")
	}
	if len(privateKey) != ed25519.PrivateKeySize {
		return errors.New("ed25519 private key is required")
	}
	if err := d.Validate(); err != nil {
		return err
	}
	data, err := d.CanonicalBytes()
	if err != nil {
		return fmt.Errorf("canonicalize channel document: %w", err)
	}
	d.Signature = base64.RawStdEncoding.EncodeToString(ed25519.Sign(privateKey, data))
	return nil
}

// VerifySignature validates the document's contract and proves its signature
// against the deployment's trusted public key.
func (d Document) VerifySignature(publicKey ed25519.PublicKey) error {
	if len(publicKey) != ed25519.PublicKeySize {
		return errors.New("ed25519 public key is required")
	}
	if err := d.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(d.Signature) == "" {
		return fmt.Errorf("%w: signature is required", ErrSignature)
	}
	signature, err := base64.RawStdEncoding.DecodeString(d.Signature)
	if err != nil || len(signature) != ed25519.SignatureSize {
		return fmt.Errorf("%w: signature is invalid", ErrSignature)
	}
	data, err := d.CanonicalBytes()
	if err != nil {
		return fmt.Errorf("canonicalize channel document: %w", err)
	}
	if !ed25519.Verify(publicKey, data, signature) {
		return fmt.Errorf("%w: verification failed", ErrSignature)
	}
	return nil
}

// Matches proves a verified release manifest is exactly the release this
// document names. A channel that names one release while the manifest it points
// at carries another is refused, never resolved in favour of either side.
func (d Document) Matches(releaseID, version, manifestDigest string, releaseSequence int64) error {
	switch {
	case strings.TrimSpace(d.ReleaseID) != strings.TrimSpace(releaseID):
		return fmt.Errorf("%w: channel release id does not match the signed manifest", ErrDocument)
	case strings.TrimSpace(d.Version) != strings.TrimSpace(version):
		return fmt.Errorf("%w: channel version does not match the signed manifest", ErrDocument)
	case d.ReleaseSequence != releaseSequence:
		return fmt.Errorf("%w: channel release sequence does not match the signed manifest", ErrDocument)
	case strings.TrimSpace(d.ManifestDigest) != strings.TrimSpace(manifestDigest):
		return fmt.Errorf("%w: channel manifest digest does not match the signed manifest", ErrDocument)
	}
	return nil
}

func isSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, char := range value {
		switch {
		case char >= '0' && char <= '9':
		case char >= 'a' && char <= 'f':
		default:
			return false
		}
	}
	return true
}
