package domain

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type ReleaseChannel string

const (
	ReleaseChannelStable  ReleaseChannel = "stable"
	ReleaseChannelBeta    ReleaseChannel = "beta"
	ReleaseChannelNightly ReleaseChannel = "nightly"
)

type RollbackClass string

const (
	CodeOnly        RollbackClass = "code_only"
	RestoreRequired RollbackClass = "restore_required"
	Unsupported     RollbackClass = "unsupported"
)

type MigrationRef struct {
	ID            string `json:"id"`
	Checksum      string `json:"checksum"`
	Reversible    bool   `json:"reversible"`
	Compatibility string `json:"compatibility"`
}

type ArtifactRef struct {
	Platform     string   `json:"platform"`
	Architecture string   `json:"architecture"`
	Digest       string   `json:"digest"`
	Size         int64    `json:"size"`
	Signature    string   `json:"signature"`
	Platforms    []string `json:"platforms,omitempty"`
}

// ImageRef is a digest-pinned container image in the production bundle.
type ImageRef struct {
	Name     string `json:"name"`
	Digest   string `json:"digest"`
	Platform string `json:"platform,omitempty"`
}

// EmbeddedUpdateFormatV1 identifies the container-embedded self-update
// payload format produced by build-release.sh and consumed by the future
// embedded bootstrap. It is deliberately a distinct signing surface from the
// outer release manifest.
const EmbeddedUpdateFormatV1 = "embedded-update-v1"

// EmbeddedUpdateSegment describes the signed overlay payload shipped next to
// an immutable base image. The payload digest is always the bare 64-hex
// sha256 form because it feeds directly into SignArtifactDigest /
// VerifyArtifactDigestSignature; registry image digests keep their
// conventional "sha256:" prefix elsewhere in the manifest.
type EmbeddedUpdateSegment struct {
	FormatVersion       string `json:"format_version"`
	Platform            string `json:"platform"`
	Architecture        string `json:"architecture"`
	PayloadDigest       string `json:"payload_digest"`
	PayloadSize         int64  `json:"payload_size"`
	PayloadSignature    string `json:"payload_signature"`
	BootstrapMinVersion string `json:"bootstrap_min_version"`
	MinImageVersion     string `json:"min_image_version,omitempty"`
	TargetReleaseID     string `json:"target_release_id"`
	TargetVersion       string `json:"target_version"`
	TargetSchemaVersion string `json:"target_schema_version"`
	ReleaseSequence     int64  `json:"release_sequence"`
}

type ReleaseManifest struct {
	SchemaVersion          string         `json:"schema_version"`
	ReleaseSequence        int64          `json:"release_sequence,omitempty"`
	ReleaseID              string         `json:"release_id,omitempty"`
	Version                string         `json:"version"`
	Channel                ReleaseChannel `json:"channel,omitempty"`
	PublishedAt            time.Time      `json:"published_at,omitempty"`
	SourceCommit           string         `json:"source_commit,omitempty"`
	APIContractVersion     string         `json:"api_contract_version,omitempty"`
	TargetMigrationVersion string         `json:"target_migration_version,omitempty"`
	CompatibleFromVersions []string       `json:"compatible_from_versions,omitempty"`
	RequiredDowntime       bool           `json:"required_downtime"`
	BackupRequired         bool           `json:"backup_required"`
	Rollback               RollbackClass  `json:"rollback_class"`
	Migrations             []MigrationRef `json:"migrations,omitempty"`
	Images                 []ImageRef     `json:"images,omitempty"`
	ComposeRef             string         `json:"compose_ref,omitempty"`
	FrontendRef            string         `json:"frontend_ref,omitempty"`
	Artifacts              []ArtifactRef  `json:"artifacts,omitempty"`
	SBOMRef                string         `json:"sbom_ref,omitempty"`
	ProvenanceRef          string         `json:"provenance_ref,omitempty"`
	ArtifactDigest         string         `json:"artifact_digest,omitempty"`
	Signature              string         `json:"signature"`
	Immutable              bool           `json:"immutable"`

	// EmbeddedUpdate is optional for backward compatibility: historical
	// bundles signed before the embedded self-update program have no such
	// segment and must keep verifying (and byte-wise re-canonicalizing)
	// unchanged. ValidateForEmbeddedUpdate enforces it strictly for bundles
	// that declare an embedded payload.
	EmbeddedUpdate *EmbeddedUpdateSegment `json:"embedded_update,omitempty"`

	// ReleaseNotes is optional: test builds and manifests signed before the
	// formal release lane carry none, and omitempty keeps their canonical
	// bytes unchanged. A formal release carries its own bilingual notes first,
	// then those of earlier versions (see ValidateReleaseNotes).
	ReleaseNotes []ReleaseNotesEntry `json:"release_notes,omitempty"`
}

func (m ReleaseManifest) Verify() error {
	if strings.TrimSpace(m.Version) == "" || strings.TrimSpace(m.ArtifactDigest) == "" || strings.TrimSpace(m.Signature) == "" || strings.TrimSpace(m.SchemaVersion) == "" {
		return errors.New("release manifest fields are required")
	}
	if !m.Immutable {
		return errors.New("release manifest must be immutable")
	}
	if len(m.ArtifactDigest) < 16 {
		return errors.New("release artifact digest is too short")
	}
	return nil
}

// ValidateForRelease enforces the strict identity contract used by release
// promotion and the UpdateSupervisor. Verify remains intentionally compatible
// with the small foundation value used by early S1 tests.
func (m ReleaseManifest) ValidateForRelease() error {
	if strings.TrimSpace(m.Version) == "" || strings.TrimSpace(m.SchemaVersion) == "" || !m.Immutable {
		return errors.New("release manifest version, schema and immutable identity are required")
	}
	if strings.TrimSpace(m.ReleaseID) == "" || strings.TrimSpace(m.SourceCommit) == "" || strings.TrimSpace(m.APIContractVersion) == "" {
		return errors.New("release identity fields are required")
	}
	switch m.Channel {
	case ReleaseChannelStable, ReleaseChannelBeta, ReleaseChannelNightly:
	default:
		return fmt.Errorf("unsupported release channel %q", m.Channel)
	}
	switch m.Rollback {
	case CodeOnly, RestoreRequired, Unsupported:
	default:
		return fmt.Errorf("unsupported rollback class %q", m.Rollback)
	}
	if len(m.Artifacts) == 0 {
		return errors.New("release manifest must contain an artifact")
	}
	for _, artifact := range m.Artifacts {
		if strings.TrimSpace(artifact.Platform) == "" || strings.TrimSpace(artifact.Architecture) == "" || !isSHA256(artifact.Digest) || strings.TrimSpace(artifact.Signature) == "" || artifact.Size < 0 {
			return errors.New("release artifacts require platform, architecture, sha256 digest and non-negative size")
		}
	}
	if strings.TrimSpace(m.SBOMRef) == "" || strings.TrimSpace(m.ProvenanceRef) == "" {
		return errors.New("release manifest requires SBOM and provenance references")
	}
	if !isSHA256(m.ArtifactDigest) {
		return errors.New("release manifest artifact digest must be a sha256 digest")
	}
	if err := ValidateReleaseNotes(m.Version, m.ReleaseNotes); err != nil {
		return err
	}
	return nil
}

// ValidateForEmbeddedUpdate enforces the full embedded self-update contract on
// top of ValidateForRelease: a publisher-owned monotonic release sequence in
// the signed byte set plus a strictly consistent payload segment whose target
// identity is bound to the outer manifest.
func (m ReleaseManifest) ValidateForEmbeddedUpdate() error {
	if err := m.ValidateForRelease(); err != nil {
		return err
	}
	if m.ReleaseSequence < 1 {
		return errors.New("embedded update manifests require a positive release_sequence")
	}
	payload := m.EmbeddedUpdate
	if payload == nil {
		return errors.New("embedded update manifests require the embedded_update segment")
	}
	if payload.FormatVersion != EmbeddedUpdateFormatV1 {
		return fmt.Errorf("unsupported embedded update format %q", payload.FormatVersion)
	}
	if strings.TrimSpace(payload.Platform) == "" || strings.TrimSpace(payload.Architecture) == "" ||
		strings.ContainsAny(payload.Platform, " \t/") || strings.ContainsAny(payload.Architecture, " \t/") {
		return errors.New("embedded update payload requires platform and architecture identifiers")
	}
	if !isSHA256(payload.PayloadDigest) {
		return errors.New("embedded update payload digest must be a bare 64-hex sha256 digest")
	}
	if payload.PayloadSize <= 0 {
		return errors.New("embedded update payload size must be positive")
	}
	if strings.TrimSpace(payload.PayloadSignature) == "" {
		return errors.New("embedded update payload signature is required")
	}
	if strings.TrimSpace(payload.BootstrapMinVersion) == "" || strings.TrimSpace(payload.MinImageVersion) == "" {
		return errors.New("embedded update payload requires bootstrap and image minimum versions")
	}
	if _, err := m.EffectiveMinImageVersion(); err != nil {
		return err
	}
	if payload.TargetReleaseID != m.ReleaseID || payload.TargetVersion != m.Version || payload.TargetSchemaVersion != m.TargetMigrationVersion {
		return errors.New("embedded update target identity does not match the outer manifest")
	}
	if payload.ReleaseSequence != m.ReleaseSequence {
		return errors.New("embedded update segment release sequence does not match the outer manifest")
	}
	return nil
}

// VerifyEmbeddedPayloadSignature checks the payload signature against the
// declared digest using the standard artifact-digest signing surface.
func (m ReleaseManifest) VerifyEmbeddedPayloadSignature(publicKey ed25519.PublicKey) error {
	if err := m.ValidateForEmbeddedUpdate(); err != nil {
		return err
	}
	return VerifyArtifactDigestSignature(publicKey, m.EmbeddedUpdate.PayloadDigest, m.EmbeddedUpdate.PayloadSignature)
}

// EffectiveBootstrapMinVersion returns the bootstrap floor the payload requires
// from the base image. It mirrors EffectiveMinImageVersion so both immutable
// base-image floors are read through one explicit shape instead of reaching
// into the segment.
func (m ReleaseManifest) EffectiveBootstrapMinVersion() (string, error) {
	if m.EmbeddedUpdate == nil {
		return "", errors.New("embedded update payload is required")
	}
	value := m.EmbeddedUpdate.BootstrapMinVersion
	if value == "" {
		return "", errors.New("embedded update payload requires a bootstrap minimum version")
	}
	return value, nil
}

// EffectiveMinImageVersion returns the image compatibility floor the payload
// requires from the base image.
func (m ReleaseManifest) EffectiveMinImageVersion() (string, error) {
	if m.EmbeddedUpdate == nil {
		return "", errors.New("embedded update payload is required")
	}
	if current := strings.TrimSpace(m.EmbeddedUpdate.MinImageVersion); current != "" {
		return current, nil
	}
	return "", errors.New("embedded update payload requires an image minimum version")
}

func (m ReleaseManifest) signingCopy() ReleaseManifest {
	m.Signature = ""
	return m
}

func (m ReleaseManifest) CanonicalBytes() ([]byte, error) {
	return json.Marshal(m.signingCopy())
}

func (m ReleaseManifest) CanonicalDigest() (string, error) {
	data, err := m.CanonicalBytes()
	if err != nil {
		return "", err
	}
	return HashArtifact(data), nil
}

func (m *ReleaseManifest) Sign(privateKey ed25519.PrivateKey) error {
	if m == nil || len(privateKey) != ed25519.PrivateKeySize {
		return errors.New("ed25519 private key is required")
	}
	if err := m.ValidateForRelease(); err != nil {
		return err
	}
	data, err := m.CanonicalBytes()
	if err != nil {
		return fmt.Errorf("canonicalize release manifest: %w", err)
	}
	m.Signature = base64.RawStdEncoding.EncodeToString(ed25519.Sign(privateKey, data))
	return nil
}

func (m ReleaseManifest) VerifySignature(publicKey ed25519.PublicKey) error {
	if len(publicKey) != ed25519.PublicKeySize {
		return errors.New("ed25519 public key is required")
	}
	if err := m.ValidateForRelease(); err != nil {
		return err
	}
	if strings.TrimSpace(m.Signature) == "" {
		return errors.New("release manifest signature is required")
	}
	signature, err := base64.RawStdEncoding.DecodeString(m.Signature)
	if err != nil || len(signature) != ed25519.SignatureSize {
		return errors.New("release manifest signature is invalid")
	}
	data, err := m.CanonicalBytes()
	if err != nil {
		return fmt.Errorf("canonicalize release manifest: %w", err)
	}
	if !ed25519.Verify(publicKey, data, signature) {
		return errors.New("release manifest signature verification failed")
	}
	return nil
}

func GenerateSigningKey() (ed25519.PublicKey, ed25519.PrivateKey, error) {
	return ed25519.GenerateKey(rand.Reader)
}

func isSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

type UpdatePlan struct {
	Manifest         ReleaseManifest
	Rollback         RollbackClass
	BackupVerified   bool
	Maintenance      bool
	BackupArtifactID string
	InstanceID       string
	OperationID      string
	IdempotencyKey   string
}

func (p UpdatePlan) Admit() error {
	if err := p.Manifest.Verify(); err != nil {
		return err
	}
	if !p.BackupVerified || !p.Maintenance {
		return errors.New("update requires verified backup and maintenance admission")
	}
	if p.Rollback != CodeOnly && p.Rollback != RestoreRequired && p.Rollback != Unsupported {
		return errors.New("unsupported rollback class")
	}
	return nil
}

func HashArtifact(payload []byte) string {
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

func SignArtifactDigest(privateKey ed25519.PrivateKey, digest string) (string, error) {
	if len(privateKey) != ed25519.PrivateKeySize {
		return "", errors.New("ed25519 private key is required")
	}
	if !isSHA256(digest) {
		return "", errors.New("artifact digest must be a sha256 digest")
	}
	return base64.RawStdEncoding.EncodeToString(ed25519.Sign(privateKey, []byte("sha256:"+digest))), nil
}

func VerifyArtifactDigestSignature(publicKey ed25519.PublicKey, digest, signature string) error {
	if len(publicKey) != ed25519.PublicKeySize {
		return errors.New("ed25519 public key is required")
	}
	if !isSHA256(digest) {
		return errors.New("artifact digest must be a sha256 digest")
	}
	decoded, err := base64.RawStdEncoding.DecodeString(signature)
	if err != nil || len(decoded) != ed25519.SignatureSize {
		return errors.New("artifact signature is invalid")
	}
	if !ed25519.Verify(publicKey, []byte("sha256:"+digest), decoded) {
		return errors.New("artifact signature verification failed")
	}
	return nil
}
