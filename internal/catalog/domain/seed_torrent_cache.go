package domain

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// SeedTorrentCache bounds. The payload bound mirrors the migration CHECK and
// the reader's default fetch limit, so a cached row can always be written back
// without a size disagreement.
const (
	MaxSeedTorrentPayloadBytes = 20971520
	MaxSeedTorrentCacheLocator = 2048
)

// SeedTorrentCache is one locator's long-lived verified torrent payload: the
// bytes, their digest and the parsed file projection. It deliberately holds
// content only. Targets, filters and verdicts are configuration products, so
// the read model recomputes them against the current configuration and a
// configuration change never invalidates this row.
//
// The locator is the cache key because a locator's content is expected to be
// stable; a redirect to different bytes is caught by the caller's
// strong-identity comparison before a row is written.
type SeedTorrentCache struct {
	Locator       string
	Payload       []byte
	PayloadDigest string
	Manifest      TorrentManifest
	FetchedAt     time.Time
}

// Validate rejects a row that cannot be an exact content fact: a digest that
// does not cover the bytes, a manifest that does not project them, or a value
// outside the range the database CHECK accepts.
func (c SeedTorrentCache) Validate() error {
	if locator := strings.TrimSpace(c.Locator); locator != c.Locator || locator == "" || len(locator) > MaxSeedTorrentCacheLocator {
		return errors.New("seed torrent cache locator is required and bounded")
	}
	if len(c.Payload) == 0 || len(c.Payload) > MaxSeedTorrentPayloadBytes {
		return errors.New("seed torrent cache payload is required and bounded")
	}
	if c.PayloadDigest != DigestBytes(c.Payload) {
		return errors.New("seed torrent cache payload digest does not cover its bytes")
	}
	if err := c.Manifest.Validate(); err != nil {
		return fmt.Errorf("seed torrent cache manifest: %w", err)
	}
	if c.Manifest.PayloadDigest != c.PayloadDigest {
		return errors.New("seed torrent cache manifest is not bound to the cached payload")
	}
	if c.FetchedAt.IsZero() {
		return errors.New("seed torrent cache fetch time is required")
	}
	return nil
}
