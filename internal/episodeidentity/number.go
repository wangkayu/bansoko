// Package episodeidentity owns the frozen episode identity contract: canonical
// episode numbers, typed evidence, and the pure resolver that turns one raw
// episode into an identity and applies the effective offset exactly once.
//
// This package is deliberately parser-agnostic. Parser and provider adapters
// produce EpisodeEvidence; the resolver normalizes, selects, applies the
// offset, and creates the coverage key. It never parses titles, reads files,
// queries metadata, or touches the database.
package episodeidentity

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// ResolverVersion identifies this resolver contract implementation.
const ResolverVersion = "episode-identity-v1"

// Canonical episode evidence syntax: non-negative ASCII decimal, optional
// fraction, no sign, no exponent, no leading/trailing zero noise. A fraction
// is still canonical evidence, so the resolver can see it and refuse it: an
// episode identity is only ever a non-negative integer.
var episodeNumberPattern = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?$`)

// EpisodeNumber is a canonical episode value such as "12" (or, as raw
// evidence only, "12.5"). Canonical form: no leading integer zeroes, no
// trailing fractional zeroes, no "." when the fraction is zero.
type EpisodeNumber string

// CanonicalEpisode normalizes a raw decimal string into an EpisodeNumber.
// It returns an error for empty, non-ASCII-decimal, negative, or otherwise
// malformed input.
func CanonicalEpisode(raw string) (EpisodeNumber, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", fmt.Errorf("episode number is empty")
	}
	if !episodeNumberPattern.MatchString(value) {
		return "", fmt.Errorf("episode number %q is not a non-negative decimal", raw)
	}
	integer, fraction, hasFraction := strings.Cut(value, ".")
	integer = strings.TrimLeft(integer, "0")
	if integer == "" {
		integer = "0"
	}
	if hasFraction {
		fraction = strings.TrimRight(fraction, "0")
		if fraction != "" {
			return EpisodeNumber(integer + "." + fraction), nil
		}
	}
	return EpisodeNumber(integer), nil
}

// IsFractional reports whether the value carries a fraction. A fractional
// value is evidence only; it is never an episode identity.
func (n EpisodeNumber) IsFractional() bool {
	return strings.Contains(string(n), ".")
}

// IsZero reports whether the episode is the canonical zero value.
func (n EpisodeNumber) IsZero() bool {
	return n == "0"
}

// AddOffset applies an integer offset to an integer episode and returns the
// canonical result. Offsets outside ±100000, a negative or out-of-bound
// result, and a fractional value with a non-zero offset are rejected; a
// fractional value with a zero offset is returned unchanged (raw evidence).
func (n EpisodeNumber) AddOffset(offset int) (EpisodeNumber, error) {
	if offset < -100000 || offset > 100000 {
		return "", fmt.Errorf("episode offset %d is outside the allowed bounds", offset)
	}
	if n == "" {
		return "", fmt.Errorf("episode number is empty")
	}
	if n.IsFractional() {
		if offset != 0 {
			return "", fmt.Errorf("fractional episode %q takes no offset", string(n))
		}
		return n, nil
	}
	value, err := strconv.Atoi(string(n))
	if err != nil {
		return "", fmt.Errorf("episode number %q is not an integer", string(n))
	}
	value += offset
	if value < 0 {
		return "", fmt.Errorf("episode after offset is negative")
	}
	if value > maxEpisode {
		return "", fmt.Errorf("episode after offset is outside the allowed bounds")
	}
	return EpisodeNumber(strconv.Itoa(value)), nil
}

// maxEpisode mirrors the historical parser bound (0..100000).
const maxEpisode = 100000

// InputDigest is the deterministic identity of one resolution input set.
func InputDigest(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x1f")))
	return hex.EncodeToString(sum[:])
}
