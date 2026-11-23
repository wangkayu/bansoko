package qualityprofile

import (
	"math"
	"strconv"
	"strings"
)

// Subject is one side of a comparison: its frozen values, the subscription's
// score for its subgroup source (nil = no source or no score), and its
// release-level size in bytes (nil = unknown).
type Subject struct {
	Values    map[string][]string
	Score     *int64
	SizeBytes *int64
}

// Step results.
const (
	StepBetter  = "better"
	StepWorse   = "worse"
	StepEqual   = "equal"
	StepSkipped = "skipped"
)

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
// only; Equal when nothing decides
// (the caller keeps the existing file).
type Result struct {
	Better bool
	Equal  bool
	// Decided is the configured item that decided, or empty for a tie.
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
// read. If all items tie, nothing decides and the existing file stays.
func Compare(candidate, current Subject, profile Profile) Result {
	result := Result{}
	for _, item := range profile.Items {
		def, ok := Lookup(item.Key)
		if !ok {
			continue
		}
		if (def.Ranked() || def.Kind == KindSize) && len(item.Ranks) == 0 {
			result.Steps = append(result.Steps, Step{Key: item.Key, Result: StepSkipped})
			continue
		}
		a, b := levelOf(def, item, candidate), levelOf(def, item, current)
		step := Step{Key: item.Key, Candidate: a.label, Current: b.label, CandidateListed: a.listed, CurrentListed: b.listed, Result: StepEqual}
		// A ranked layer only compares when both sides have data for it (owner
		// ruling 2026-10-27): a release without AniBT tags, say, ties on the
		// AniBT layer and the next item decides. A value that is present but
		// not listed still ranks lowest. For rt.sub_flag, listing "unknown"
		// opts missing values into ranking at that slot (e.g. soft > hard > unknown > ex).
		noData := false
		switch def.Kind {
		case KindRevision, KindSize:
			noData = !a.listed || !b.listed
		case KindSingle, KindMulti:
			candHave := effectiveValues(item, candidate.Values[item.Key])
			currHave := effectiveValues(item, current.Values[item.Key])
			noData = len(candHave) == 0 || len(currHave) == 0
		}
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
	result.Equal = true
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
		number, known := RevisionNumber(label)
		return level{value: number, label: label, listed: known}
	case KindSize:
		if subject.SizeBytes == nil || len(item.Ranks) == 0 || len(item.Ranks[0]) == 0 {
			return level{}
		}
		size := *subject.SizeBytes
		val := size
		if item.Ranks[0][0] == SizeSmaller {
			val = -size
		}
		return level{value: val, label: strconv.FormatInt(size, 10), listed: true}
	default:
		have := effectiveValues(item, subject.Values[item.Key])
		index, ok := RankIndex(item.Ranks, matchValues(item.Key, have))
		if !ok {
			return level{value: 0, label: strings.Join(have, "+")}
		}
		return level{value: int64(len(item.Ranks) - index), label: strings.Join(item.Ranks[index], "+"), listed: true}
	}
}

func effectiveValues(item Item, have []string) []string {
	if len(have) > 0 {
		return have
	}
	if item.Key == KeyRtSubFlag && ranksContainValue(item.Ranks, UnknownSubFlag) {
		return []string{UnknownSubFlag}
	}
	return nil
}

func ranksContainValue(ranks [][]string, target string) bool {
	target = strings.ToLower(strings.TrimSpace(target))
	for _, entry := range ranks {
		for _, value := range entry {
			if strings.ToLower(strings.TrimSpace(value)) == target {
				return true
			}
		}
	}
	return false
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
