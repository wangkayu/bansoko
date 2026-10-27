package batchplan

import (
	"path/filepath"
	"strconv"
	"strings"
)

// ReasonTargetReplacedInRelease marks a video of a manual one-shot selection
// that a later file of the same episode in the same release replaces under the
// overwrite collision policy.
const ReasonTargetReplacedInRelease = "target_replaced_in_release"

// SameEpisodeTarget is the decision for one member of a group of videos of one
// release that render one target path and cover the same episode.
type SameEpisodeTarget struct {
	// Keep is whether the member is placed.
	Keep bool
	// Version is the version ordinal of a kept member's target (0: the rendered
	// target itself; 2 and up: VersionedTargetPath of it).
	Version int
	// Reason is the drop reason of a member that is not kept.
	Reason string
}

// SameEpisodeTargets decides a group of n videos of one release that render one
// target and cover the same episode, in source path order.
//
// Without a manual file selection the first keeps the target and every later
// one is target_duplicate, whatever the collision policy (a v2 or a second
// encode of one episode in a subscription release).
//
// Under a manual one-shot selection (owner 10-27) the user chose every member,
// so the global collision policy decides, as if the members landed one after
// another in source path order:
//   - fail (跳过): the first lands and every later one is target_duplicate;
//   - versioned (加版本号): every member lands, the k-th (k >= 2) under the
//     versioned name the library uses for an occupied target;
//   - overwrite (覆盖): each later member replaces the earlier one, so only the
//     last lands and every earlier one is target_replaced_in_release. Nothing
//     of the release is written and then removed, and no file of the release is
//     ever displaced by the release itself.
//
// The decision is made when the targets are planned, because one release
// declares each target path once; the library's own collision handling still
// applies to every kept target against what already exists on disk.
func SameEpisodeTargets(policy string, n int, manual bool) []SameEpisodeTarget {
	out := make([]SameEpisodeTarget, n)
	if n == 0 {
		return out
	}
	switch {
	case manual && isVersionPolicy(policy):
		for index := range out {
			out[index] = SameEpisodeTarget{Keep: true}
			if index > 0 {
				out[index].Version = index + 1
			}
		}
	case manual && strings.EqualFold(strings.TrimSpace(policy), "overwrite"):
		for index := range out {
			out[index] = SameEpisodeTarget{Reason: ReasonTargetReplacedInRelease}
		}
		out[n-1] = SameEpisodeTarget{Keep: true}
	default:
		out[0] = SameEpisodeTarget{Keep: true}
		for index := 1; index < n; index++ {
			out[index] = SameEpisodeTarget{Reason: ReasonTargetDuplicate}
		}
	}
	return out
}

func isVersionPolicy(policy string) bool {
	switch strings.ToLower(strings.TrimSpace(policy)) {
	case "version", "versioned":
		return true
	}
	return false
}

// VersionedTargetPath is the versioned name of an occupied target, the same
// form the library publishes under the versioned policy: "<base> (<n>)<ext>".
func VersionedTargetPath(target string, version int) string {
	if version < 2 {
		return target
	}
	extension := filepath.Ext(target)
	return strings.TrimSuffix(target, extension) + " (" + strconv.Itoa(version) + ")" + extension
}
