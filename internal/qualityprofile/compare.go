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
		if def.Valued() && len(item.Ranks) == 0 {
			result.Steps = append(result.Steps, Step{Key: item.Key, Result: StepSkipped})
			continue
		}
		a, b := levelOf(def, item, candidate), levelOf(def, item, current)
		step := Step{Key: item.Key, Candidate: a.label, Current: b.label, CandidateListed: a.listed, CurrentListed: b.listed, Result: StepEqual}
		// A ranked layer only compares when both sides have data for it (owner
		// ruling 2026-10-27): a release without AniBT tags, say, ties on the
		// AniBT layer and the next item decides. A value that is present but
		// not listed still ranks lowest.
		noData := def.Valued() && (len(candidate.Values[item.Key]) == 0 || len(current.Values[item.Key]) == 0)
		switch {
		case noData:
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
		index, ok := RankIndex(item.Ranks, matchValues(item.Key, have))
		if !ok {
			return level{value: 0, label: strings.Join(have, "+")}
		}
		return level{value: int64(len(item.Ranks) - index), label: strings.Join(item.Ranks[index], "+"), listed: true}
	}
}

// rtCompositeLanguages are the reltitle sub_lang codes that name two
// languages at once. A language comparison reads a release's languages as a
// set (owner ruling 2026-10-27: the release's languages intersected with the
// ranking), so a composite code also counts as each language it names: JPSC
// hits an SC entry and a JPN entry, and an entry written as JPSC still hits.
var rtCompositeLanguages = map[string][]string{
	"jpsc": {"JPN", "SC"},
	"jptc": {"JPN", "TC"},
}

// matchValues is the value set one item matches its ranking against: the
// frozen values, plus the languages a composite rt.sub_lang code names. The
// frozen values themselves stay native; only the match widens.
func matchValues(key string, have []string) []string {
	if key != KeyRtSubLang {
		return have
	}
	out := append([]string(nil), have...)
	for _, value := range have {
		out = append(out, rtCompositeLanguages[strings.ToLower(strings.TrimSpace(value))]...)
	}
	return out
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
