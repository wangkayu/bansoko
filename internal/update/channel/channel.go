// Package channel defines the release-discovery document an already configured
// deployment reads to learn which CI release is the newest one, without the
// deployment naming a version itself.
//
// The document is a locator, never a release identity: it names one immutable
// release (`release_id`, `version`, positive `release_sequence`) and the exact
// immutable manifest URL that carries it. A deployment that resolves the
// channel still fetches the named manifest through its own source policy and
// re-checks its digest, payload digest, schema and release sequence; the
// durable fence floor, not the channel, authorizes an admission.
//
// The document is not signed (owner 2026-11-01): the update client only ever
// reads this project's own channel branches over HTTPS, so the repository is
// the trust anchor and a signature would only add a key to lose. A document
// that does not decode or does not validate still fails closed.
//
// The document is shared by CI (`cmd/bansoko-release channel-write`) and the
// runtime reader, so the canonical form exists exactly once.
package channel

import (
	"bytes"
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
)

// Document is one published channel pointer. The document is not signed: a
// deployment only ever reads this project's own channel branches over HTTPS,
// and the manifest it names is bound by its digest and the durable release
// sequence.
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
	// Signature is the retired document signature (owner 2026-11-01). It stays
	// in the struct only so a document published before the retirement still
	// decodes; nothing reads or checks it.
	Signature string `json:"signature,omitempty"`
}

// CanonicalBytes returns the deterministic document bytes.
func (d Document) CanonicalBytes() ([]byte, error) {
	return json.Marshal(d)
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
