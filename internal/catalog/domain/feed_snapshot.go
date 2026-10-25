package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	acquisitiondomain "bansoko/internal/acquisition/domain"
)

const (
	MaxProviderFeedSnapshotSeeds   = 10000
	MaxProviderFeedPayloadBytes    = 20 << 20
	MaxProviderFeedLocatorLength   = 2048
	MaxProviderFeedTitleLength     = 512
	MaxProviderFeedStableKeyLength = 256
	MaxProviderFeedSizeBytes       = 1 << 50
)

type FeedSnapshotFreshness string

const (
	FeedSnapshotFresh   FeedSnapshotFreshness = "fresh"
	FeedSnapshotStale   FeedSnapshotFreshness = "stale"
	FeedSnapshotMissing FeedSnapshotFreshness = "missing"
)

type ProviderRSSPreviewIdentity struct {
	SnapshotID      string
	SnapshotVersion int64
	ContentHash     string
}

// Exact-identity read outcomes. They are sentinels so callers can distinguish a
// refreshable data-freshness condition from an infrastructure failure and fail
// closed instead of silently falling back to the latest snapshot.
var (
	ErrProviderFeedSnapshotNotFound         = errors.New("provider feed snapshot identity was not found")
	ErrProviderFeedSnapshotIdentityMismatch = errors.New("provider feed snapshot identity does not match the persisted snapshot")
	ErrProviderFeedSnapshotStale            = errors.New("provider feed snapshot is stale")
)

// ValidateIdentityShape rejects an unusable identity before any read.
func (i ProviderRSSPreviewIdentity) ValidateIdentityShape() error {
	if strings.TrimSpace(i.SnapshotID) == "" || len(i.SnapshotID) > 128 {
		return errors.New("feed snapshot id is required and bounded")
	}
	if i.SnapshotVersion < 1 {
		return errors.New("feed snapshot version must be >= 1")
	}
	if err := validateSHA256Digest(i.ContentHash); err != nil {
		return fmt.Errorf("feed snapshot content hash: %w", err)
	}
	return nil
}

// VerifyIdentity checks one persisted snapshot against the exact identity the
// caller supplied. A mismatch is never silently accepted.
func (s ProviderFeedSnapshot) VerifyIdentity(identity ProviderRSSPreviewIdentity) error {
	if s.ID != identity.SnapshotID || s.Version != identity.SnapshotVersion || s.ContentHash != identity.ContentHash {
		return fmt.Errorf("%w: persisted snapshot %s v%d does not match requested %s v%d", ErrProviderFeedSnapshotIdentityMismatch, s.ID, s.Version, identity.SnapshotID, identity.SnapshotVersion)
	}
	return nil
}

type ProviderFeedSnapshotSeed struct {
	StableSeedKey  string
	Locator        string
	LocatorDisplay string
	Title          string
	PublishedAt    *time.Time
	SizeBytes      *int64
	InfoHash       *string
	Payload        []byte
	PayloadDigest  *string
	// TorrentFilename is the torrent's own file/folder name parsed from the
	// persisted payload. It is the naming input a live fetch would have used,
	// so a saved preview can replay the same rendered target. It is not part of
	// the content hash: the payload digest already covers it.
	TorrentFilename *string
	// TorrentState is one of the TorrentAcquisition* values. A seed whose
	// torrent could not be acquired keeps its place in the group with an
	// explicit bounded reason instead of vanishing from the read model.
	TorrentState       string
	TorrentErrorCode   *string
	TorrentErrorReason *string
	// Manifest is the derived file projection of this seed's exact payload. It
	// is present exactly when TorrentState is available; its digest is part of
	// the snapshot's canonical content hash in schema v3.
	Manifest *TorrentManifest
	// AniBTTags is the AniBT structured tag set the feed item carried when the
	// snapshot was published, in the persisted (public) block shape the release
	// path already stores. The tags are namespaced XML fields, not title text,
	// so they can never be re-derived from a persisted seed; without this field
	// every surface rebuilt from a snapshot lost `anibt.*`. It is deliberately
	// OUTSIDE the content hash: provider-feed-snapshot-v1 is part of the hashed
	// payload and every append-only snapshot published before this field existed
	// carries none, so folding it in would fail closed on all of them. nil means
	// "no tag set" (non-AniBT provider, or a row written before the column
	// existed); a set observed with no tags is the non-nil block with no tags.
	AniBTTags *acquisitiondomain.StoredAniBTTags
}

type ProviderFeedSnapshotWrite struct {
	Provider         string
	ProviderEntryRef string
	ProviderGroupRef string
	FetchedAt        time.Time
	FreshUntil       time.Time
	ContentHash      string
	HashSchema       string
	// ProjectionSchema names the file-projection schema every available seed's
	// manifest was derived with. It is empty for a pre-v3 write.
	ProjectionSchema string
	Seeds            []ProviderFeedSnapshotSeed
}

type ProviderFeedSnapshot struct {
	ID               string
	Provider         string
	ProviderEntryRef string
	ProviderGroupRef string
	Version          int64
	FetchedAt        time.Time
	FreshUntil       time.Time
	ContentHash      string
	// HashSchema names the canonical content-hash schema the stored hash used;
	// it is always ProviderFeedSnapshotContentHashSchema.
	HashSchema string
	// ProjectionSchema names the file-projection schema the stored manifests
	// were derived with.
	ProjectionSchema string
	FreshnessState   FeedSnapshotFreshness
	Seeds            []ProviderFeedSnapshotSeed
}

func (s ProviderFeedSnapshot) FreshnessAt(now time.Time) FeedSnapshotFreshness {
	if s.ID == "" {
		return FeedSnapshotMissing
	}
	if s.FreshnessState != "" {
		return s.FreshnessState
	}
	if now.Before(s.FreshUntil) {
		return FeedSnapshotFresh
	}
	return FeedSnapshotStale
}

func (s ProviderFeedSnapshot) Identity() ProviderRSSPreviewIdentity {
	return ProviderRSSPreviewIdentity{SnapshotID: s.ID, SnapshotVersion: s.Version, ContentHash: s.ContentHash}
}

func validCustomSnapshotRef(value, prefix string) bool {
	if len(value) != len(prefix)+64 || !strings.HasPrefix(value, prefix) {
		return false
	}
	for _, c := range value[len(prefix):] {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

func (s ProviderFeedSnapshotWrite) Validate() error {
	if s.Provider != "anibt" && s.Provider != "mikan" && s.Provider != "custom" {
		return fmt.Errorf("unsupported feed snapshot provider %q", s.Provider)
	}
	if strings.TrimSpace(s.ProviderEntryRef) == "" || len(s.ProviderEntryRef) > 256 {
		return errors.New("provider feed snapshot entry reference is required and bounded")
	}
	if strings.TrimSpace(s.ProviderGroupRef) == "" || len(s.ProviderGroupRef) > 256 {
		return errors.New("provider feed snapshot group reference is required and bounded")
	}
	if s.Provider == "custom" {
		if !validCustomSnapshotRef(s.ProviderEntryRef, "custom-source:") || !validCustomSnapshotRef(s.ProviderGroupRef, "custom-group:") {
			return errors.New("custom snapshot requires exact hashed source/group scope")
		}
	}
	if s.FetchedAt.IsZero() || s.FreshUntil.Before(s.FetchedAt) {
		return errors.New("provider feed snapshot timestamps are invalid")
	}
	if err := validateSHA256Digest(s.ContentHash); err != nil {
		return fmt.Errorf("provider feed snapshot content hash: %w", err)
	}
	if s.HashSchema != ProviderFeedSnapshotContentHashSchema {
		return fmt.Errorf("provider feed snapshot hash schema must be %q", ProviderFeedSnapshotContentHashSchema)
	}
	if s.ProjectionSchema != TorrentManifestProjectionSchema {
		return fmt.Errorf("provider feed snapshot projection schema must be %q", TorrentManifestProjectionSchema)
	}
	if len(s.Seeds) > MaxProviderFeedSnapshotSeeds {
		return fmt.Errorf("provider feed snapshot contains too many seeds: %d", len(s.Seeds))
	}
	seen := make(map[string]struct{}, len(s.Seeds))
	for i, seed := range s.Seeds {
		if err := seed.Validate(); err != nil {
			return fmt.Errorf("provider feed snapshot seed %d: %w", i, err)
		}
		if _, exists := seen[seed.StableSeedKey]; exists {
			return fmt.Errorf("provider feed snapshot seed key is duplicated: %q", seed.StableSeedKey)
		}
		seen[seed.StableSeedKey] = struct{}{}
	}
	return nil
}

func (s ProviderFeedSnapshotSeed) Validate() error {
	if strings.TrimSpace(s.StableSeedKey) == "" || len(s.StableSeedKey) > MaxProviderFeedStableKeyLength {
		return errors.New("stable seed key is required and bounded")
	}
	if strings.TrimSpace(s.Locator) == "" || len(s.Locator) > MaxProviderFeedLocatorLength {
		return errors.New("seed locator is required and bounded")
	}
	if strings.TrimSpace(s.LocatorDisplay) == "" || len(s.LocatorDisplay) > MaxProviderFeedLocatorLength {
		return errors.New("seed locator display is required and bounded")
	}
	if strings.TrimSpace(s.Title) == "" || len(s.Title) > MaxProviderFeedTitleLength {
		return errors.New("seed title is required and bounded")
	}
	if s.SizeBytes != nil && (*s.SizeBytes < 0 || *s.SizeBytes > MaxProviderFeedSizeBytes) {
		return errors.New("seed size is outside the allowed bound")
	}
	if s.InfoHash != nil {
		if err := validateInfoHash(*s.InfoHash); err != nil {
			return fmt.Errorf("seed info hash: %w", err)
		}
	}
	if len(s.Payload) > MaxProviderFeedPayloadBytes {
		return fmt.Errorf("seed torrent payload exceeds %d bytes", MaxProviderFeedPayloadBytes)
	}
	if len(s.Payload) > 0 {
		if s.PayloadDigest == nil {
			return errors.New("seed torrent payload digest is required")
		}
		if err := validateSHA256Digest(*s.PayloadDigest); err != nil {
			return fmt.Errorf("seed torrent payload digest: %w", err)
		}
		if got := DigestBytes(s.Payload); got != *s.PayloadDigest {
			return errors.New("seed torrent payload digest does not match payload")
		}
	} else if s.PayloadDigest != nil {
		return errors.New("seed torrent payload digest requires payload bytes")
	}
	if s.TorrentFilename != nil {
		filename := strings.TrimSpace(*s.TorrentFilename)
		if filename == "" || len(filename) > MaxProviderFeedTitleLength {
			return errors.New("seed torrent filename must be non-empty and bounded")
		}
	}
	if err := s.validateAniBTTags(); err != nil {
		return err
	}
	return s.validateTorrentAcquisition()
}

// validateAniBTTags holds the frozen block to the extractor's own bounds: a
// tag is "dimension:value" with both sides non-empty, at most
// MaxAniBTTagValueBytes of value, no whitespace control characters, and at most
// MaxAniBTTags entries. The column's SQL CHECK enforces the same shape, so a
// value the extractor could not have produced is refused before publication
// rather than at the owner function.
func (s ProviderFeedSnapshotSeed) validateAniBTTags() error {
	if s.AniBTTags == nil {
		return nil
	}
	if len(s.AniBTTags.Tags) > acquisitiondomain.MaxAniBTTags {
		return fmt.Errorf("seed AniBT tag set exceeds %d tags", acquisitiondomain.MaxAniBTTags)
	}
	for _, tag := range s.AniBTTags.Tags {
		_, value, ok := acquisitiondomain.SplitTag(tag)
		if !ok {
			return fmt.Errorf("seed AniBT tag %q is not a dimension:value pair", tag)
		}
		if len(value) > acquisitiondomain.MaxAniBTTagValueBytes || strings.ContainsAny(value, "\r\n\t") {
			return errors.New("seed AniBT tag value is not bounded")
		}
	}
	return nil
}

// validateTorrentAcquisition keeps the acquisition state, its bounded reason
// and the derived manifest mutually consistent:
//
//   - available   → the manifest exists and is an exact projection of this
//     seed's payload digest;
//   - unavailable → a bounded error code explains it and no manifest exists;
//   - not_applicable → neither.
//
// An empty state is read as not_applicable so a caller that never opts into
// torrent acquisition keeps a valid seed.
//
// NormalizedTorrentState is the single reading of that rule: validation,
// persisted-snapshot verification and the canonical content hash must all see
// the same value, or one seed would hash differently before and after a round
// trip through a column whose default is the normalized state.
func (s ProviderFeedSnapshotSeed) NormalizedTorrentState() string {
	if s.TorrentState == "" {
		return TorrentAcquisitionNotApplicable
	}
	return s.TorrentState
}
func (s ProviderFeedSnapshotSeed) validateTorrentAcquisition() error {
	switch s.NormalizedTorrentState() {
	case TorrentAcquisitionAvailable:
		if s.Manifest == nil {
			return errors.New("an available torrent seed must carry its derived manifest")
		}
		if s.TorrentErrorCode != nil || s.TorrentErrorReason != nil {
			return errors.New("an available torrent seed must not carry an acquisition error")
		}
		if s.PayloadDigest == nil {
			return errors.New("an available torrent seed must carry its payload digest")
		}
		if s.Manifest.PayloadDigest != *s.PayloadDigest {
			return errors.New("the torrent manifest payload digest does not match its seed payload digest")
		}
		if err := s.Manifest.Validate(); err != nil {
			return fmt.Errorf("seed torrent manifest: %w", err)
		}
	case TorrentAcquisitionUnavailable:
		if s.Manifest != nil {
			return errors.New("an unavailable torrent seed must not carry a manifest")
		}
		if s.TorrentErrorCode == nil || strings.TrimSpace(*s.TorrentErrorCode) == "" {
			return errors.New("an unavailable torrent seed must carry a bounded error code")
		}
		if len(*s.TorrentErrorCode) > 64 {
			return errors.New("seed torrent error code is not bounded")
		}
		if s.TorrentErrorReason != nil && len(*s.TorrentErrorReason) > 512 {
			return errors.New("seed torrent error reason is not bounded")
		}
	case TorrentAcquisitionNotApplicable:
		if s.Manifest != nil {
			return errors.New("a seed without torrent acquisition must not carry a manifest")
		}
		if s.TorrentErrorCode != nil || s.TorrentErrorReason != nil {
			return errors.New("a seed without torrent acquisition must not carry an error")
		}
	default:
		return fmt.Errorf("unsupported torrent acquisition state %q", s.NormalizedTorrentState())
	}
	return nil
}

func validateSHA256Digest(value string) error {
	if !strings.HasPrefix(value, "sha256:") || len(value) != len("sha256:")+64 {
		return errors.New("digest must use sha256:<64 lowercase hex> format")
	}
	if _, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:")); err != nil {
		return errors.New("digest must contain lowercase hexadecimal bytes")
	}
	if value != strings.ToLower(value) {
		return errors.New("digest must contain lowercase hexadecimal bytes")
	}
	return nil
}

func validateInfoHash(value string) error {
	if value != strings.ToLower(value) {
		return errors.New("info hash must contain lowercase hexadecimal bytes")
	}
	if len(value) != 40 && len(value) != 64 {
		return errors.New("info hash must contain 40 or 64 hexadecimal characters")
	}
	if _, err := hex.DecodeString(value); err != nil {
		return errors.New("info hash must contain lowercase hexadecimal bytes")
	}
	return nil
}

func DigestBytes(payload []byte) string {
	digest := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(digest[:])
}

type canonicalFeedSeed struct {
	StableSeedKey    string  `json:"stable_seed_key"`
	Locator          string  `json:"locator"`
	Title            string  `json:"title"`
	PublishedAt      *string `json:"published_at,omitempty"`
	SizeBytes        *int64  `json:"size_bytes,omitempty"`
	InfoHash         *string `json:"info_hash,omitempty"`
	PayloadDigest    *string `json:"payload_digest,omitempty"`
	TorrentFilename  *string `json:"torrent_filename,omitempty"`
	TorrentState     string  `json:"torrent_state"`
	ProjectionDigest *string `json:"projection_digest,omitempty"`
}

// ProviderFeedSnapshotContentHashSchema is the canonical content-hash schema
// label. It is part of the hashed payload. The hash covers each seed's stable
// key, torrent filename, torrent acquisition state and manifest projection
// digest, so the snapshot's content identity covers the file tree the read
// model renders and not only the seed list that produced it.
const ProviderFeedSnapshotContentHashSchema = "provider-feed-snapshot-v1"

type canonicalFeedSnapshot struct {
	Schema string              `json:"schema"`
	Seeds  []canonicalFeedSeed `json:"seeds"`
}

func CanonicalFeedSnapshotContentHash(seeds []ProviderFeedSnapshotSeed) (string, error) {
	canonical := make([]canonicalFeedSeed, 0, len(seeds))
	for index, seed := range seeds {
		if err := seed.Validate(); err != nil {
			return "", fmt.Errorf("seed %d: %w", index, err)
		}
		var published *string
		if seed.PublishedAt != nil {
			value := seed.PublishedAt.UTC().Format(time.RFC3339Nano)
			published = &value
		}
		var filename *string
		if seed.TorrentFilename != nil {
			value := strings.TrimSpace(*seed.TorrentFilename)
			filename = &value
		}
		var projectionDigest *string
		if seed.Manifest != nil {
			value := seed.Manifest.ProjectionDigest
			projectionDigest = &value
		}
		canonical = append(canonical, canonicalFeedSeed{
			StableSeedKey: seed.StableSeedKey,
			Locator:       seed.Locator, Title: seed.Title, PublishedAt: published,
			SizeBytes: seed.SizeBytes, InfoHash: seed.InfoHash, PayloadDigest: seed.PayloadDigest,
			TorrentFilename:  filename,
			TorrentState:     seed.NormalizedTorrentState(),
			ProjectionDigest: projectionDigest,
		})
	}
	sort.SliceStable(canonical, func(i, j int) bool {
		left, _ := json.Marshal(canonical[i])
		right, _ := json.Marshal(canonical[j])
		return string(left) < string(right)
	})
	encoded, err := json.Marshal(canonicalFeedSnapshot{Schema: ProviderFeedSnapshotContentHashSchema, Seeds: canonical})
	if err != nil {
		return "", fmt.Errorf("marshal canonical feed seed facts: %w", err)
	}
	return DigestBytes(encoded), nil
}

// VerifyContentHash recomputes the canonical content hash over the seed set and
// fails closed on any disagreement. A snapshot whose stored hash or seed facts
// do not reproduce its declared identity is not replayable, and neither is one
// whose manifest no longer matches its payload bytes.
func (s ProviderFeedSnapshot) VerifyContentHash() error {
	recomputed, err := CanonicalFeedSnapshotContentHash(s.Seeds)
	if err != nil {
		return err
	}
	if recomputed != s.ContentHash {
		return fmt.Errorf("%w: recomputed content hash does not match the persisted snapshot", ErrProviderFeedSnapshotIdentityMismatch)
	}
	for index, seed := range s.Seeds {
		if seed.PayloadDigest == nil {
			continue
		}
		if len(seed.Payload) == 0 {
			return fmt.Errorf("%w: seed %d declares a payload digest without payload bytes", ErrProviderFeedSnapshotIdentityMismatch, index)
		}
		if got := DigestBytes(seed.Payload); got != *seed.PayloadDigest {
			return fmt.Errorf("%w: seed %d payload digest does not match its bytes", ErrProviderFeedSnapshotIdentityMismatch, index)
		}
	}
	return nil
}
