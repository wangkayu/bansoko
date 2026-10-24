package parser

import (
	"errors"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/wangkayu/reltitle/pkg/config"
	"github.com/wangkayu/reltitle/pkg/model"
	"github.com/wangkayu/reltitle/pkg/tokenize"
)

// Parser parses anime/film release titles into structured results. Build one
// with NewDefault (embedded configs) or New (custom configs); after New the
// Parser is read-only and safe to share across goroutines.
type Parser struct {
	cfg                   *config.Config
	groupIndex            map[string]*config.GroupDef // 字幕组索引: normalized pattern -> GroupDef
	aliasInlineStripWords map[string]struct{}
	aliasNoiseExactWords  map[string]struct{}
	aliasNoisePatterns    []*regexp.Regexp
	notePromotionRules    []notePromotionRule
	groupRouteRules       []groupRouteRule
	groupSlot2Rules       []groupSlot2PlatformRule
	packProfileRules      []packProfileRule
	sourceTypeCanonical   map[string]string
	platformCanonical     map[string]string
	videoCodecCanonical   map[string]string
	audioCodecCanonical   map[string]string
	containerCanonical    map[string]string
	audioFlagCanonical    map[string]string
	subFlagCanonical      map[string]string
	subLangCanonical      map[string]string
	audioLangCanonical    map[string]string
	packKindSourceType    map[string]string
}

type notePromotionRule struct {
	values              map[string]struct{}
	targetType          model.AttrType
	normalized          string
	normalizedFromToken bool
}

type groupRouteRule struct {
	id                      string
	priority                int
	groups                  map[string]struct{}
	rawMustContainAny       []string
	requireEpisodeRange     bool
	requireEpisodeLike      bool
	requireEpisodeWithTitle bool
	episodeTitleSepIn       map[string]struct{}
	forceGroup              string
	forceTitle              string
	clearAliases            bool
	forceTemplateID         string
	forceContentKind        string
	forceRouteID            string
	titleStripPrefixes      []string
	aliasStripPrefixes      []string
}

type groupSlot2PlatformRule struct {
	id                        string
	priority                  int
	groups                    map[string]struct{}
	groupRegex                *regexp.Regexp
	slotIndex                 int
	yueKeywords               []string
	distributorKeywords       []string
	languageMetaKeywords      []string
	trimForInfer              []string
	platformWhitelist         []groupSlot2PlatformWhitelist
	emitAudioLang             string
	noteOnDistributor         string
	inferUnknownPlatform      bool
	unknownPlatformConfidence float64
}

type groupSlot2PlatformWhitelist struct {
	containsAny []string
	normalized  string
}

type packProfileRule struct {
	id                  string
	priority            int
	emit                string
	contentKindIn       map[string]struct{}
	packKindIn          map[string]struct{}
	packKindNotIn       map[string]struct{}
	requiresContentsAll map[string]struct{}
	requiresContentsAny map[string]struct{}
	forbidContentsAny   map[string]struct{}
	hasSeason           *bool
	hasSeasonRange      *bool
	minContentsDistinct int
}

var wxhResolutionPattern = regexp.MustCompile(`(?i)^(\d{2,5})\s*[x×X]\s*(\d{2,5})(?:\s*([ip]))?$`)

// New precompiles cfg into sorted rule structures. A nil cfg yields an empty
// configuration; callers normally use NewDefault or config.LoadAll.
func New(cfg *config.Config) *Parser {
	if cfg == nil {
		cfg = &config.Config{}
	}
	p := &Parser{
		cfg:                   cfg,
		groupIndex:            make(map[string]*config.GroupDef),
		aliasInlineStripWords: make(map[string]struct{}),
		aliasNoiseExactWords:  make(map[string]struct{}),
		sourceTypeCanonical:   buildTypeCanonicalMap(cfg, "source_type"),
		platformCanonical:     buildTypeCanonicalMap(cfg, "platform"),
		videoCodecCanonical:   buildTypeCanonicalMap(cfg, "video_codec"),
		audioCodecCanonical:   buildTypeCanonicalMap(cfg, "audio_codec"),
		containerCanonical:    buildTypeCanonicalMap(cfg, "container"),
		audioFlagCanonical:    buildTypeCanonicalMap(cfg, "audio_flag"),
		subFlagCanonical:      buildTypeCanonicalMap(cfg, "sub_flag"),
		subLangCanonical:      buildTypeCanonicalMap(cfg, "sub_lang"),
		audioLangCanonical:    buildTypeCanonicalMap(cfg, "audio_lang"),
		packKindSourceType:    buildPackKindSourceTypeMap(cfg),
	}

	// 构建字幕组索引
	if cfg.GroupList.Groups != nil {
		for i := range cfg.GroupList.Groups {
			group := &cfg.GroupList.Groups[i]
			for _, pattern := range group.Patterns {
				normalized := strings.ToLower(pattern)
				p.groupIndex[normalized] = group
			}
		}
	}

	for _, w := range cfg.DictAliasNoiseWords.AliasNoise.InlineStripWordsCaseInsensitive {
		ww := strings.ToLower(strings.TrimSpace(w))
		if ww == "" {
			continue
		}
		p.aliasInlineStripWords[ww] = struct{}{}
	}
	for _, w := range cfg.DictAliasNoiseWords.AliasNoise.ExactWordsCaseInsensitive {
		ww := strings.ToLower(strings.TrimSpace(w))
		if ww == "" {
			continue
		}
		p.aliasNoiseExactWords[ww] = struct{}{}
	}
	for _, pat := range cfg.DictAliasNoiseWords.AliasNoise.RegexPatterns {
		pp := strings.TrimSpace(pat)
		if pp == "" {
			continue
		}
		re, err := regexp.Compile(pp)
		if err != nil {
			continue
		}
		p.aliasNoisePatterns = append(p.aliasNoisePatterns, re)
	}

	for _, r := range cfg.DictNotePromotion.Rules {
		targetType := model.AttrType(strings.TrimSpace(r.PromoteTo.Type))
		if targetType == "" {
			continue
		}
		vals := make(map[string]struct{})
		for _, v := range r.Match.NoteValuesCaseInsensitive {
			vv := strings.ToLower(strings.TrimSpace(v))
			if vv == "" {
				continue
			}
			vals[vv] = struct{}{}
		}
		if len(vals) == 0 {
			continue
		}
		p.notePromotionRules = append(p.notePromotionRules, notePromotionRule{
			values:              vals,
			targetType:          targetType,
			normalized:          strings.TrimSpace(r.PromoteTo.Normalized),
			normalizedFromToken: r.PromoteTo.NormalizedFromToken,
		})
	}

	for _, r := range cfg.GroupRoute.Overrides {
		if !r.Enabled {
			continue
		}
		id := strings.TrimSpace(r.ID)
		if id == "" {
			continue
		}
		groupSet := make(map[string]struct{}, len(r.Match.GroupIn))
		for _, g := range r.Match.GroupIn {
			gg := strings.ToLower(strings.TrimSpace(g))
			if gg == "" {
				continue
			}
			groupSet[gg] = struct{}{}
		}
		rawContains := make([]string, 0, len(r.Match.RawMustContainAny))
		for _, s := range r.Match.RawMustContainAny {
			ss := strings.ToLower(strings.TrimSpace(s))
			if ss == "" {
				continue
			}
			rawContains = append(rawContains, ss)
		}
		titleStripPrefixes := make([]string, 0, len(r.Output.TitleStripPrefixes))
		for _, s := range r.Output.TitleStripPrefixes {
			ss := strings.TrimSpace(s)
			if ss == "" {
				continue
			}
			titleStripPrefixes = append(titleStripPrefixes, ss)
		}
		aliasStripPrefixes := make([]string, 0, len(r.Output.AliasStripPrefixes))
		for _, s := range r.Output.AliasStripPrefixes {
			ss := strings.TrimSpace(s)
			if ss == "" {
				continue
			}
			aliasStripPrefixes = append(aliasStripPrefixes, ss)
		}
		episodeTitleSepIn := map[string]struct{}{}
		for _, s := range r.Match.EpisodeTitleSepIn {
			if sep := normalizeEpisodeTitleSepConstraint(s); sep != "" {
				episodeTitleSepIn[sep] = struct{}{}
			}
		}
		p.groupRouteRules = append(p.groupRouteRules, groupRouteRule{
			id:                      id,
			priority:                r.Priority,
			groups:                  groupSet,
			rawMustContainAny:       rawContains,
			requireEpisodeRange:     r.Match.RequireEpisodeRange,
			requireEpisodeLike:      r.Match.RequireEpisodeLike,
			requireEpisodeWithTitle: r.Match.RequireEpisodeWithTitle,
			episodeTitleSepIn:       episodeTitleSepIn,
			forceGroup:              strings.TrimSpace(r.Output.ForceGroup),
			forceTitle:              strings.TrimSpace(r.Output.ForceTitle),
			clearAliases:            r.Output.ClearAliases,
			forceTemplateID:         strings.TrimSpace(r.Output.ForceTemplateID),
			forceContentKind:        strings.TrimSpace(r.Output.ForceContentKind),
			forceRouteID:            strings.TrimSpace(r.Output.ForceRouteID),
			titleStripPrefixes:      titleStripPrefixes,
			aliasStripPrefixes:      aliasStripPrefixes,
		})
	}
	sort.SliceStable(p.groupRouteRules, func(i, j int) bool {
		return p.groupRouteRules[i].priority > p.groupRouteRules[j].priority
	})

	for _, r := range cfg.GroupSlot2Platform.Rules {
		if !r.Enabled {
			continue
		}
		id := strings.TrimSpace(r.ID)
		if id == "" {
			continue
		}
		groupSet := make(map[string]struct{}, len(r.Match.GroupIn))
		for _, g := range r.Match.GroupIn {
			gg := strings.ToLower(strings.TrimSpace(g))
			if gg == "" {
				continue
			}
			groupSet[gg] = struct{}{}
		}
		var groupRegex *regexp.Regexp
		if ss := strings.TrimSpace(r.Match.GroupRegex); ss != "" {
			if re, err := regexp.Compile(ss); err == nil {
				groupRegex = re
			}
		}
		slotIndex := r.Match.SlotIndex
		if slotIndex <= 0 {
			slotIndex = 2
		}
		toLowerList := func(in []string) []string {
			out := make([]string, 0, len(in))
			for _, s := range in {
				ss := strings.ToLower(strings.TrimSpace(s))
				if ss == "" {
					continue
				}
				out = append(out, ss)
			}
			return out
		}
		yueKeywords := toLowerList(r.Keywords.Yue)
		distributorKeywords := toLowerList(r.Keywords.Distributor)
		languageMetaKeywords := toLowerList(r.Keywords.LanguageMeta)
		trimForInfer := toLowerList(r.Keywords.TrimForInfer)
		if len(yueKeywords) == 0 {
			yueKeywords = []string{"粤语", "粵語", "cantonese"}
		}
		if len(distributorKeywords) == 0 {
			distributorKeywords = []string{"代理商", "發行商", "发行商"}
		}
		platformWhitelist := make([]groupSlot2PlatformWhitelist, 0, len(r.Whitelist))
		for _, w := range r.Whitelist {
			norm := strings.TrimSpace(w.Normalized)
			if norm == "" {
				continue
			}
			containsAny := toLowerList(w.ContainsAny)
			if len(containsAny) == 0 {
				continue
			}
			platformWhitelist = append(platformWhitelist, groupSlot2PlatformWhitelist{
				containsAny: containsAny,
				normalized:  norm,
			})
		}
		emitAudioLang := strings.TrimSpace(r.Output.EmitAudioLang)
		if emitAudioLang == "" {
			emitAudioLang = "YUE"
		}
		noteOnDistributor := strings.TrimSpace(r.Output.NoteOnDistributor)
		if noteOnDistributor == "" {
			noteOnDistributor = "CHANNEL_DISTRIBUTOR"
		}
		unknownConf := r.Output.UnknownPlatformConfidence
		if unknownConf <= 0 {
			unknownConf = 0.6
		}
		p.groupSlot2Rules = append(p.groupSlot2Rules, groupSlot2PlatformRule{
			id:                        id,
			priority:                  r.Priority,
			groups:                    groupSet,
			groupRegex:                groupRegex,
			slotIndex:                 slotIndex,
			yueKeywords:               yueKeywords,
			distributorKeywords:       distributorKeywords,
			languageMetaKeywords:      languageMetaKeywords,
			trimForInfer:              trimForInfer,
			platformWhitelist:         platformWhitelist,
			emitAudioLang:             emitAudioLang,
			noteOnDistributor:         noteOnDistributor,
			inferUnknownPlatform:      r.Output.InferUnknownPlatform,
			unknownPlatformConfidence: unknownConf,
		})
	}
	sort.SliceStable(p.groupSlot2Rules, func(i, j int) bool {
		return p.groupSlot2Rules[i].priority > p.groupSlot2Rules[j].priority
	})

	toUpperSet := func(in []string) map[string]struct{} {
		out := make(map[string]struct{}, len(in))
		for _, s := range in {
			ss := strings.ToUpper(strings.TrimSpace(s))
			if ss == "" {
				continue
			}
			out[ss] = struct{}{}
		}
		return out
	}
	toLowerSet := func(in []string) map[string]struct{} {
		out := make(map[string]struct{}, len(in))
		for _, s := range in {
			ss := strings.ToLower(strings.TrimSpace(s))
			if ss == "" {
				continue
			}
			out[ss] = struct{}{}
		}
		return out
	}
	for _, r := range cfg.PackProfileRules.Rules {
		if !r.Enabled {
			continue
		}
		id := strings.TrimSpace(r.ID)
		emit := strings.TrimSpace(r.Emit)
		if id == "" || emit == "" {
			continue
		}
		minDistinct := r.When.MinContentsDistinct
		if minDistinct < 0 {
			minDistinct = 0
		}
		p.packProfileRules = append(p.packProfileRules, packProfileRule{
			id:                  id,
			priority:            r.Priority,
			emit:                emit,
			contentKindIn:       toUpperSet(r.When.ContentKindIn),
			packKindIn:          toLowerSet(r.When.PackKindIn),
			packKindNotIn:       toLowerSet(r.When.PackKindNotIn),
			requiresContentsAll: toUpperSet(r.When.RequiresContentsAll),
			requiresContentsAny: toUpperSet(r.When.RequiresContentsAny),
			forbidContentsAny:   toUpperSet(r.When.ForbidContentsAny),
			hasSeason:           r.When.HasSeason,
			hasSeasonRange:      r.When.HasSeasonRange,
			minContentsDistinct: minDistinct,
		})
	}
	sort.SliceStable(p.packProfileRules, func(i, j int) bool {
		return p.packProfileRules[i].priority > p.packProfileRules[j].priority
	})

	return p
}

func selectGroupFromAttrs(attrs []model.Attribute) (string, [2]int, bool) {
	best := ""
	span := [2]int{0, 0}
	bestScore := -1
	for _, a := range attrs {
		if a.Type != model.AttrGroup {
			continue
		}
		g := strings.TrimSpace(a.Normalized)
		if g == "" {
			continue
		}
		score := 0
		if a.Source.Canonical() == model.AttrSourceLexicon {
			score += 100
		}
		if a.Confidence > 0 {
			score += int(a.Confidence * 10)
		}
		if score > bestScore {
			bestScore = score
			best = g
			span = [2]int{a.TokenStart, a.TokenEnd}
		}
	}
	if bestScore < 0 {
		return "", [2]int{0, 0}, false
	}
	return best, span, true
}

func hasGroupAttr(attrs []model.Attribute, group string) bool {
	group = strings.TrimSpace(group)
	if group == "" {
		return false
	}
	for _, a := range attrs {
		if a.Type != model.AttrGroup {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(a.Normalized), group) {
			return true
		}
	}
	return false
}

func buildTypeCanonicalMap(cfg *config.Config, targetType string) map[string]string {
	out := make(map[string]string)
	if cfg == nil {
		return out
	}
	targetKey := strings.ToLower(strings.TrimSpace(targetType))
	if targetKey == "" {
		return out
	}
	for _, r := range cfg.DictTokenSynonyms.Rules {
		if strings.ToLower(strings.TrimSpace(r.Emit.Type)) != targetKey {
			continue
		}
		norm := strings.TrimSpace(r.Emit.Normalized)
		if norm == "" {
			continue
		}
		out[strings.ToLower(norm)] = norm
		for _, raw := range r.RawTokens {
			rr := strings.ToLower(strings.TrimSpace(raw))
			if rr == "" {
				continue
			}
			out[rr] = norm
		}
	}
	for _, canonical := range readStringSlice(cfg.DictCanonicalValues.Canonical[targetKey]) {
		cc := strings.TrimSpace(canonical)
		if cc == "" {
			continue
		}
		key := strings.ToLower(cc)
		if _, exists := out[key]; !exists {
			out[key] = cc
		}
	}
	aliases := readNestedStringMap(cfg.DictCanonicalValues.Canonical, "mappings", targetKey+"_aliases")
	// Iterate in key order so colliding entries resolve the same way on every run.
	for _, raw := range sortedKeys(aliases) {
		canonical := aliases[raw]
		rr := strings.ToLower(strings.TrimSpace(raw))
		cc := strings.TrimSpace(canonical)
		if rr == "" || cc == "" {
			continue
		}
		out[rr] = cc
		if _, exists := out[strings.ToLower(cc)]; !exists {
			out[strings.ToLower(cc)] = cc
		}
	}
	return out
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func buildPackKindSourceTypeMap(cfg *config.Config) map[string]string {
	out := make(map[string]string)
	if cfg == nil {
		return out
	}
	mappings := readNestedStringMap(cfg.DictCanonicalValues.Canonical, "mappings", "pack_kind_to_source_type")
	for _, raw := range sortedKeys(mappings) {
		canonical := mappings[raw]
		rr := strings.ToLower(strings.TrimSpace(raw))
		cc := strings.TrimSpace(canonical)
		if rr == "" || cc == "" {
			continue
		}
		out[rr] = cc
	}
	return out
}

func readNestedStringMap(root map[string]any, path ...string) map[string]string {
	if len(path) == 0 {
		return map[string]string{}
	}
	cur := mapStringAny(root)
	for i, key := range path {
		if cur == nil {
			return map[string]string{}
		}
		v, ok := cur[key]
		if !ok {
			return map[string]string{}
		}
		if i == len(path)-1 {
			return mapStringString(v)
		}
		cur = mapStringAny(v)
	}
	return map[string]string{}
}

func readStringSlice(v any) []string {
	switch vv := v.(type) {
	case []string:
		return append([]string(nil), vv...)
	case []any:
		out := make([]string, 0, len(vv))
		for _, item := range vv {
			s, ok := item.(string)
			if !ok {
				continue
			}
			out = append(out, s)
		}
		return out
	default:
		return nil
	}
}

func mapStringAny(v any) map[string]any {
	switch vv := v.(type) {
	case map[string]any:
		return vv
	case map[any]any:
		out := make(map[string]any, len(vv))
		for k, val := range vv {
			ks, ok := k.(string)
			if !ok {
				continue
			}
			out[ks] = val
		}
		return out
	default:
		return nil
	}
}

func mapStringString(v any) map[string]string {
	switch vv := v.(type) {
	case map[string]string:
		return vv
	case map[string]any:
		out := make(map[string]string, len(vv))
		for k, val := range vv {
			s, ok := val.(string)
			if !ok {
				continue
			}
			out[k] = s
		}
		return out
	case map[any]any:
		out := make(map[string]string, len(vv))
		for k, val := range vv {
			ks, ok1 := k.(string)
			vs, ok2 := val.(string)
			if !ok1 || !ok2 {
				continue
			}
			out[ks] = vs
		}
		return out
	default:
		return map[string]string{}
	}
}

// isKnownGroup checks if a token is a known subtitle group
// Returns the group definition and confidence boost if found
func (p *Parser) isKnownGroup(token string) (*config.GroupDef, bool) {
	normalized := strings.ToLower(strings.TrimSpace(token))
	if normalized == "" {
		return nil, false
	}

	group, ok := p.groupIndex[normalized]
	return group, ok
}

func normalizeRawTitleForParsing(raw string) string {
	s := strings.Map(func(r rune) rune {
		switch r {
		case '\u200b', '\u200c', '\u200d', '\u2060', '\ufeff':
			return -1
		default:
			return r
		}
	}, raw)
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}
	unwrap := func(prefix string) (string, bool) {
		if !strings.HasPrefix(s, prefix) || !strings.HasSuffix(s, "]") {
			return "", false
		}
		if len(s) <= len(prefix) {
			return "", false
		}
		inner := strings.TrimSpace(s[len(prefix) : len(s)-1])
		if inner == "" {
			return "", false
		}
		return "[" + inner, true
	}
	if out, ok := unwrap("[搬運["); ok {
		return out
	}
	if out, ok := unwrap("[搬运["); ok {
		return out
	}
	return s
}

// Parse parses one raw release title and returns the full result. It only
// returns an error when the title is empty; anything unrecognized falls back
// to the UNKNOWN template or note attributes.
func (p *Parser) Parse(rawTitle string) (*model.ParseResult, error) {
	if strings.TrimSpace(rawTitle) == "" {
		return nil, errors.New("empty title")
	}
	originalRawTitle := rawTitle
	rawTitle = normalizeRawTitleForParsing(rawTitle)

	toks := tokenize.Tokenize(rawTitle)
	tokens := make([]model.Token, 0, len(toks))
	for _, t := range toks {
		tokens = append(tokens, model.Token{Text: t})
	}
	p.protectKnownTitleSpans(rawTitle, tokens)

	attrs := make([]model.Attribute, 0, 16)
	seasonRange := extractSeasonRangeAndRemoveFromTokens(tokens)
	extractPackSeasonExtrasAndRemoveFromTokens(tokens, &attrs, seasonRange)
	extractStrictCustomReleaseTagAttrsAndRemoveFromTokens(tokens, &attrs)
	extractGuardedDVDRIPReleaseTagAttrsAndRemoveFromTokens(tokens, &attrs)
	extractPlatformRegionAttrsAndRemoveFromTokens(tokens, &attrs)
	version := extractVersionAttrsAndRemoveFromTokens(tokens, &attrs)
	extractEditionAttrsAndRemoveFromTokens(tokens, &attrs, shouldUseUnderscoreEditionRoute(rawTitle))
	extractSearchHintIndexAttrsAndRemoveFromTokens(tokens, &attrs)
	extractSeasonShowTagAttrsAndRemoveFromTokens(tokens, &attrs)
	if isToonsHubCN(tokens) {
		extractTechSubsSemanticsAndRemoveFromTokens(tokens, &attrs)
	}
	p.applyLexicon(tokens, &attrs)
	markFileExtensionContainer(rawTitle, tokens, &attrs)
	p.extractStableTechSpanComboAttrsAndRemoveFromTokens(tokens, &attrs)
	p.extractLooseCodecBitDepthAttrsAndRemoveFromTokens(tokens, &attrs)
	extractDashedAudioCodecAttrsAndRemoveFromTokens(tokens, &attrs)
	p.extractMaProfileAudioCodecAttrsAndRemoveFromTokens(tokens, &attrs)
	p.extractStandaloneMaProfileBitDepthAttrsAndRemoveFromTokens(tokens, &attrs)
	p.extractStandaloneSourceTypeAttrsAndRemoveFromTokens(tokens, &attrs)
	p.extractStandaloneAudioCodecAttrsAndRemoveFromTokens(tokens, &attrs)
	extractCRCAttrsAndRemoveFromTokens(tokens, &attrs)
	p.extractVolumeEpisodeMetaMarkersAndRemoveFromTokens(tokens, &attrs)
	extractLooseBitDepthAttrsAndRemoveFromTokens(tokens, &attrs)
	promoteFrenchMultiNoteToAudioFlag(attrs)
	p.normalizePlatformAttrs(attrs)
	p.normalizeSourceTypeAttrs(attrs)
	convertRegionAttrsToPlatformRegion(attrs)
	extractWxHResolutionAttrsAndRemoveFromTokens(tokens, &attrs)
	extractYearAttrsAndRemoveFromTokens(tokens, &attrs, hasTechAttr(attrs))
	removePreYearNoiseNumberTokens(tokens)
	tailParenBlock := detectTailParenBlock(tokens)

	group := ""
	groupSpan := [2]int{0, 0}
	if g0, span0, ok := selectGroupFromAttrs(attrs); ok {
		group = normalizeGroupText(g0)
		groupSpan = span0
	}
	if group == "" {
		group, groupSpan = extractHeadBracketGroup(tokens)
	}
	if group == "" {
		group, groupSpan = extractHeadParenGroup(tokens)
	}
	if group == "" {
		group, groupSpan = extractHeadBracketGroupAfterLeadingParenMeta(tokens)
	}
	if group == "" {
		if g := detectHeadBracketGroupFromRawTitle(rawTitle); g != "" {
			group = g
			groupSpan = [2]int{-1, -1}
		}
	}

	if group != "" {
		// If we can detect a reliable tail group from raw title (e.g. "-ANi"), the head bracket
		// is more likely a bracketed title, not a release group. Do not remove it as group.
		// A head value present in grouplist.yaml has stronger evidence and must not be
		// displaced by a free-form trailing bracket.
		_, knownHeadGroup := p.isKnownGroup(group)
		if !knownHeadGroup && !hasExplicitGroupIdentityMarker(group) {
			if tg := p.detectTailGroupFromRawTitle(rawTitle); tg != "" {
				ng := normalizeGroupText(group)
				nt := normalizeGroupText(tg)
				if !strings.EqualFold(ng, nt) {
					group = ""
				}
			}
		}
	}

	if group != "" {
		// Group normalization is now handled by grouplist.yaml configuration
		// Apply normal normalization
		group = normalizeGroupText(group)
		if !hasGroupAttr(attrs, group) {
			attrs = append(attrs, model.Attribute{Type: model.AttrGroup, Normalized: group, TokenStart: groupSpan[0], TokenEnd: groupSpan[1], Confidence: 0.9, Source: model.AttrSourceHeuristic})
		}
		if groupSpan[0] >= 0 && groupSpan[1] > groupSpan[0] {
			for i := groupSpan[0]; i < groupSpan[1] && i < len(tokens); i++ {
				tokens[i].Removed = true
			}
		}
	}
	if group == "" {
		if g := extractInlineStarGroup(tokens); g != "" {
			group = normalizeGroupText(g)
			attrs = append(attrs, model.Attribute{Type: model.AttrGroup, Normalized: group, TokenStart: 0, TokenEnd: 1, Confidence: 0.7, Source: model.AttrSourceHeuristic})
		}
	}
	if group == "" {
		if g, gIdx := detectTechPrefixedGroup(tokens); g != "" {
			group = normalizeGroupText(g)
			attrs = append(attrs, model.Attribute{Type: model.AttrGroup, Normalized: group, TokenStart: gIdx, TokenEnd: gIdx + 1, Confidence: 0.7, Source: model.AttrSourceHeuristic})
		}
	}
	attrs = pruneGroupSpanTechArtifacts(attrs, groupSpan)

	// Structural title protection (dictionary-free): "N/N" fraction-like digit
	// segments in the title zone (e.g. "22/7") and leading digit titles in a
	// strict template slot (e.g. "86 - 03") must not be mistaken for
	// season/episode markers by later detection passes.
	protectTitleFractionSpans(tokens, groupSpan[1])
	protectTitleLeadingDigits(tokens, groupSpan[1])

	packKind := detectPackKind(p.cfg, rawTitle)
	ctx := newParseContext(rawTitle, packKind, tailParenBlock)
	ctx.AliasValidFunc = p.isValidAliasToken
	ctx.GroupSlot2Rules = p.groupSlot2Rules
	ctx.AudioCodecCanonical = p.audioCodecCanonical
	ctx.SeasonRange = seasonRange
	ctx.Group = group
	for _, np := range parsePasses {
		np.Pass(tokens, &attrs, ctx)
	}

	st := &parseState{
		attrs:            attrs,
		ctx:              ctx,
		group:            group,
		groupSpan:        groupSpan,
		originalRawTitle: originalRawTitle,
		packKind:         packKind,
		rawTitle:         rawTitle,
		seasonRange:      seasonRange,
		tailParenBlock:   tailParenBlock,
		tokens:           tokens,
		version:          version,
	}
	p.absorbPassResults(st)
	p.resolveTitleAndAliases(st)
	p.detectTailGroups(st)
	p.inferContext(st)
	p.matchInitialTemplate(st)
	p.reclassifyAfterAdjudication(st)
	p.cleanTitleAndAliases(st)
	p.applyGroupRoute(st)
	p.canonicalizeAttributes(st)
	p.finalizeEpisodeSignals(st)
	p.consumeTokens(st)

	epSemantics := buildCanonicalEPSemantics(st.originalRawTitle, st.tokens, st.epslot, st.season, st.seasonRange, st.episode, st.episodeList, st.episodeRange, st.spKind)
	if isUnknownOutput(st.contentKind, st.templateID) {
		epSemantics = nil
	}

	res := &model.ParseResult{
		RawTitle:           st.originalRawTitle,
		ContentKind:        st.contentKind,
		ContentConfidence:  st.contentConf,
		TemplateID:         st.templateID,
		TemplateConfidence: st.templateConf,
		EPSemantics:        epSemantics,
		AudioSlot:          st.ctx.AudioSlot,
		Attributes:         st.attrs,
		Tokens:             st.tokens,
	}

	return res, nil
}

// absorbPassResults copies what the registered passes left in the parseContext into the state.
func (p *Parser) absorbPassResults(st *parseState) {
	st.attrs = pruneGroupSpanTechArtifacts(st.attrs, st.groupSpan)
	// PackKind may be refined/overridden by parse passes (e.g. contents markers like TV+OAD).
	st.packKind = strings.TrimSpace(st.ctx.PackKind)
	if hasAttrNormalized(st.attrs, model.AttrNote, "TV_PACK") {
		st.packKind = "TV_PACK"
	}
	st.season = st.ctx.Season
	st.seasonIdx = st.ctx.SeasonIdx
	st.seasonRange = st.ctx.SeasonRange
	st.spKind = st.ctx.SPKind
	st.movieIndex = st.ctx.MovieIndex
	st.movieIndexIdx = st.ctx.MovieIndexIdx
	st.movieIndexRange = st.ctx.MovieIndexRange
	st.hasMovieKeyword = st.ctx.HasMovieKeyword
	st.episodeRange = st.ctx.EpisodeRange
	st.episodeRangeIdx = st.ctx.EpisodeRangeIdx
	st.episodeList = st.ctx.EpisodeList
	st.episode = st.ctx.Episode
	st.episodeIdx = st.ctx.EpisodeIdx
	st.epslot = st.ctx.EPSlot
}

// resolveTitleAndAliases decides the title span, the title and its aliases, and records the title,
// version and movie-index attributes.
func (p *Parser) resolveTitleAndAliases(st *parseState) {

	seasonCutIdx := -1
	// Only cut title at plain Sxx season tokens that are part of the main title stream.
	// Do NOT cut at "Season" tokens which are often inside parentheses (we remove the whole paren block instead).
	if st.seasonIdx >= 0 && st.seasonIdx < len(st.tokens) {
		if !isInBracket(st.tokens, st.seasonIdx) && !isInParen(st.tokens, st.seasonIdx) {
			if seasonOnlyPattern.MatchString(st.tokens[st.seasonIdx].Text) || seasonEmbeddedPattern.MatchString(st.tokens[st.seasonIdx].Text) {
				seasonCutIdx = st.seasonIdx
			}
		}
	}
	if st.season != "" && st.episode == "" && st.episodeRange == "" {
		for i, token := range st.tokens {
			if token.Protected || isInBracket(st.tokens, i) || isInParen(st.tokens, i) {
				continue
			}
			if m := seasonOnlyPattern.FindStringSubmatch(token.Text); len(m) == 2 && strings.TrimLeft(m[1], "0") == st.season {
				seasonCutIdx = i
				break
			}
		}
	}

	if shouldDemoteEpisodeToMovieIndex(st.rawTitle, st.episode, st.episodeRange, st.season, st.seasonRange, st.packKind, st.hasMovieKeyword, st.movieIndex, st.movieIndexRange) {
		if strings.TrimSpace(st.movieIndex) == "" {
			st.movieIndex = strings.TrimSpace(st.episode)
			st.movieIndexIdx = st.episodeIdx
		}
		st.episode = ""
		st.episodeIdx = -1
		st.epslot = nil
	}

	// Use the episode/season anchors to bound the candidate title slot before
	// generic bracket cutoffs run. Repeated brackets across aliases belong to it.
	titleSlotEnd := len(st.tokens)
	for _, idx := range []int{st.episodeIdx, st.episodeRangeIdx, seasonCutIdx} {
		if idx >= 0 && idx < titleSlotEnd {
			titleSlotEnd = idx
		}
	}
	protectRepeatedTitleBrackets(st.tokens, titleStartAfterLeadingEmptyBracketBlocks(st.tokens), titleSlotEnd)

	_, titleEnd0 := p.inferTitleTokenSpan(st.tokens, st.episodeIdx, st.episodeRangeIdx, seasonCutIdx)
	parenAliases := p.extractParenAliasesAndRemoveFromTitle(st.tokens, titleEnd0, st.episodeIdx, st.episodeRangeIdx, &st.attrs)

	st.title = p.extractTitle(st.tokens, st.episodeIdx, st.episodeRangeIdx, seasonCutIdx)
	originalTitleSpan := strings.TrimSpace(st.title)
	if strings.TrimSpace(st.season) == "" {
		if s := extractExplicitSeasonNumber(originalTitleSpan); s != "" {
			st.season = s
		} else if s, idx := detectSeasonKeywordAny(st.tokens); s != "" {
			st.season = s
			st.seasonIdx = idx
		}
	}
	st.title, st.aliases = inferTitleAndAliasesFromTitleCanonical(st.title, p.isValidAliasToken)
	st.title, st.aliases = restorePartSuffixFromOriginalTitle(originalTitleSpan, st.title, st.aliases)
	st.titleStart, st.titleEnd = p.inferTitleTokenSpan(st.tokens, st.episodeIdx, st.episodeRangeIdx, seasonCutIdx)
	pipeScanStart := titleStartAfterLeadingEmptyBracketBlocks(st.tokens)
	if len(st.groupSpan) == 2 && st.groupSpan[1] > pipeScanStart {
		pipeScanStart = st.groupSpan[1]
	}
	pipeScanEnd := len(st.tokens)
	if st.episodeRangeIdx >= 0 && st.episodeRangeIdx < pipeScanEnd {
		pipeScanEnd = st.episodeRangeIdx
	}
	if st.episodeIdx >= 0 && st.episodeIdx < pipeScanEnd {
		pipeScanEnd = st.episodeIdx
	}
	if seasonCutIdx >= 0 && seasonCutIdx < pipeScanEnd {
		pipeScanEnd = seasonCutIdx
	}
	if techStart, ok := firstTechTokenIndexFromAttrs(st.attrs); ok && techStart >= 0 && techStart < pipeScanEnd {
		// Do not let early pre-title tech/meta tokens (e.g. slot2 platform markers in bracket)
		// block alias-chain extraction in the main title span.
		if st.titleStart < 0 || techStart > st.titleStart {
			pipeScanEnd = techStart
		}
	}
	if pipeTitle, pipeAliases, ok := p.extractPreEpisodePipeAliasesAndConsume(st.tokens, pipeScanStart, pipeScanEnd); ok {
		if strings.TrimSpace(pipeTitle) != "" {
			st.title = pipeTitle
		}
		st.aliases = mergeAliases(pipeAliases, st.aliases)
	}
	st.aliases = mergeAliases(st.ctx.ForcedAliases, st.aliases)
	if strings.TrimSpace(st.title) == "" {
		scanStart := 0
		if len(st.groupSpan) == 2 && st.groupSpan[1] > scanStart {
			scanStart = st.groupSpan[1]
		}
		if fbTitle, fbAliases, fbStart, fbEnd := p.inferBracketTitleFallback(st.tokens, scanStart, st.episodeIdx, st.episodeRangeIdx); strings.TrimSpace(fbTitle) != "" {
			st.title = fbTitle
			st.aliases = mergeAliases(st.aliases, fbAliases)
			st.titleStart = fbStart
			st.titleEnd = fbEnd
		}
	}
	if strings.TrimSpace(st.ctx.ForcedTitle) != "" {
		ft := st.ctx.ForcedTitle
		// If forced title contains strong delimiters (e.g. from bracket title extraction),
		// still infer main title + aliases deterministically.
		if strings.ContainsAny(ft, "/／|｜;；\\") {
			ftMain, ftAliases := inferTitleAndAliasesFromTitleCanonical(ft, p.isValidAliasToken)
			if strings.TrimSpace(ftMain) != "" {
				st.title = ftMain
				st.aliases = mergeAliases(st.aliases, ftAliases)
			} else {
				st.title = ft
			}
		} else {
			st.title = ft
		}
		if st.ctx.ForcedTitleStart >= 0 && st.ctx.ForcedTitleEnd > st.ctx.ForcedTitleStart {
			st.titleStart = st.ctx.ForcedTitleStart
			st.titleEnd = st.ctx.ForcedTitleEnd
		}
	}
	st.title = trimMovieDashEpisodeTailFromTitle(st.rawTitle, st.title, st.episode, st.movieIndex)
	if st.titleStart >= 0 && st.titleEnd > st.titleStart {
		techStart, hasTechStart := firstTechTokenIndexFromAttrs(st.attrs)
		if hasTechStart {
			if techStart < 0 {
				techStart = 0
			}
			if techStart > len(st.tokens) {
				techStart = len(st.tokens)
			}
			if techStart > st.titleStart && st.titleEnd > techStart {
				st.titleEnd = techStart
			}
		}
	}
	if st.titleStart >= 0 && st.titleEnd > st.titleStart {
		st.attrs = append(st.attrs, model.Attribute{Type: model.AttrTitle, Normalized: st.title, TokenStart: st.titleStart, TokenEnd: st.titleEnd, Confidence: 0.7, Source: model.AttrSourceHeuristic})
		st.aliases = mergeAliases(st.aliases, p.inferAliasesFromAdjacentBrackets(st.tokens, st.titleEnd, st.episodeIdx, st.episodeRangeIdx))
		st.aliases = mergeAliases(st.aliases, p.inferTitleGapSingleSlashAlias(st.tokens, st.title, st.titleEnd, st.episodeIdx, st.episodeRangeIdx))
		st.aliases = mergeAliases(st.aliases, p.inferTitleGapDashAlias(st.tokens, st.title, st.titleEnd, st.episodeIdx, st.episodeRangeIdx))
		st.aliases = mergeAliases(st.aliases, p.inferTitleGapBilingualRunAlias(st.tokens, st.title, st.titleEnd, st.episodeIdx, st.episodeRangeIdx))
		st.title, st.aliases = p.promoteInlineBracketContinuationIntoTitle(st.tokens, st.title, st.aliases, st.titleEnd, st.episodeIdx, st.episodeRangeIdx, st.attrs)
	}
	st.aliases = mergeAliases(st.aliases, parenAliases)
	st.aliases = mergeAliases(st.aliases, inferPostEpisodeSearchHintAliasesFromParens(st.tokens, st.episodeIdx, st.episodeRangeIdx, &st.attrs))
	if strings.TrimSpace(st.ctx.EpisodeVersion) != "" {
		// Align with V1: use generic AttrVersion.
		norm := strings.TrimSpace(st.ctx.EpisodeVersion)
		if !strings.HasPrefix(strings.ToLower(norm), "v") {
			norm = "v" + norm
		}
		if st.ctx.EpisodeVersionIdx >= 0 {
			st.attrs = append(st.attrs, model.Attribute{Type: model.AttrVersion, Normalized: norm, TokenStart: st.ctx.EpisodeVersionIdx, TokenEnd: st.ctx.EpisodeVersionIdx + 1, Confidence: 0.7, Source: model.AttrSourceEpisodeVersion})
		} else {
			st.attrs = append(st.attrs, model.Attribute{Type: model.AttrVersion, Normalized: norm, Confidence: 0.65, Source: model.AttrSourceEpisodeVersion})
		}
		if st.version == "" {
			st.version = norm
		}
	}
	if st.movieIndex != "" {
		start := st.movieIndexIdx
		if start < 0 {
			start = -1
		}
		ed := start
		if ed >= 0 {
			ed = start + 1
		}
		st.attrs = append(st.attrs, model.Attribute{Type: model.AttrMovieIndex, Normalized: st.movieIndex, TokenStart: start, TokenEnd: ed, Confidence: 0.7, Source: model.AttrSourceHeuristic})
	}
}

// detectTailGroups looks for a release group at the tail of the title. A tail group found
// before the episode/season anchor is only accepted when another one follows the anchor.
func (p *Parser) detectTailGroups(st *parseState) {
	tg, tgIdx := detectTailGroupForAnchor(st.tokens, st.episodeAnchor())
	if st.group == "" && tg != "" {
		st.group = normalizeGroupText(tg)
		st.attrs = append(st.attrs, model.Attribute{Type: model.AttrGroup, Normalized: st.group, TokenStart: tgIdx, TokenEnd: tgIdx + 1, Confidence: 0.7, Source: model.AttrSourceHeuristic})
	}
	if st.group == "" {
		if rawTail := p.detectTailGroupFromRawTitle(st.rawTitle); rawTail != "" {
			st.group = normalizeGroupText(rawTail)
			st.attrs = append(st.attrs, model.Attribute{Type: model.AttrGroup, Normalized: st.group, TokenStart: -1, TokenEnd: -1, Confidence: 0.65, Source: model.AttrSourceHeuristicRaw})
			st.attrs = append(st.attrs, model.Attribute{Type: model.AttrTailGroup, Normalized: st.group, TokenStart: -1, TokenEnd: -1, Confidence: 0.65, Source: model.AttrSourceHeuristicRaw})
		}
	}
	if tg != "" {
		st.attrs = append(st.attrs, model.Attribute{Type: model.AttrTailGroup, Normalized: normalizeGroupText(tg), TokenStart: tgIdx, TokenEnd: tgIdx + 1, Confidence: 0.7, Source: model.AttrSourceHeuristic})
	}
}

// inferContext derives the tech/year flags, aliases and attributes that come from the tail of the
// title and from raw parentheses, and settles the special and pack kinds.
func (p *Parser) inferContext(st *parseState) {

	st.hasTech = hasTechAttr(st.attrs)
	st.hasYear = false
	for _, a := range st.attrs {
		if a.Type == model.AttrYear {
			if strings.TrimSpace(a.Normalized) != "" {
				st.hasYear = true
				break
			}
		}
	}
	if st.hasTech {
		st.aliases = mergeAliases(st.aliases, p.inferTailAliasesFromPipes(st.tokens, st.attrs, st.group))
		st.aliases = mergeAliases(st.aliases, p.inferTailAliasesFromParens(st.tokens, &st.attrs, st.episodeIdx, st.episodeRangeIdx, st.seasonIdx))
	}
	if len(st.aliases) > 0 {
		var contentMarkers []string
		st.aliases, contentMarkers = extractContentsFromAliases(st.aliases)
		if len(contentMarkers) > 0 {
			emitContentsAttrs(&st.attrs, contentMarkers, model.AttrSourceAliasContentsMarker)
		}
	}
	if rawMarkers := extractContentsFromRawParens(st.rawTitle); len(rawMarkers) > 0 {
		emitContentsAttrs(&st.attrs, rawMarkers, model.AttrSourceRawParenContentsMarker)
	}
	st.attrs = appendEmbeddedJapaneseDubAttrs(st.attrs, st.rawTitle)
	st.attrs = appendEmbeddedChineseDubAttrs(st.attrs, st.rawTitle)
	st.attrs = appendEmbeddedEnglishSubAttrs(st.attrs, st.rawTitle)
	st.attrs = dropContentsAttrsInsideTitleSpan(st.attrs, st.titleStart, st.titleEnd)
	if strings.TrimSpace(st.spKind) == "" {
		st.spKind = inferSPKindFromContentsAttrs(st.attrs)
	}
	if strings.TrimSpace(st.packKind) == "" {
		st.packKind = inferPackKindFromContentsAttrs(st.packKind, st.attrs)
	}
	if strings.TrimSpace(st.packKind) == "" {
		if strings.TrimSpace(st.episode) == "" && strings.TrimSpace(st.episodeRange) == "" {
			if reParse1.MatchString(st.rawTitle) {
				st.packKind = "bd"
			}
		}
	}
	if !st.hasMovieKeyword && hasMovieKeywordInRawTitle(st.rawTitle) {
		st.hasMovieKeyword = true
	}
	if shouldDropEpisodeRangeForMovieRange(st.rawTitle, st.movieIndexRange, st.episode, st.episodeRange) {
		st.clearEpisodeSignals()
	}
}

// matchInitialTemplate classifies the content, matches a template, and lets the
// template's signals adjudicate group, episode and season.
func (p *Parser) matchInitialTemplate(st *parseState) {
	p.reclassify(st)
	p.normalizeMovieSignals(st)
	st.templateID, _, _ = matchTemplate(p.cfg, st.epRoute, st.contentKind, st.group, st.episode, st.episodeRange, st.season, st.packKind, st.spKind, st.movieIndex, st.hasTech, st.hasMovieKeyword, st.attrs, st.tokens)
	var signalEvents []string
	st.attrs, st.group, st.episode, st.episodeIdx, st.episodeRange, st.episodeRangeIdx, st.season, st.seasonIdx, signalEvents = applyTemplateSignalAdjudication(
		p.cfg,
		st.templateID,
		st.tokens,
		st.attrs,
		st.group,
		st.episode,
		st.episodeIdx,
		st.episodeRange,
		st.episodeRangeIdx,
		st.season,
		st.seasonIdx,
	)
	if os.Getenv("TITLEPARSER_TEMPLATE_SIGNAL_TRACE") == "1" {
		for _, ev := range signalEvents {
			if strings.TrimSpace(ev) == "" {
				continue
			}
			st.attrs = append(st.attrs, model.Attribute{
				Type:       model.AttrNote,
				Normalized: "template_signal:" + ev,
				TokenStart: -1,
				TokenEnd:   -1,
				Confidence: 0.6,
				Source:     model.AttrSourceTemplateSignalTrace,
			})
		}
	}
}

// reclassifyAfterAdjudication re-runs the classification on what the adjudication changed
// and settles the final template and route.
func (p *Parser) reclassifyAfterAdjudication(st *parseState) {
	p.reclassify(st)
	if shouldDropEpisodeRangeForMovieRange(st.rawTitle, st.movieIndexRange, st.episode, st.episodeRange) {
		st.clearEpisodeSignals()
		p.reclassify(st)
	}
	p.normalizeMovieSignals(st)
	st.templateID, st.templateConf, st.routeID = matchTemplate(p.cfg, st.epRoute, st.contentKind, st.group, st.episode, st.episodeRange, st.season, st.packKind, st.spKind, st.movieIndex, st.hasTech, st.hasMovieKeyword, st.attrs, st.tokens)
	st.contentKind = normalizeContentKindByTemplateID(st.contentKind, st.templateID)
	if strings.HasPrefix(st.routeID, "streaming_release_") {
		p.applyStreamingReleaseRouteV1(st.tailParenBlock, &st.aliases, &st.attrs)
	}
}

// cleanTitleAndAliases strips tails and noise from the title and aliases before group routing.
func (p *Parser) cleanTitleAndAliases(st *parseState) {
	st.title = trimMovieRangeStartTailFromTitle(st.rawTitle, st.title, st.movieIndexRange, st.episode, st.episodeRange)
	origTitle := st.title
	st.title = sanitizePrimaryTitleForOutput(st.title)
	st.title = trimTitleTailByPackKind(st.title, st.packKind)
	st.title = trimAliasTailBySPKind(st.title, st.spKind)
	st.aliases = p.sanitizeAliasesForOutput(st.aliases, st.title, st.episode, st.season, st.spKind)
	st.title, st.aliases = p.rebalanceGenericPrimaryTitleFromAliases(st.title, st.aliases)
	if bridgedTitle, bridgedAliases, ok := applyBracketUnderscoreAliasBridge(st.rawTitle, st.title, st.aliases); ok {
		st.title, st.aliases = bridgedTitle, bridgedAliases
	}
	if strings.TrimSpace(st.title) != "" && aliasSemanticKey(st.title) != aliasSemanticKey(origTitle) {
		st.attrs = upsertTitleAttribute(st.attrs, st.title, model.AttrSourceTitleCleanup)
	}
}

// applyGroupRoute applies the per-group route overrides (group_route_overrides.yaml) and the
// extra handling that two of those routes need.
func (p *Parser) applyGroupRoute(st *parseState) {
	var overrideApplied bool
	st.group, st.title, st.aliases, st.templateID, st.contentKind, st.routeID, overrideApplied = p.applyGroupRouteOverride(st.rawTitle, st.group, st.episodeRange, st.title, st.aliases, st.templateID, st.contentKind, st.routeID)
	st.title = trimTitleTailByGroup(st.title, st.group)
	for _, a := range st.attrs {
		if a.Type != model.AttrTailGroup {
			continue
		}
		if strings.TrimSpace(a.Normalized) == "" {
			continue
		}
		st.title = trimTitleTailByGroup(st.title, a.Normalized)
	}
	st.title = trimAliasTailBySPKind(st.title, st.spKind)
	if overrideApplied && strings.HasPrefix(st.routeID, "group_epseg_") {
		p.applyEpSegRoute(st)
	}
	if overrideApplied && strings.EqualFold(strings.TrimSpace(st.routeID), "group_aptx4869_conan_bracket_ep_v1") {
		p.applyAPTX4869ConanRoute(st)
	}
	if overrideApplied {
		st.contentKind = normalizeContentKindByTemplateID(st.contentKind, st.templateID)
		st.title, st.aliases = applyGroupTitleAliasSpecialization(st.rawTitle, st.routeID, st.title, st.aliases)
		if strings.TrimSpace(st.title) != "" {
			st.attrs = upsertTitleAttribute(st.attrs, st.title, model.AttrSourceGroupRouteOverride)
		}
		st.attrs = upsertGroupAttribute(st.attrs, st.group, model.AttrSourceGroupRouteOverride)
	}
}

// applyEpSegRoute takes the episode (and special kind) from the third bracket for the
// group_epseg_* routes, then re-classifies and re-matches the template.
func (p *Parser) applyEpSegRoute(st *parseState) {
	ep, sp, epTitle, ok := extractEpisodeLikeFromThirdBracketDetailed(st.rawTitle)
	if !ok {
		return
	}
	st.episode = ep
	if strings.TrimSpace(sp) != "" {
		st.spKind = sp
	}
	isMarukoRoute := strings.EqualFold(strings.TrimSpace(st.routeID), "group_epseg_maruko_v1")
	st.aliases = pruneEpisodeLikeAliases(st.aliases)
	axis := model.EPAxisABS
	if strings.TrimSpace(st.season) != "" {
		axis = model.EPAxisSE
	}
	primary := &model.EPEntity{
		Kind:       model.EPKindEP,
		Axis:       axis,
		Episode:    st.episode,
		SPKind:     st.spKind,
		TokenStart: -1,
		TokenEnd:   -1,
		Source:     model.EPSourceGroupRouteOverride,
	}
	if isMarukoRoute {
		st.epslot = newEPSlot(primary)
		tailTitle := strings.TrimSpace(epTitle)
		if tailTitle == "" {
			tailTitle = strings.TrimSpace(extractTailBetweenThirdAndFourthBracket(st.rawTitle))
		}
		if tailTitle != "" {
			st.attrs = append(st.attrs, groupRouteNote(tailTitle))
		}
		if dm := bracketDatePattern.FindStringSubmatch(st.rawTitle); len(dm) > 1 && strings.TrimSpace(dm[1]) != "" {
			st.attrs = append(st.attrs, groupRouteNote(strings.TrimSpace(dm[1])))
		}
	} else if st.epslot == nil || st.epslot.Primary == nil || strings.TrimSpace(st.epslot.Primary.Episode) == "" {
		st.epslot = newEPSlot(primary)
	}
	p.reclassify(st)
	p.rematchTemplate(st)
}

// applyAPTX4869ConanRoute takes title and episode from the bracketed episode title of the
// APTX4869 Conan route, then re-classifies and re-matches the template.
func (p *Parser) applyAPTX4869ConanRoute(st *parseState) {
	forcedTitle, ep, epTitle, ok := extractAPTX4869ConanEpisodeTitleRoute(st.rawTitle)
	if !ok {
		return
	}
	st.title = forcedTitle
	st.episode = ep
	st.episodeIdx = -1
	st.episodeList = ""
	st.episodeRange = ""
	st.episodeRangeIdx = -1
	st.aliases = pruneEpisodeLikeAliases(st.aliases)
	if strings.TrimSpace(epTitle) != "" {
		st.attrs = append(st.attrs, groupRouteNote(strings.TrimSpace(epTitle)))
	}
	st.epslot = newEPSlot(&model.EPEntity{
		Kind:       model.EPKindEP,
		Axis:       model.EPAxisABS,
		Episode:    st.episode,
		TokenStart: -1,
		TokenEnd:   -1,
		Source:     model.EPSourceGroupRouteOverride,
	})
	p.reclassify(st)
	p.rematchTemplate(st)
}

// canonicalizeAttributes writes the final title, group and scalar attributes and normalizes the
// platform, source, audio and subtitle values.
func (p *Parser) canonicalizeAttributes(st *parseState) {
	// Group-route overrides may inject raw alias candidates; run final alias/title cleanup once more.
	st.aliases = p.sanitizeAliasesForOutput(st.aliases, st.title, st.episode, st.season, st.spKind)
	st.title, st.aliases = rebalanceSingleSlashSeasonBridgePrimaryFromRaw(st.rawTitle, st.title, st.aliases)
	st.title, st.aliases = p.rebalanceGenericPrimaryTitleFromAliases(st.title, st.aliases)
	st.title, st.aliases = p.applyTitleKnowledge(st.rawTitle, st.title, st.aliases)
	prefixStart := st.groupSpan[1]
	if prefixStart < 0 || prefixStart >= len(st.tokens) {
		prefixStart = 0
	}
	prefixEnd := len(st.tokens)
	if st.tokens[prefixStart].Text == "[" || st.tokens[prefixStart].Text == "【" {
		for i := prefixStart + 1; i < len(st.tokens); i++ {
			if st.tokens[i].Text == "]" || st.tokens[i].Text == "】" {
				prefixEnd = i + 1
				break
			}
		}
	} else {
		for _, idx := range []int{st.episodeIdx, st.episodeRangeIdx} {
			if idx >= prefixStart && idx < prefixEnd {
				prefixEnd = idx
			}
		}
	}
	st.title, st.aliases = restoreRepeatedTitlePrefix(st.tokens, prefixStart, prefixEnd, st.title, st.aliases)
	packProfile := p.inferPackProfile(st.contentKind, st.packKind, st.season, st.seasonRange, st.attrs)
	st.attrs = upsertTitleAttribute(st.attrs, st.title, model.AttrSourceCanonical)
	st.attrs = upsertGroupAttribute(st.attrs, st.group, model.AttrSourceCanonical)
	st.attrs = upsertScalarAttribute(st.attrs, model.AttrVersion, st.version, model.AttrSourceCanonical)
	st.attrs = upsertScalarAttribute(st.attrs, model.AttrPackKind, st.packKind, model.AttrSourceCanonical)
	st.attrs = upsertScalarAttribute(st.attrs, model.AttrPackProfile, packProfile, model.AttrSourceCanonical)
	st.attrs = upsertScalarAttribute(st.attrs, model.AttrMovieIndex, st.movieIndex, model.AttrSourceCanonical)
	st.attrs = upsertScalarAttribute(st.attrs, model.AttrMovieIndexRange, st.movieIndexRange, model.AttrSourceCanonical)
	st.attrs = appendTitleAliasAttributes(st.attrs, st.aliases, st.title, model.AttrSourceCanonical)
	st.attrs = p.promoteNoteAttributes(st.attrs)
	st.attrs = dropRedundantNoteAttributes(st.attrs, st.season, st.seasonRange)
	p.normalizePlatformAttrs(st.attrs)
	p.normalizeSourceTypeAttrs(st.attrs)
	p.normalizeAudioAttrs(st.attrs)
	st.attrs = p.normalizeSubtitleAttrs(st.attrs, st.tokens)
	st.attrs = p.enrichBatchSourceTypeFromPackKind(st.attrs, st.contentKind)
	st.attrs = reconcileFileExtensionContainer(st.attrs)
	st.attrs = dedupeAttributes(st.attrs)
	st.attrs = collapseOverlappingSpecificAudioCodecs(st.attrs)
	st.ctx.AudioSlot = reconcileAudioSlotWithAttributes(st.ctx.AudioSlot, st.attrs)
	normalizeAudioSlotLanguages(st.ctx.AudioSlot)
}

// finalizeEpisodeSignals reconciles the episode slot with the canonical signals and prunes episode
// data that does not belong to the content kind.
func (p *Parser) finalizeEpisodeSignals(st *parseState) {
	st.epslot = syncEPSlotWithCanonicalSignals(st.epslot, st.season, st.episode, st.episodeRange, st.episodeList, st.spKind, st.episodeIdx, st.episodeRangeIdx, st.ctx.EpisodeListIdx)
	st.epslot = pruneBatchEPSlotForConservativeOutput(st.epslot, st.contentKind, st.episode, st.episodeRange, st.spKind)
	if shouldPreferSPPrimaryForSpecial(st.contentKind, st.spKind, st.epslot) {
		st.episode = ""
		st.episodeIdx = -1
		st.episodeList = ""
		st.ctx.EpisodeListIdx = -1
		st.episodeRange = ""
		st.episodeRangeIdx = -1
		st.epslot = forceSPPrimaryFromSPKind(st.epslot, st.season, st.spKind)
	}
	// A single video file is not a season pack: a file that names a season
	// but no episode, range or list of its own is unknown. A file that spans
	// a range (one recording of "241-248") keeps its batch classification.
	if st.contentKind.Canonical() == model.ContentKindBatch && hasFileExtensionContainer(st.attrs) &&
		st.season != "" && st.episode == "" && st.episodeRange == "" && st.episodeList == "" {
		st.contentKind = model.ContentKindUnknown
		st.templateID = "TF_UNKNOWN_DEFAULT"
	}
	st.contentKind, st.episode, st.episodeList, st.episodeRange, st.epslot = pruneCanonicalEpisodeSignalsForOutput(st.rawTitle, st.contentKind, st.packKind, st.season, st.episode, st.episodeList, st.episodeRange, st.spKind, st.movieIndex, st.movieIndexRange, st.epslot)
	if isUnknownOutput(st.contentKind, st.templateID) {
		st.attrs = pruneUnknownCoreAttributes(st.attrs)
		st.epslot = nil
		st.ctx.AudioSlot = nil
	}
}

// consumeTokens marks the tokens covered by the final attributes as consumed.
func (p *Parser) consumeTokens(st *parseState) {
	consumeTokensByTechAttrSpans(st.tokens, st.attrs)
	consumeTokensByEPSlotPrimarySpan(st.tokens, st.epslot)
	finalTitle := finalTitleValueFromAttributes(st.attrs, st.title)
	finalAliases := finalTitleAliasValuesFromAttributes(st.attrs)
	p.consumeTitleGapSeasonBridgeMarkers(st.tokens, st.titleEnd, st.episodeIdx, st.episodeRangeIdx, st.attrs)
	p.consumeTitleGapLatinAliasRuns(st.tokens, finalTitle, finalAliases, st.titleEnd, st.episodeIdx, st.episodeRangeIdx, st.attrs)
	p.consumeTitleGapAliasSemanticTokens(st.tokens, finalAliases, st.titleEnd, st.episodeIdx, st.episodeRangeIdx, st.attrs)
	p.consumeTitleGapBatchTVMarkers(st.tokens, st.titleEnd, st.episodeIdx, st.episodeRangeIdx, st.attrs)
	p.consumeAliasChainTokensByFinalAliases(st.tokens, st.groupSpan, finalTitle, finalAliases, st.episodeIdx, st.episodeRangeIdx, st.seasonIdx, st.attrs)
	p.consumeGroupSpecificTitleChainDelimiters(st.tokens, st.groupSpan, st.group, st.episodeIdx, st.episodeRangeIdx, st.attrs)
	consumeTailGroupTokensByCanonicalGroup(st.tokens, st.group)
	removeBracketsAroundFullyConsumedBlocks(st.tokens)
	st.attrs = stripLegacyEPAttributes(st.attrs)
}

// Regular expressions hoisted out of function bodies so they are compiled once at init.
var (
	reParse1 = regexp.MustCompile(`(?i)\bBD\b`)
)
