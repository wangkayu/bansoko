package application

type persistedSubgroupProvider string

type persistedSubgroupSelectionMode string

type persistedProviderGroupDefaultMode string

const (
	persistedSubgroupProviderAniBT    persistedSubgroupProvider         = "anibt"
	persistedSubgroupProviderMikan    persistedSubgroupProvider         = "mikan"
	persistedSubgroupSelectionAuto    persistedSubgroupSelectionMode    = "auto"
	persistedSubgroupSelectionManual  persistedSubgroupSelectionMode    = "manual"
	persistedProviderDefaultBlacklist persistedProviderGroupDefaultMode = "blacklist"
	persistedProviderDefaultWhitelist persistedProviderGroupDefaultMode = "whitelist"
)

type persistedNameKeywordRule struct {
	Keyword string `json:"keyword"`
	Score   int    `json:"score"`
}

type persistedProviderNameKeywordRules struct {
	Anibt *[]persistedNameKeywordRule `json:"anibt,omitempty"`
	Mikan *[]persistedNameKeywordRule `json:"mikan,omitempty"`
}

type persistedPinnedGroupScore struct {
	GroupRef string `json:"group_ref"`
	Score    int    `json:"score"`
}

type persistedProviderGroupDefault struct {
	Groups       []string                          `json:"groups"`
	Mode         persistedProviderGroupDefaultMode `json:"mode"`
	Score        int                               `json:"score"`
	PinnedScores []persistedPinnedGroupScore       `json:"pinned_scores,omitempty"`
}

type persistedProviderGroupDefaults struct {
	Anibt *persistedProviderGroupDefault `json:"anibt,omitempty"`
	Mikan *persistedProviderGroupDefault `json:"mikan,omitempty"`
}

type persistedSubgroupScoreEntry struct {
	DisplayName       string                         `json:"display_name"`
	EffectiveSelected bool                           `json:"effective_selected"`
	GroupRef          string                         `json:"group_ref"`
	ManualSelected    *bool                          `json:"manual_selected"`
	Provider          persistedSubgroupProvider      `json:"provider"`
	Score             int                            `json:"score"`
	SelectionMode     persistedSubgroupSelectionMode `json:"selection_mode"`
}

type persistedSubgroupsSection struct {
	Entries          []persistedSubgroupScoreEntry      `json:"entries"`
	MaxRss           int                                `json:"max_rss"`
	NameKeywords     *persistedProviderNameKeywordRules `json:"name_keywords,omitempty"`
	ProviderDefaults *persistedProviderGroupDefaults    `json:"provider_defaults,omitempty"`
}
