// Package organizingpolicy contains pure organizing decisions. It neither
// filters inventory nor verifies filesystem sizes or performs external effects.
package organizingpolicy

import "sort"

const MovieAlgorithm = "movie-main-v1"

type FileRef string

// Video is a caller-classified eligible first-level video within one immutable
// manifest. Order is its stable manifest position (not the caller slice
// position). Size is known inventory evidence, not a substitute for actual
// Artifact verification.
type Video struct {
	Ref      FileRef
	Order    uint32
	Size     *uint64
	Eligible bool
}

type MovieDecision struct {
	Algorithm string
	Main      FileRef // empty when no eligible video exists; at most one target
	Reason    string
}

// SelectMovie requires a confirmed movie binding at the call site. It must not
// be used to infer media kind. The main video is the largest known size among
// the eligible videos; equal or unknown sizes resolve by manifest order. Every
// other eligible video is a plain unknown file for the caller: there are no
// name hints.
func SelectMovie(videos []Video) MovieDecision {
	d := MovieDecision{Algorithm: MovieAlgorithm, Reason: "no_applicable_video"}
	candidates := make([]Video, 0, len(videos))
	for _, v := range videos {
		if v.Eligible {
			candidates = append(candidates, v)
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Order != candidates[j].Order {
			return candidates[i].Order < candidates[j].Order
		}
		return candidates[i].Ref < candidates[j].Ref
	})
	if len(candidates) == 0 {
		return d
	}
	if len(candidates) == 1 {
		d.Main = candidates[0].Ref
		d.Reason = "unique_eligible_video"
		return d
	}
	winner := candidates[0]
	known := false
	for _, v := range candidates {
		if v.Size != nil && (!known || *v.Size > *winner.Size) {
			winner = v
			known = true
		}
	}
	d.Main = winner.Ref
	if known {
		d.Reason = "largest_known_size_stable_order"
	} else {
		d.Reason = "unknown_size_stable_order"
	}
	return d
}
