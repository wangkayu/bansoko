package domain

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	providerdomain "bansoko/internal/provider/domain"
)

type SelectionMode string

const (
	Always SelectionMode = "always"
	Ranked SelectionMode = "ranked"
	Never  SelectionMode = "never"
)

type PolicyTargetKind string

const (
	TargetCanonicalGroup PolicyTargetKind = "canonical_group"
	TargetProviderGroup  PolicyTargetKind = "provider_group"
)

type PolicyTarget struct {
	Kind         PolicyTargetKind
	CanonicalKey string
	ProviderRef  *providerdomain.GroupRef
}

func (t PolicyTarget) Validate() error {
	switch t.Kind {
	case TargetCanonicalGroup:
		if strings.TrimSpace(t.CanonicalKey) == "" || t.ProviderRef != nil {
			return errors.New("canonical group target requires only a canonical key")
		}
	case TargetProviderGroup:
		if t.ProviderRef == nil || strings.TrimSpace(t.CanonicalKey) != "" {
			return errors.New("provider group target requires only a provider group reference")
		}
		return t.ProviderRef.Validate()
	default:
		return fmt.Errorf("unsupported group policy target %q", t.Kind)
	}
	return nil
}

type ProviderGroupPolicy struct {
	Target       PolicyTarget
	Mode         SelectionMode
	Score        *int
	CountInLimit bool
	Version      int64
}

func ValidateProviderGroupPolicy(policy ProviderGroupPolicy) error {
	if err := policy.Target.Validate(); err != nil {
		return err
	}
	switch policy.Mode {
	case Always:
		if policy.Score != nil {
			return errors.New("always group policy cannot define a score")
		}
	case Ranked:
		if policy.Score == nil {
			return errors.New("ranked group policy requires a score")
		}
		if *policy.Score < 1 || *policy.Score > 100 {
			return errors.New("ranked group policy score must be between 1 and 100")
		}
		if !policy.CountInLimit {
			return errors.New("ranked group policy must count in max_rss")
		}
	case Never:
		if policy.Score != nil || policy.CountInLimit {
			return errors.New("never group policy cannot define score or consume max_rss")
		}
	default:
		return fmt.Errorf("unsupported group policy mode %q", policy.Mode)
	}
	return nil
}

type EffectivePolicySource string

const (
	SourceAnimeExact      EffectivePolicySource = "anime_exact"
	SourceAnimeCanonical  EffectivePolicySource = "anime_canonical"
	SourceProviderDefault EffectivePolicySource = "provider_default"
)

type EffectiveProviderGroup struct {
	Ref           providerdomain.GroupRef
	CanonicalKey  string
	Observed      bool
	Mode          SelectionMode
	Score         int
	CountInLimit  bool
	PolicySource  EffectivePolicySource
	PolicyVersion int64
	// Breakdown explains Score and Mode: the site default, the site list
	// effect, each matched name keyword and any per-anime manual override. It
	// is produced by the resolution itself, so a reader never recomputes it.
	// Nil only for a frozen per-entry score rule, which has no site layer.
	Breakdown *ScoreBreakdown
}

// SiteListMode is how a provider's site-level group list is read: blacklist
// ranks every group except the listed ones, whitelist ranks only the listed
// ones.
type SiteListMode string

const (
	SiteBlacklist SiteListMode = "blacklist"
	SiteWhitelist SiteListMode = "whitelist"
)

// SiteListEffect is what the site list did to one group.
type SiteListEffect string

const (
	ListEffectNone           SiteListEffect = "none"
	ListEffectWhitelisted    SiteListEffect = "whitelisted"
	ListEffectBlacklisted    SiteListEffect = "blacklisted"
	ListEffectNotWhitelisted SiteListEffect = "not_whitelisted"
)

// EntryOverride is the per-anime manual intent that decided a group.
type EntryOverride string

const (
	EntryOverrideNone   EntryOverride = "none"
	EntryOverrideAlways EntryOverride = "always"
	EntryOverrideNever  EntryOverride = "never"
)

// KeywordContribution is one matched name keyword and the score it added.
type KeywordContribution struct {
	Keyword string
	Score   int
}

// ScoreBreakdown is how one group's score and mode were decided. For a ranked
// group FinalScore = PinnedScore when the site pins the group (Keywords is then
// empty: no keyword applied), otherwise SiteDefaultScore + the sum of
// Keywords; an excluded or manually decided group has no FinalScore and no
// PinnedScore.
type ScoreBreakdown struct {
	SiteMode         SiteListMode
	SiteDefaultScore int
	ListEffect       SiteListEffect
	Keywords         []KeywordContribution
	EntryOverride    EntryOverride
	PinnedScore      *int
	FinalScore       *int
}

// ProviderDefaultPolicy is one provider's site-level policy. ListMode and
// Listed carry the live site list; a policy without ListMode is the frozen
// representation, where Mode alone decides an unlisted group (Ranked behaves
// as a blacklist with no listed group, Never as a whitelist with none).
//
// Pinned maps a group's provider key to its pinned final score: a pinned group that the list lets through is ranked at exactly
// that score, with neither Score nor any name keyword applied. Only the live
// site policy carries pins; the frozen representation has none.
type ProviderDefaultPolicy struct {
	Mode         SelectionMode
	Score        int
	Version      int64
	NameKeywords []NameKeywordRule
	ListMode     SiteListMode
	Listed       []string
	Pinned       map[string]int `json:",omitempty"`
}

// NameKeywordRule adjusts the provider default score when a discovered group
// name contains Keyword. Rules are additive and are evaluated case-insensitively.
type NameKeywordRule struct {
	Keyword string
	Score   int
}

// DiscoveryPolicy is the typed policy snapshot consumed by the automation
// dispatcher, built from one subscription's own subgroups section. Defaults are
// provider-owned, while rules are the subscription's exact or canonical entries.
type DiscoveryPolicy struct {
	MaxRSS           int
	AnimeRules       []ProviderGroupPolicy
	ProviderDefaults map[providerdomain.Kind]ProviderDefaultPolicy
	NameKeywords     map[providerdomain.Kind][]NameKeywordRule
}

func ResolveProviderGroupPolicy(ref providerdomain.GroupRef, canonicalKey string, observed bool, animeRules []ProviderGroupPolicy, providerDefault ProviderDefaultPolicy) (EffectiveProviderGroup, error) {
	return ResolveProviderGroupPolicyWithNameKeywords(ref, canonicalKey, "", observed, animeRules, providerDefault, nil)
}

// ResolveProviderGroupPolicyWithNameKeywords resolves exact and canonical
// policies first. Name-keyword adjustments apply only when the provider
// default is used, preserving explicit group policy precedence.
func ResolveProviderGroupPolicyWithNameKeywords(ref providerdomain.GroupRef, canonicalKey, groupName string, observed bool, animeRules []ProviderGroupPolicy, providerDefault ProviderDefaultPolicy, nameKeywords []NameKeywordRule) (EffectiveProviderGroup, error) {
	if err := ref.Validate(); err != nil {
		return EffectiveProviderGroup{}, err
	}
	if err := ValidateNameKeywordRules(nameKeywords); err != nil {
		return EffectiveProviderGroup{}, err
	}
	if nameKeywords == nil {
		nameKeywords = providerDefault.NameKeywords
		if err := ValidateNameKeywordRules(nameKeywords); err != nil {
			return EffectiveProviderGroup{}, err
		}
	}
	layers := []struct {
		rules  []ProviderGroupPolicy
		kind   PolicyTargetKind
		source EffectivePolicySource
	}{
		{animeRules, TargetProviderGroup, SourceAnimeExact},
		{animeRules, TargetCanonicalGroup, SourceAnimeCanonical},
	}
	// The site decision is needed for an explicit rule only as context in the
	// breakdown, so an invalid site default fails only a group it decides.
	site, siteMode, siteScore, siteErr := resolveSiteGroup(ref, canonicalKey, groupName, providerDefault, nameKeywords)
	for _, layer := range layers {
		policy, found, err := matchingPolicy(layer.rules, layer.kind, ref, canonicalKey)
		if err != nil {
			return EffectiveProviderGroup{}, err
		}
		if found {
			group := effectiveProviderGroup(ref, canonicalKey, observed, policy.Mode, policy.Score, policy.CountInLimit, layer.source, policy.Version)
			if siteErr == nil {
				switch policy.Mode {
				case Always:
					group.Breakdown = site.withOverride(EntryOverrideAlways)
				case Never:
					group.Breakdown = site.withOverride(EntryOverrideNever)
				}
			}
			return group, nil
		}
	}
	if siteErr != nil {
		return EffectiveProviderGroup{}, siteErr
	}
	var group EffectiveProviderGroup
	if siteMode == Ranked {
		group = effectiveProviderGroup(ref, canonicalKey, observed, Ranked, &siteScore, true, SourceProviderDefault, providerDefault.Version)
	} else {
		group = effectiveProviderGroup(ref, canonicalKey, observed, Never, nil, false, SourceProviderDefault, providerDefault.Version)
	}
	group.Breakdown = &site
	return group, nil
}

// resolveSiteGroup is the site layer's own decision for one group: the list
// effect, the default score and every matched name keyword. A whitelisted
// group, and an unlisted group under a blacklist, is ranked at its pinned
// score when the site pins it, otherwise at the site default plus its matched
// keywords; any other group is excluded, pinned or not.
func resolveSiteGroup(ref providerdomain.GroupRef, canonicalKey, groupName string, providerDefault ProviderDefaultPolicy, nameKeywords []NameKeywordRule) (ScoreBreakdown, SelectionMode, int, error) {
	breakdown := ScoreBreakdown{SiteDefaultScore: providerDefault.Score, EntryOverride: EntryOverrideNone, ListEffect: ListEffectNone}
	mode := providerDefault.ListMode
	listed := false
	switch mode {
	case SiteBlacklist, SiteWhitelist:
		for _, value := range providerDefault.Listed {
			if strings.TrimSpace(value) == ref.ExternalID {
				listed = true
				break
			}
		}
	case "":
		switch providerDefault.Mode {
		case Ranked:
			mode = SiteBlacklist
		case Never:
			mode = SiteWhitelist
		default:
			return ScoreBreakdown{}, "", 0, errors.New("provider default group policy must be ranked or never")
		}
	default:
		return ScoreBreakdown{}, "", 0, fmt.Errorf("unsupported site list mode %q", mode)
	}
	breakdown.SiteMode = mode
	switch {
	case mode == SiteBlacklist && listed:
		breakdown.ListEffect = ListEffectBlacklisted
	case mode == SiteWhitelist && listed:
		breakdown.ListEffect = ListEffectWhitelisted
	case mode == SiteWhitelist:
		breakdown.ListEffect = ListEffectNotWhitelisted
	}
	if breakdown.ListEffect == ListEffectBlacklisted || breakdown.ListEffect == ListEffectNotWhitelisted {
		return breakdown, Never, 0, nil
	}
	if pinned, ok := providerDefault.Pinned[ref.ExternalID]; ok {
		if pinned < 1 || pinned > 100 {
			return ScoreBreakdown{}, "", 0, fmt.Errorf("pinned score for group %s must be between 1 and 100", ref.Key())
		}
		score := pinned
		breakdown.PinnedScore = &pinned
		breakdown.FinalScore = &score
		return breakdown, Ranked, score, nil
	}
	breakdown.Keywords = MatchNameKeywords(canonicalKey, groupName, ref.ExternalID, nameKeywords)
	score := providerDefault.Score
	for _, keyword := range breakdown.Keywords {
		score += keyword.Score
	}
	breakdown.FinalScore = &score
	return breakdown, Ranked, score, nil
}

// withOverride is the breakdown of a group a per-anime manual intent decided:
// the site facts stay visible, but no score was used.
func (b ScoreBreakdown) withOverride(override EntryOverride) *ScoreBreakdown {
	result := b
	result.EntryOverride = override
	result.Keywords = nil
	result.PinnedScore = nil
	result.FinalScore = nil
	return &result
}

// ValidateNameKeywordRules validates one provider's additive score rules.
func ValidateNameKeywordRules(rules []NameKeywordRule) error {
	seen := make(map[string]struct{}, len(rules))
	for index, rule := range rules {
		keyword := strings.TrimSpace(rule.Keyword)
		if keyword == "" {
			return fmt.Errorf("name keyword rule %d must have a non-empty keyword", index)
		}
		if len(keyword) > 256 {
			return fmt.Errorf("name keyword rule %d exceeds the 256 character limit", index)
		}
		if rule.Score < -100 || rule.Score > 100 {
			return fmt.Errorf("name keyword rule %d score must be between -100 and 100", index)
		}
		key := strings.ToLower(keyword)
		if _, exists := seen[key]; exists {
			return fmt.Errorf("duplicate name keyword rule %q", keyword)
		}
		seen[key] = struct{}{}
	}
	return nil
}

// ApplyNameKeywordAdjustments adds every matching rule to the provider
// default score. A rule matches if its keyword occurs in any stable or
// provider-discovered group name representation.
func ApplyNameKeywordAdjustments(base int, canonicalKey, groupName, refName string, rules []NameKeywordRule) int {
	result := base
	for _, matched := range MatchNameKeywords(canonicalKey, groupName, refName, rules) {
		result += matched.Score
	}
	return result
}

// MatchNameKeywords returns every rule whose keyword occurs in one of the
// group's names, in rule order, each rule counted once.
func MatchNameKeywords(canonicalKey, groupName, refName string, rules []NameKeywordRule) []KeywordContribution {
	names := []string{strings.ToLower(strings.TrimSpace(canonicalKey)), strings.ToLower(strings.TrimSpace(groupName)), strings.ToLower(strings.TrimSpace(refName))}
	var result []KeywordContribution
	for _, rule := range rules {
		keyword := strings.ToLower(strings.TrimSpace(rule.Keyword))
		if keyword == "" {
			continue
		}
		for _, name := range names {
			if strings.Contains(name, keyword) {
				result = append(result, KeywordContribution{Keyword: strings.TrimSpace(rule.Keyword), Score: rule.Score})
				break
			}
		}
	}
	return result
}

func matchingPolicy(rules []ProviderGroupPolicy, kind PolicyTargetKind, ref providerdomain.GroupRef, canonicalKey string) (ProviderGroupPolicy, bool, error) {
	var matched ProviderGroupPolicy
	found := false
	for _, rule := range rules {
		if err := ValidateProviderGroupPolicy(rule); err != nil {
			return ProviderGroupPolicy{}, false, err
		}
		matches := rule.Target.Kind == kind
		switch kind {
		case TargetProviderGroup:
			matches = matches && rule.Target.ProviderRef != nil && *rule.Target.ProviderRef == ref
		case TargetCanonicalGroup:
			matches = matches && canonicalKey != "" && rule.Target.CanonicalKey == canonicalKey
		}
		if !matches {
			continue
		}
		if found {
			return ProviderGroupPolicy{}, false, errors.New("duplicate group policies target the same effective group")
		}
		matched, found = rule, true
	}
	return matched, found, nil
}

func effectiveProviderGroup(ref providerdomain.GroupRef, canonicalKey string, observed bool, mode SelectionMode, score *int, countInLimit bool, source EffectivePolicySource, version int64) EffectiveProviderGroup {
	value := 0
	if score != nil {
		value = *score
	}
	return EffectiveProviderGroup{
		Ref: ref, CanonicalKey: canonicalKey, Observed: observed, Mode: mode,
		Score: value, CountInLimit: countInLimit, PolicySource: source, PolicyVersion: version,
	}
}

type GroupSelectionOutcome string

const (
	GroupSelected    GroupSelectionOutcome = "selected"
	GroupNotSelected GroupSelectionOutcome = "not_selected"
	GroupUnavailable GroupSelectionOutcome = "unavailable"
)

// The decision reasons are part of the read contract: a caller that explains a
// group's selection state to a person reads these codes, so they are named here
// rather than repeated as string literals.
const (
	ReasonProviderGroupNotObserved = "provider_group_not_observed"
	ReasonPolicyNever              = "policy_never"
	ReasonPolicyScoreNegative      = "policy_score_negative"
	ReasonPolicyAlways             = "policy_always"
	ReasonRankedWithinMaxRSS       = "ranked_within_max_rss"
	ReasonMaxRSSExhausted          = "max_rss_exhausted"
	// ReasonSiteBlacklisted and ReasonSiteNotWhitelisted replace policy_never
	// when the site list, not a per-anime manual exclusion, excluded the
	// group; policy_never then means a manual exclusion only.
	ReasonSiteBlacklisted    = "site_blacklisted"
	ReasonSiteNotWhitelisted = "site_not_whitelisted"
)

type GroupSelectionDecision struct {
	Group   EffectiveProviderGroup
	Outcome GroupSelectionOutcome
	Reason  string
	MaxRSS  int
	Rank    *int
}

// ProviderGroupSelectionInput is one group's selection input in the
// transport-neutral form the decision engine consumes. Key is the caller's
// stable group identity; for provider-namespaced groups it is
// providerdomain.GroupRef.Key() ("<provider>:<external_ref>").
//
// It exists so a caller that owns a non-Ref representation of the same group
// (for example a stored subgroups section entry) can obtain the identical
// selection decision without reimplementing the rules, and without the input
// having to pass GroupRef validation it may not need.
type ProviderGroupSelectionInput struct {
	Key          string
	Observed     bool
	Mode         SelectionMode
	Score        int
	CountInLimit bool
}

// GroupSelectionResult is one decision, aligned to its input by Index.
type GroupSelectionResult struct {
	Index   int
	Key     string
	Outcome GroupSelectionOutcome
	Reason  string
	Rank    *int
}

// SelectProviderGroupsByKey is the single group-selection decision core. It
// returns decisions in the engine's deterministic order (observed first, then
// always/ranked/never, then ranked score descending, then key ascending), with
// each result carrying its input index so a caller can map back to its own
// representation.
func SelectProviderGroupsByKey(inputs []ProviderGroupSelectionInput, maxRSS int) ([]GroupSelectionResult, error) {
	if maxRSS < 0 {
		return nil, errors.New("max_rss cannot be negative")
	}
	ordered := make([]ProviderGroupSelectionInput, len(inputs))
	copy(ordered, inputs)
	indexByInput := make([]int, len(inputs))
	for i := range indexByInput {
		indexByInput[i] = i
	}
	for _, input := range ordered {
		switch input.Mode {
		case Ranked:
			if !input.CountInLimit {
				return nil, errors.New("ranked group must count in max_rss")
			}
		case Always, Never:
		default:
			return nil, fmt.Errorf("unsupported effective group mode %q", input.Mode)
		}
	}
	// Sort the input indices so the engine's ordering is reproduced exactly.
	sort.SliceStable(indexByInput, func(i, j int) bool {
		left, right := ordered[indexByInput[i]], ordered[indexByInput[j]]
		if left.Observed != right.Observed {
			return left.Observed
		}
		if left.Mode != right.Mode {
			return selectionModeOrder(left.Mode) < selectionModeOrder(right.Mode)
		}
		if left.Mode == Ranked && left.Score != right.Score {
			return left.Score > right.Score
		}
		return left.Key < right.Key
	})

	results := make([]GroupSelectionResult, 0, len(ordered))
	remaining := maxRSS
	rank := 0
	for _, index := range indexByInput {
		input := ordered[index]
		result := GroupSelectionResult{Index: index, Key: input.Key}
		switch {
		case !input.Observed:
			result.Outcome, result.Reason = GroupUnavailable, ReasonProviderGroupNotObserved
		case input.Mode == Never:
			result.Outcome, result.Reason = GroupNotSelected, ReasonPolicyNever
		case input.Mode == Ranked && input.Score < 0:
			result.Outcome, result.Reason = GroupNotSelected, ReasonPolicyScoreNegative
		case input.Mode == Always:
			result.Outcome, result.Reason = GroupSelected, ReasonPolicyAlways
			if input.CountInLimit {
				remaining--
			}
		case input.Mode == Ranked && remaining > 0:
			rank++
			result.Outcome, result.Reason, result.Rank = GroupSelected, ReasonRankedWithinMaxRSS, intPointer(rank)
			remaining--
		case input.Mode == Ranked:
			rank++
			result.Outcome, result.Reason, result.Rank = GroupNotSelected, ReasonMaxRSSExhausted, intPointer(rank)
		}
		results = append(results, result)
	}
	return results, nil
}

// SelectProviderGroups applies group-only policy. It deliberately has no
// episode or release input: max_rss counts selected group subscriptions.
func SelectProviderGroups(groups []EffectiveProviderGroup, maxRSS int) ([]GroupSelectionDecision, error) {
	inputs := make([]ProviderGroupSelectionInput, len(groups))
	for i, group := range groups {
		if err := group.Ref.Validate(); err != nil {
			return nil, err
		}
		inputs[i] = ProviderGroupSelectionInput{
			Key: group.Ref.Key(), Observed: group.Observed, Mode: group.Mode,
			Score: group.Score, CountInLimit: group.CountInLimit,
		}
	}
	results, err := SelectProviderGroupsByKey(inputs, maxRSS)
	if err != nil {
		return nil, err
	}
	decisions := make([]GroupSelectionDecision, 0, len(results))
	for _, result := range results {
		group := groups[result.Index]
		decisions = append(decisions, GroupSelectionDecision{
			Group: group, Outcome: result.Outcome, Reason: siteReason(result.Reason, group.Breakdown),
			MaxRSS: maxRSS, Rank: result.Rank,
		})
	}
	return decisions, nil
}

// siteReason names the site list as the reason for a policy_never decision
// that no per-anime manual intent made.
func siteReason(reason string, breakdown *ScoreBreakdown) string {
	if reason != ReasonPolicyNever || breakdown == nil || breakdown.EntryOverride != EntryOverrideNone {
		return reason
	}
	switch breakdown.ListEffect {
	case ListEffectBlacklisted:
		return ReasonSiteBlacklisted
	case ListEffectNotWhitelisted:
		return ReasonSiteNotWhitelisted
	}
	return reason
}

func AutomaticAdmissionAllowed(active bool, outcome GroupSelectionOutcome) bool {
	return active && outcome == GroupSelected
}

func selectionModeOrder(mode SelectionMode) int {
	switch mode {
	case Always:
		return 0
	case Ranked:
		return 1
	case Never:
		return 2
	default:
		return 3
	}
}

func intPointer(value int) *int { return &value }
