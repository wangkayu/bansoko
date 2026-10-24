package filemanager

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"sort"
	"strings"
)

// revisionOf is the opaque token of everything a write is admitted on: the
// entry's metadata (kind, size, modification time), its derived state, role
// and reason, the placement and companion rows at its path with their
// versions, the video's companion relations, and every unfinished owner that
// holds it. Any change between the read and the confirmed write changes the
// token, and the write is refused as changed.
func revisionOf(entry Entry, derived derivation, facts PathFacts, spellings []string, companions []CompanionFact) string {
	var lines []string
	add := func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) }
	add("entry|%s|%s", entry.Kind, entry.Path)
	if entry.SizeBytes != nil {
		add("size|%d", *entry.SizeBytes)
	}
	if entry.ModifiedAt != nil {
		add("mtime|%d", entry.ModifiedAt.UnixNano())
	}
	add("state|%s|%s|%s|%t|%t", entry.State, entry.Role, derived.reason, entry.NotAligned, entry.Locked)
	for _, variant := range spellings {
		for _, placement := range facts.Placements[variant] {
			add("placement|%s|%s|%d|%t", placement.ID, placement.State, placement.Version, placement.Retired)
		}
		for _, companion := range facts.Companions[variant] {
			add("companion|%s|%s|%d|%s|%s|%d", companion.ID, companion.State, companion.Version,
				companion.Placement.ID, companion.Placement.State, companion.Placement.Version)
		}
	}
	for _, owner := range derived.inProgress {
		add("owner|%s|%s|%s", owner.Kind, owner.AcquisitionID, owner.Path)
	}
	for _, companion := range companions {
		add("linked|%s|%s|%d|%s", companion.ID, companion.State, companion.Version, companion.Path)
	}
	sort.Strings(lines)
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return base64.RawURLEncoding.EncodeToString(sum[:18])
}
