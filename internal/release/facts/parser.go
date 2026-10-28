// Package facts owns the versioned release-derived candidate facts contract:
// a unified, deterministic projection producing quality dimensions plus
// canonical episode coverage keys, and a durable idempotent repository. It is
// admission-neutral — no winner/cutoff/rank decisions live here; downstream
// FileMaintenance consumes only the comparable facts and coverage.
//
// Candidate coverage is never re-derived from a title here. The frozen
// BatchPlan coverage keys (every planned video's own filename identity) are the
// one coverage authority of the automatic path; CandidateFromCoverage is the
// only builder of a candidate's coverage, and it merely projects them.
// The legacy read-time title parse was removed with the parser-selection
// convergence: an episode identity must be produced once, under the user's
// explicit parser selection, and then frozen.
package facts

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"bansoko/internal/acquisition/domain"
	"bansoko/internal/attrrules"
	"bansoko/internal/episodeidentity"
)

// ParserVersion is retained as the parser family identifier for callers that
// do not have an active profile available. Persisted facts should use the
// profile-specific version returned by Candidate.ParserVersion.
const ParserVersion = "candidate-facts-v1"

// CoverageUnresolved marks a candidate whose episode coverage cannot be
// proven; such candidates are never eligible for automatic replacement.
const CoverageUnresolved = "unresolved"

// Candidate is the parsed, comparable fact set for one Release.
type Candidate struct {
	ParserVersion string
	Title         string
	MediaKind     string // video | movie
	Resolution    string
	// Revision is the release revision marker ("v2"); empty = none (v1).
	Revision           string
	Quality            string
	Language           string
	Group              string
	Special            bool
	Episode            string
	Coverage           []string // canonical, deduped, sorted coverage keys
	CoverageUnresolved bool
	UnresolvedReason   string
	Warnings           []string
}

// CandidateFromCoverage projects the frozen placeable coverage of a Release
// (its BatchPlan coverage keys: one key per planned video, deduplicated and
// sorted) into the comparable candidate facts contract. It never parses a
// title for an episode: the keys are the coverage authority, so candidate
// facts cannot disagree with the plan the Release was accepted with. No key is
// unresolved coverage. Quality dimensions still come from ParseReleaseFacts,
// which never decides episode identity.
func CandidateFromCoverage(title string, rules attrrules.Rules, keys []string) Candidate {
	candidate := Candidate{
		ParserVersion: attrrules.ProfileVersion(mustFingerprint(rules)),
		Title:         strings.TrimSpace(title),
		MediaKind:     "video",
	}
	base := domain.ParseReleaseFacts(candidate.Title, rules)
	candidate.Resolution = base.Resolution
	candidate.Revision = base.Revision
	candidate.Quality = base.Quality
	candidate.Language = base.Language
	candidate.Group = base.GroupName
	candidate.Special = base.Special
	candidate.Warnings = append(candidate.Warnings, base.Warnings...)

	coverage := make([]string, 0, len(keys))
	seen := map[string]bool{}
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" || key == CoverageUnresolved || seen[key] {
			continue
		}
		seen[key] = true
		coverage = append(coverage, key)
	}
	sort.Strings(coverage)
	if len(coverage) == 0 {
		candidate.CoverageUnresolved = true
		candidate.UnresolvedReason = "no_placeable_coverage"
		candidate.Coverage = []string{CoverageUnresolved}
		return candidate
	}
	if len(coverage) == 1 && coverage[0] == "movie:0" {
		candidate.MediaKind = "movie"
	}
	if len(coverage) == 1 {
		if parsed, ok := episodeidentity.DecomposeCoverageKey(coverage[0]); ok {
			candidate.Episode = parsed.Episode
		}
	}
	candidate.Coverage = coverage
	return candidate
}

// InputFingerprint is the deterministic scan input identity.
func InputFingerprint(title string, seasonContext *int, isMovie bool) string {
	raw := fmt.Sprintf("%s|%v|%v", strings.TrimSpace(title), seasonContext, isMovie)
	digest := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(digest[:])
}

func mustFingerprint(r attrrules.Rules) string {
	fp, err := attrrules.Fingerprint(r)
	if err != nil {
		return "invalid"
	}
	return fp
}
