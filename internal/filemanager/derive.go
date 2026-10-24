package filemanager

import (
	"sort"

	"bansoko/internal/filekinds"
	"bansoko/internal/filemaintenance"
)

// Placement and companion states as stored by their owners.
const (
	placementActive    = "active"
	placementReleased  = "released"
	placementDisplaced = "displaced"
	companionActive    = "active"
)

// derivation is the derived ownership of one file path.
type derivation struct {
	state      State
	role       Role
	reason     DetachedReason
	placement  *PlacementFact
	companion  *CompanionFact
	inProgress []InProgressFact
	notAligned bool
}

// placementState derives a video placement's state .
// Stopped subscriptions still count as managed; a retired (unsubscribed)
// anime, a deleted anime, a superseded placement and a one-shot's temporary
// anime all count as detached.
func placementState(placement PlacementFact) (State, DetachedReason) {
	switch {
	case placement.State == placementReleased:
		return StateDetached, DetachedReleased
	case placement.State == placementDisplaced && placement.DisplacedCause == "redownload":
		return StateDetached, DetachedRedownloaded
	case placement.State == placementDisplaced:
		return StateDetached, DetachedDisplaced
	case placement.State != placementActive:
		return StateDetached, DetachedReplaced
	case !placement.AnimeExists:
		return StateDetached, DetachedAnimeRemoved
	case placement.Retired:
		return StateDetached, DetachedSubscriptionEnded
	case placement.OneShotID != "":
		return StateDetached, DetachedOneShot
	default:
		return StateManaged, ""
	}
}

// pickPlacement prefers the active placement at a path; otherwise any
// earlier one (superseded), in a stable order.
func pickPlacement(placements []PlacementFact) *PlacementFact {
	if len(placements) == 0 {
		return nil
	}
	sorted := append([]PlacementFact(nil), placements...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if (sorted[i].State == placementActive) != (sorted[j].State == placementActive) {
			return sorted[i].State == placementActive
		}
		return sorted[i].ID < sorted[j].ID
	})
	return &sorted[0]
}

// pickCompanion prefers the active relation at a path (at most one exists).
func pickCompanion(companions []CompanionFact) *CompanionFact {
	if len(companions) == 0 {
		return nil
	}
	sorted := append([]CompanionFact(nil), companions...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if (sorted[i].State == companionActive) != (sorted[j].State == companionActive) {
			return sorted[i].State == companionActive
		}
		return sorted[i].ID < sorted[j].ID
	})
	return &sorted[0]
}

func companionRole(mediaKind string) Role {
	if mediaKind == "subtitle" {
		return RoleSubtitle
	}
	return RoleSidecar
}

// collect gathers the facts of every spelling of one path.
func collect[T any](facts map[string][]T, variants []string) []T {
	var result []T
	for _, variant := range variants {
		result = append(result, facts[variant]...)
	}
	return result
}

// deriveFile derives the state of one file path (first match
// wins):
//
//  1. in progress: a non-terminal owner names the path; a bound sidecar also
//     inherits its video's in-progress owners;
//  2. a placement at the path: its own state (managed, or detached for
//     released/superseded/retired/removed/one-shot);
//  3. a companion relation at the path: an active relation follows its video
//     placement's state exactly (subtitle-follows-video, no record of its
//     own); an ended relation is detached;
//  4. anything else is detached (no record).
//
// videoInProgress returns the in-progress facts of a video placement path.
func deriveFile(facts PathFacts, variants []string, videoInProgress func(path string) []InProgressFact) derivation {
	result := derivation{inProgress: collect(facts.InProgress, variants)}
	if placement := pickPlacement(collect(facts.Placements, variants)); placement != nil {
		result.placement = placement
		result.role = RoleVideo
		result.state, result.reason = placementState(*placement)
	} else if companion := pickCompanion(collect(facts.Companions, variants)); companion != nil {
		result.companion = companion
		result.role = companionRole(companion.MediaKind)
		video := companion.Placement
		result.placement = &video
		if companion.State != companionActive {
			result.state, result.reason = StateDetached, DetachedCompanionEnded
			if video.State == placementReleased {
				// Released with its video.
				result.reason = DetachedReleased
			}
		} else {
			result.state, result.reason = placementState(video)
			if videoInProgress != nil {
				result.inProgress = append(result.inProgress, videoInProgress(video.Path)...)
			}
			result.notAligned = companion.AlignmentFailed ||
				(video.Path != "" && filemaintenance.CompanionPath(video.Path, companion.Suffix, companion.Extension) != companion.Path)
		}
	} else {
		result.state, result.reason = StateDetached, DetachedNoRecord
	}
	if len(result.inProgress) > 0 {
		result.state, result.reason = StateInProgress, ""
	}
	return result
}

// deriveDirectory gives a directory the strongest state of what it holds: any
// in-progress owner below it makes it in progress, then any managed file. A
// directory operation would touch every file below it, so it must be judged
// by the most protected one.
func deriveDirectory(fact SubtreeFact) State {
	switch {
	case fact.InProgress:
		return StateInProgress
	case fact.Managed:
		return StateManaged
	default:
		return StateDetached
	}
}

// operationsFor applies the admission matrix to one file. Only
// applicable operations are listed. Upload, remote-mirror and
// reclassification rows belong to later phases and are not reported.
// alignmentEnabled is the effective file maintenance policy of the file's
// anime: alignment runs through the policy-gated consumer, so a disabled
// policy refuses it up front.
//
// match is the match rule of a detached or in-progress video (nil when a match
// does not apply to the file); it sits before delete.
func operationsFor(state State, role Role, alignmentEnabled bool, match *OperationRule) []OperationRule {
	companion := role == RoleSubtitle || role == RoleSidecar
	allow := func(operation Operation) OperationRule { return OperationRule{Operation: operation, Allowed: true} }
	block := func(operation Operation, reason OperationBlock) OperationRule {
		return OperationRule{Operation: operation, Block: reason}
	}
	release := func() OperationRule {
		if companion {
			// A sidecar is never released apart from its video.
			return block(OperationRelease, BlockFollowsVideo)
		}
		return allow(OperationRelease)
	}
	switch state {
	case StateInProgress:
		rules := []OperationRule{}
		for _, operation := range []Operation{OperationScan, OperationAlign, OperationCopy, OperationMove, OperationRename, OperationDelete, OperationRelease} {
			rules = append(rules, block(operation, BlockInProgress))
		}
		if match != nil {
			rules = append(rules, block(OperationMatch, BlockInProgress))
		}
		return rules
	case StateManaged:
		align := allow(OperationAlign)
		switch {
		case companion:
			// A sidecar aligns with its video, never on its own.
			align = block(OperationAlign, BlockFollowsVideo)
		case !alignmentEnabled:
			align = block(OperationAlign, BlockAlignmentDisabled)
		}
		return []OperationRule{
			allow(OperationScan), align, allow(OperationCopy),
			block(OperationMove, BlockManaged), block(OperationRename, BlockManaged), block(OperationDelete, BlockManaged),
			release(),
		}
	default:
		rules := []OperationRule{allow(OperationCopy), allow(OperationMove), allow(OperationRename)}
		if match != nil {
			rules = append(rules, *match)
		}
		return append(rules, allow(OperationDelete))
	}
}

// matchRuleFor decides the match rule of one file: it applies to a
// detached or in-progress video that is not a bound sidecar. A match may take
// its source from any browsable path; the target is decided by the work's own
// naming target, so the file's location does not restrict it.
func matchRuleFor(entry Entry, internal bool) *OperationRule {
	if entry.Kind != KindFile || internal || entry.State == StateManaged {
		return nil
	}
	if entry.Role != RoleNone && entry.Role != RoleVideo {
		return nil
	}
	if filekinds.Classify(filekinds.Version, entry.Name) != filekinds.KindVideo {
		return nil
	}
	return &OperationRule{Operation: OperationMatch, Allowed: true}
}
