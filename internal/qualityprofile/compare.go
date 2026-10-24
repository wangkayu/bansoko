package qualityprofile

import (
	"math"
	"strconv"
	"strings"
)

// Subject is one side of a comparison: its frozen values, the subscription's
// score for its subgroup source (nil = no source or no score), and its size.
type Subject struct {
	Values map[string][]string
	Score  *int64
	Size   *int64
}

// Step results.
const (
	StepBetter  = "better"
	StepWorse   = "worse"
	StepEqual   = "equal"
	StepSkipped = "skipped"
)

// SizeStep is the key of the size tiebreak step.
const SizeStep = "size"

// Step is one comparison step's evidence. Candidate/Current are the matched
// entry (or the release's own values when nothing matched, empty when it has
// none); Listed reports whether a side matched an entry of the ranking.
type Step struct {
	Key             string `json:"key"`
	Candidate       string `json:"candidate,omitempty"`
	Current         string `json:"current,omitempty"`
	CandidateListed bool   `json:"candidate_listed,omitempty"`
	CurrentListed   bool   `json:"current_listed,omitempty"`
	Result          string `json:"result"`
}

// Result is a comparison verdict: Better when the candidate wins on an item
// or, all items equal, on a strictly larger size; Equal when nothing decides
// (the caller keeps the existing file).
type Result struct {
	Better bool
	Equal  bool
	// Decided is the key of the deciding step ("size" for the tiebreak,
	// empty when nothing decided).
	Decided string
	Steps   []Step
}

// level is one side's rank on one item (larger is better).
type level struct {
	value  int64
	label  string
	listed bool
}

// Compare compares the candidate with the current file item by item: the
// first item that ranks them differently decides and later items are not
// read. All items equal: the larger size wins when both are known; otherwise
// nothing decides and the existing file stays.
func Compare(candidate, current Subject, profile Profile) Result {
	result := Result{}
	for _, item := range profile.Items {
		def, ok := Lookup(item.Key)
		if !ok {
			continue
		}
		if def.Ranked() && len(item.Ranks) == 0 {
			result.Steps = append(result.Steps, Step{Key: item.Key, Result: StepSkipped})
			continue
		}
		a, b := levelOf(def, item, candidate), levelOf(def, item, current)
		step := Step{Key: item.Key, Candidate: a.label, Current: b.label, CandidateListed: a.listed, CurrentListed: b.listed, Result: StepEqual}
		switch {
		case a.value > b.value:
			step.Result = StepBetter
		case a.value < b.value:
			step.Result = StepWorse
		}
		result.Steps = append(result.Steps, step)
		if step.Result != StepEqual {
			result.Better = step.Result == StepBetter
			result.Decided = item.Key
			return result
		}
	}
	step := Step{Key: SizeStep, Candidate: sizeLabel(candidate.Size), Current: sizeLabel(current.Size), Result: StepEqual}
	if candidate.Size != nil && current.Size != nil && *candidate.Size != *current.Size {
		if *candidate.Size > *current.Size {
			step.Result = StepBetter
			result.Better = true
		} else {
			step.Result = StepWorse
		}
		result.Decided = SizeStep
	} else {
		result.Equal = true
	}
	result.Steps = append(result.Steps, step)
	return result
}

func levelOf(def ItemDef, item Item, subject Subject) level {
	switch def.Kind {
	case KindScore:
		if subject.Score == nil {
			return level{value: math.MinInt64}
		}
		return level{value: *subject.Score, label: strconv.FormatInt(*subject.Score, 10), listed: true}
	case KindRevision:
		values := subject.Values[item.Key]
		label := ""
		if len(values) > 0 {
			label = values[0]
		}
		return level{value: RevisionNumber(label), label: label, listed: label != ""}
	default:
		have := subject.Values[item.Key]
		index, ok := RankIndex(item.Ranks, have)
		if !ok {
			return level{value: 0, label: strings.Join(have, "+")}
		}
		return level{value: int64(len(item.Ranks) - index), label: strings.Join(item.Ranks[index], "+"), listed: true}
	}
}

// RankIndex returns the best-ranked entry the release's values contain: an
// entry hits when every one of its values is among the release's values
// (order- and case-insensitive). No hit (unlisted or no values) is false.
func RankIndex(ranks [][]string, have []string) (int, bool) {
	if len(have) == 0 {
		return 0, false
	}
	set := make(map[string]bool, len(have))
	for _, value := range have {
		set[strings.ToLower(strings.TrimSpace(value))] = true
	}
	for index, entry := range ranks {
		if len(entry) == 0 {
			continue
		}
		hit := true
		for _, value := range entry {
			if !set[strings.ToLower(strings.TrimSpace(value))] {
				hit = false
				break
			}
		}
		if hit {
			return index, true
		}
	}
	return 0, false
}

func sizeLabel(size *int64) string {
	if size == nil {
		return ""
	}
	return strconv.FormatInt(*size, 10)
}
