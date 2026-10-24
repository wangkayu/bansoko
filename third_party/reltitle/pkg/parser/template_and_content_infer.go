package parser

import (
	"sort"
	"strings"

	"github.com/wangkayu/reltitle/pkg/config"
	"github.com/wangkayu/reltitle/pkg/model"
)

func inferContentKind(cfg *config.Config, episode string, episodeRange string, season string, seasonRange string, packKind string, spKind string, movieIndex string, movieIndexRange string, hasTech bool, hasMovieKeyword bool, hasYear bool, movieRouteHardExcluded bool, hasMovieContentSignal bool) (model.ContentKind, float64) {
	hasEpisode := episode != ""
	hasEpisodeRange := episodeRange != ""
	hasSeason := season != ""
	hasSeasonRange := seasonRange != ""
	hasPackKind := packKind != ""
	hasSPKind := spKind != ""
	hasMovieIndex := movieIndex != ""
	hasMovieIndexRange := movieIndexRange != ""

	if hasMovieIndexRange {
		return model.ContentKindBatch, 0.95
	}

	if movieRouteHardExcluded && hasMovieContentSignal {
		return model.ContentKindBatch, 0.95
	}

	// Season-scoped specials like "Season 2 - SP" are still SPECIAL, not season packs.
	if hasSPKind && !hasPackKind && !hasEpisode && !hasEpisodeRange && !hasSeasonRange {
		return model.ContentKindSpecial, 0.85
	}

	rules := append([]config.ContentTypeInferenceRule(nil), cfg.Inference.ContentTypeInference...)
	sort.SliceStable(rules, func(i, j int) bool { return rules[i].Priority > rules[j].Priority })

	for _, r := range rules {
		if matchContentTypeCondition(r, hasEpisode, hasEpisodeRange, hasSeason, hasSeasonRange, hasPackKind, hasSPKind, hasMovieIndex, hasMovieIndexRange, hasTech, hasMovieKeyword, hasYear, spKind) {
			return model.ContentKind(r.Result), r.Confidence
		}
	}

	return model.ContentKindUnknown, 0.5
}

func matchContentTypeCondition(r config.ContentTypeInferenceRule, hasEpisode bool, hasEpisodeRange bool, hasSeason bool, hasSeasonRange bool, hasPackKind bool, hasSPKind bool, hasMovieIndex bool, hasMovieIndexRange bool, hasTech bool, hasMovieKeyword bool, hasYear bool, spKind string) bool {
	cond := r.Condition
	if cond == nil {
		return false
	}
	if cond.Default != nil && *cond.Default {
		return true
	}
	// HasX requires the flag to equal the value; NoX requires it to differ.
	pairs := []struct {
		want *bool
		got  bool
		neg  bool // true for the no_* keys
	}{
		{cond.HasEpisode, hasEpisode, false},
		{cond.NoEpisode, hasEpisode, true},
		{cond.HasEpisodeRange, hasEpisodeRange, false},
		{cond.NoEpisodeRange, hasEpisodeRange, true},
		{cond.HasSeason, hasSeason, false},
		{cond.HasSeasonRange, hasSeasonRange, false},
		{cond.HasTech, hasTech, false},
		{cond.HasMovieKeyword, hasMovieKeyword, false},
		{cond.NoMovieKeyword, hasMovieKeyword, true},
		{cond.HasPackKind, hasPackKind, false},
		{cond.HasSPKind, hasSPKind, false},
		{cond.HasMovieIndex, hasMovieIndex, false},
		{cond.HasMovieIndexRange, hasMovieIndexRange, false},
		{cond.NoMovieIndexRange, hasMovieIndexRange, true},
		{cond.HasYear, hasYear, false},
		{cond.NoYear, hasYear, true},
	}
	for _, p := range pairs {
		if p.want == nil {
			continue
		}
		if !p.neg && *p.want != p.got {
			return false
		}
		if p.neg && *p.want == p.got {
			return false
		}
	}
	if cond.SPKind != "" {
		if !strings.EqualFold(cond.SPKind, spKind) {
			return false
		}
	}
	return true
}

func matchTemplate(cfg *config.Config, epRoute string, contentKind model.ContentKind, group string, episode string, episodeRange string, season string, packKind string, spKind string, movieIndex string, hasTech bool, hasMovieKeyword bool, attrs []model.Attribute, tokens []model.Token) (string, float64, string) {
	family := detectRouteFamily(cfg, attrs, tokens)
	epRoute = strings.TrimSpace(epRoute)
	if epRoute == "RANGE" {
		if strings.TrimSpace(episodeRange) == "" {
			return "TF_UNKNOWN_DEFAULT", 0, ""
		}
	}
	if epRoute == "SXXEXX" {
		if strings.TrimSpace(season) == "" || strings.TrimSpace(episode) == "" || !hasSxxExxInTokens(tokens) {
			return "TF_UNKNOWN_DEFAULT", 0, ""
		}
	}
	if epRoute == "ABS_EP" {
		if strings.TrimSpace(episode) == "" {
			return "TF_UNKNOWN_DEFAULT", 0, ""
		}
	}
	if epRoute == "PLAIN_EP" {
		if strings.TrimSpace(episode) == "" {
			return "TF_UNKNOWN_DEFAULT", 0, ""
		}
	}
	if epRoute == "EP_LIST" {
		if strings.TrimSpace(episode) == "" {
			return "TF_UNKNOWN_DEFAULT", 0, ""
		}
	}
	if epRoute == "MOVIE_ONLY" {
		if !hasMovieKeyword && strings.TrimSpace(movieIndex) == "" {
			return "TF_UNKNOWN_DEFAULT", 0, ""
		}
	}
	if epRoute == "SP_ONLY" {
		if strings.TrimSpace(spKind) == "" {
			return "TF_UNKNOWN_DEFAULT", 0, ""
		}
	}

	admitStreaming := isStreamingRelease(tokens, attrs)
	isBatchContent := contentKind.Canonical() == model.ContentKindBatch
	strictRouteIDs := collectStrictRouteIDs(cfg)
	allowedByFamily := allowedStrictRouteIDsForFamily(family)
	selectBest := func(tpls []config.TemplateDef, enforceEpRoute bool) (string, float64, string) {
		if len(tpls) == 0 {
			return "", 0, ""
		}
		bestID := ""
		bestConf := 0.0
		bestRoute := ""
		bestSpec := -1
		bestSigSpec := -1
		for _, t := range tpls {
			routeIDNorm := strings.ToLower(strings.TrimSpace(t.RouteID))
			if routeIDNorm != "" {
				if _, strict := strictRouteIDs[routeIDNorm]; strict {
					if _, ok := allowedByFamily[routeIDNorm]; !ok {
						continue
					}
				}
			}
			if strings.TrimSpace(t.RouteID) == "streaming_release_v1" {
				if !admitStreaming {
					continue
				}
			}
			if enforceEpRoute && epRoute == "SXXEXX" {
				sigUp := strings.ToUpper(strings.TrimSpace(t.Signature))
				if !strings.Contains(sigUp, " SE") {
					continue
				}
			}
			if !signatureSatisfiable(t.Signature, group, episode, episodeRange, season, packKind, spKind, movieIndex, hasTech, hasMovieKeyword) {
				continue
			}
			if enforceEpRoute && epRoute == "SP_ONLY" {
				sigUp := strings.ToUpper(strings.TrimSpace(t.Signature))
				if !(strings.Contains(sigUp, " SP") || strings.Contains(sigUp, " OVA") || strings.Contains(sigUp, " OAD") || strings.Contains(sigUp, " ONA") || strings.Contains(sigUp, " EX")) {
					continue
				}
			}
			if !templateConditionsSatisfiable(t.Conditions, attrs, tokens) {
				continue
			}
			spec := templateConditionSpecificity(t.Conditions)
			sigSpec := templateSignatureSpecificity(t.Signature)
			replace := false
			if isBatchContent {
				if sigSpec > bestSigSpec ||
					(sigSpec == bestSigSpec && (spec > bestSpec || (spec == bestSpec && t.Confidence > bestConf))) {
					replace = true
				}
			} else {
				if spec > bestSpec || (spec == bestSpec && t.Confidence > bestConf) {
					replace = true
				}
			}
			if replace {
				bestSigSpec = sigSpec
				bestSpec = spec
				bestConf = t.Confidence
				bestID = t.ID
				bestRoute = strings.TrimSpace(t.RouteID)
			}
		}
		return bestID, bestConf, bestRoute
	}

	// Family-level preference:
	// when a high-level family is detected, try its preferred template categories first
	// on ambiguous EP routes before falling back to the default pipeline.
	if family != nil {
		prefCats := preferredTemplateCategories(family)
		strictFilter := strictTemplateFilterEnabled(family)
		allowedRouteIDs := allowedRouteIDSet(family)
		if len(prefCats) > 0 && (epRoute == "ABS_EP" || epRoute == "PLAIN_EP" || epRoute == "EP_LIST" || epRoute == "NONE") {
			for _, cat := range prefCats {
				candidates := templatesByCategory(cfg, cat)
				if strictFilter && len(allowedRouteIDs) > 0 {
					candidates = filterTemplatesByRouteIDs(candidates, allowedRouteIDs)
				}
				if id, conf, route := selectBest(candidates, false); strings.TrimSpace(id) != "" {
					return id, conf, route
				}
			}
		}
	}

	if contentKind.Canonical() == model.ContentKindBatch {
		if id, conf, route := selectBest(cfg.TemplateCatalog.BatchTemplates, false); strings.TrimSpace(id) != "" {
			return id, conf, route
		}
	}

	if contentKind.Canonical() == model.ContentKindUnknown {
		if strings.TrimSpace(episode) == "" &&
			strings.TrimSpace(episodeRange) == "" &&
			strings.TrimSpace(season) == "" &&
			strings.TrimSpace(packKind) == "" &&
			strings.TrimSpace(spKind) == "" &&
			strings.TrimSpace(movieIndex) == "" &&
			!hasTech {
			unknownCandidates := make([]config.TemplateDef, 0, len(cfg.TemplateCatalog.UnknownTemplates))
			hasGroup := strings.TrimSpace(group) != ""
			hasAliasSep := hasUnknownAliasSeparatorSignal(tokens)
			for _, t := range cfg.TemplateCatalog.UnknownTemplates {
				route := strings.TrimSpace(t.RouteID)
				switch route {
				case "unknown_minimal_v1":
					if !hasGroup {
						continue
					}
				case "unknown_t_alias_minimal_v1":
					if hasGroup || !hasAliasSep {
						continue
					}
				}
				unknownCandidates = append(unknownCandidates, t)
			}
			if id, conf, route := selectBest(unknownCandidates, false); strings.TrimSpace(id) != "" {
				return id, conf, route
			}
			if hasGroup {
				if id, conf, route := selectBest(cfg.TemplateCatalog.UnknownTemplates, false); strings.TrimSpace(id) != "" {
					return id, conf, route
				}
			}
		}
		return "TF_UNKNOWN_DEFAULT", 0, ""
	}

	tpls := templatesByEpRoute(cfg, epRoute)
	if len(tpls) == 0 {
		tpls = templatesByContentKind(cfg, contentKind)
	}
	if id, conf, route := selectBest(tpls, true); strings.TrimSpace(id) != "" {
		return id, conf, route
	}
	return "TF_UNKNOWN_DEFAULT", 0, ""
}

func templatesByEpRoute(cfg *config.Config, epRoute string) []config.TemplateDef {
	switch strings.TrimSpace(epRoute) {
	case "RANGE":
		return cfg.TemplateCatalog.BatchTemplates
	case "MOVIE_ONLY":
		return cfg.TemplateCatalog.MovieTemplates
	case "SXXEXX", "ABS_EP", "PLAIN_EP", "EP_LIST", "SP_ONLY":
		return cfg.TemplateCatalog.TVEpisodeTemplates
	case "NONE":
		return nil
	default:
		return nil
	}
}

func templateByID(cfg *config.Config, id string) *config.TemplateDef {
	id = strings.TrimSpace(id)
	if cfg == nil || id == "" {
		return nil
	}
	for i := range cfg.TemplateCatalog.TVEpisodeTemplates {
		t := &cfg.TemplateCatalog.TVEpisodeTemplates[i]
		if strings.TrimSpace(t.ID) == id {
			return t
		}
	}
	for i := range cfg.TemplateCatalog.BatchTemplates {
		t := &cfg.TemplateCatalog.BatchTemplates[i]
		if strings.TrimSpace(t.ID) == id {
			return t
		}
	}
	for i := range cfg.TemplateCatalog.MovieTemplates {
		t := &cfg.TemplateCatalog.MovieTemplates[i]
		if strings.TrimSpace(t.ID) == id {
			return t
		}
	}
	for i := range cfg.TemplateCatalog.SpecialTemplates {
		t := &cfg.TemplateCatalog.SpecialTemplates[i]
		if strings.TrimSpace(t.ID) == id {
			return t
		}
	}
	for i := range cfg.TemplateCatalog.UnknownTemplates {
		t := &cfg.TemplateCatalog.UnknownTemplates[i]
		if strings.TrimSpace(t.ID) == id {
			return t
		}
	}
	return nil
}

func templateConditionSpecificity(cond *config.TemplateConditions) int {
	if cond == nil || cond.Require == nil {
		return 0
	}
	s := 0
	if len(cond.Require.PlatformIn) > 0 {
		s++
	}
	if len(cond.Require.SourceTypeIn) > 0 {
		s++
	}
	if cond.Require.HasSxxExx {
		s++
	}
	if cond.Require.HasTailGroup {
		s++
	}
	return s
}

func hasUnknownAliasSeparatorSignal(tokens []model.Token) bool {
	for _, t := range tokens {
		txt := strings.TrimSpace(t.Text)
		if txt == "/" || txt == "／" || txt == "|" || txt == "｜" || txt == ";" || txt == "；" || txt == "\\" {
			return true
		}
	}
	return false
}

func templateConditionsSatisfiable(cond *config.TemplateConditions, attrs []model.Attribute, tokens []model.Token) bool {
	if cond == nil || cond.Require == nil {
		return true
	}
	// has_sxxexx
	if cond.Require.HasSxxExx {
		if !hasSxxExxInTokens(tokens) {
			return false
		}
	}
	// has_tail_group
	if cond.Require.HasTailGroup {
		ok := false
		for _, a := range attrs {
			if a.Type == model.AttrTailGroup {
				if strings.TrimSpace(a.Normalized) != "" {
					ok = true
					break
				}
			}
		}
		if !ok {
			return false
		}
	}
	// platform_in
	if len(cond.Require.PlatformIn) > 0 {
		plat := ""
		for _, a := range attrs {
			if a.Type != model.AttrPlatform {
				continue
			}
			plat = strings.TrimSpace(a.Normalized)
			break
		}
		if plat == "" {
			return false
		}
		ok := false
		for _, w := range cond.Require.PlatformIn {
			if strings.EqualFold(strings.TrimSpace(w), plat) {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	// source_type_in
	if len(cond.Require.SourceTypeIn) > 0 {
		src := ""
		for _, a := range attrs {
			if a.Type != model.AttrSourceType {
				continue
			}
			src = strings.TrimSpace(a.Normalized)
			break
		}
		if src == "" {
			return false
		}
		ok := false
		for _, w := range cond.Require.SourceTypeIn {
			if strings.EqualFold(strings.TrimSpace(w), src) {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

func templatesByContentKind(cfg *config.Config, contentKind model.ContentKind) []config.TemplateDef {
	// Deliberately an exact match (no Canonical): unknown values fall through to nil.
	switch contentKind {
	case model.ContentKindTVEP:
		return cfg.TemplateCatalog.TVEpisodeTemplates
	case model.ContentKindBatch:
		return cfg.TemplateCatalog.BatchTemplates
	case model.ContentKindMovie:
		return cfg.TemplateCatalog.MovieTemplates
	case "SP", model.ContentKindSpecial:
		return cfg.TemplateCatalog.SpecialTemplates
	case model.ContentKindUnknown:
		return cfg.TemplateCatalog.UnknownTemplates
	default:
		return nil
	}
}

func templatesByCategory(cfg *config.Config, cat string) []config.TemplateDef {
	switch strings.ToLower(strings.TrimSpace(cat)) {
	case "tv_episode", "tv", "tv_ep":
		return cfg.TemplateCatalog.TVEpisodeTemplates
	case "batch":
		return cfg.TemplateCatalog.BatchTemplates
	case "movie":
		return cfg.TemplateCatalog.MovieTemplates
	case "special", "sp":
		return cfg.TemplateCatalog.SpecialTemplates
	case "unknown":
		return cfg.TemplateCatalog.UnknownTemplates
	default:
		return nil
	}
}

func preferredTemplateCategories(rule *config.RouteFamilyRule) []string {
	if rule == nil {
		return nil
	}
	out := make([]string, 0, len(rule.Select.PreferredTemplateCategories))
	seen := map[string]struct{}{}
	for _, c := range rule.Select.PreferredTemplateCategories {
		cc := strings.ToLower(strings.TrimSpace(c))
		if cc == "" {
			continue
		}
		if _, ok := seen[cc]; ok {
			continue
		}
		seen[cc] = struct{}{}
		out = append(out, cc)
	}
	return out
}

func strictTemplateFilterEnabled(rule *config.RouteFamilyRule) bool {
	if rule == nil {
		return false
	}
	return rule.Select.StrictTemplateFilter
}

func allowedRouteIDSet(rule *config.RouteFamilyRule) map[string]struct{} {
	out := map[string]struct{}{}
	if rule == nil {
		return out
	}
	for _, id := range rule.Select.AllowedRouteIDs {
		key := strings.ToLower(strings.TrimSpace(id))
		if key == "" {
			continue
		}
		out[key] = struct{}{}
	}
	return out
}

func filterTemplatesByRouteIDs(tpls []config.TemplateDef, allowed map[string]struct{}) []config.TemplateDef {
	if len(tpls) == 0 || len(allowed) == 0 {
		return tpls
	}
	out := make([]config.TemplateDef, 0, len(tpls))
	for _, t := range tpls {
		routeID := strings.ToLower(strings.TrimSpace(t.RouteID))
		if routeID == "" {
			continue
		}
		if _, ok := allowed[routeID]; !ok {
			continue
		}
		out = append(out, t)
	}
	return out
}

func collectStrictRouteIDs(cfg *config.Config) map[string]struct{} {
	out := map[string]struct{}{}
	if cfg == nil {
		return out
	}
	for _, fam := range cfg.RouteFamilies.Families {
		if !fam.Enabled || !fam.Select.StrictTemplateFilter {
			continue
		}
		for _, rid := range fam.Select.AllowedRouteIDs {
			key := strings.ToLower(strings.TrimSpace(rid))
			if key == "" {
				continue
			}
			out[key] = struct{}{}
		}
	}
	return out
}

func allowedStrictRouteIDsForFamily(rule *config.RouteFamilyRule) map[string]struct{} {
	out := map[string]struct{}{}
	if rule == nil || !rule.Enabled || !rule.Select.StrictTemplateFilter {
		return out
	}
	for _, rid := range rule.Select.AllowedRouteIDs {
		key := strings.ToLower(strings.TrimSpace(rid))
		if key == "" {
			continue
		}
		out[key] = struct{}{}
	}
	return out
}

func detectRouteFamily(cfg *config.Config, attrs []model.Attribute, tokens []model.Token) *config.RouteFamilyRule {
	if cfg == nil || len(cfg.RouteFamilies.Families) == 0 {
		return nil
	}
	rawLower := strings.ToLower(strings.TrimSpace(joinTokensForFamilyMatch(tokens)))
	sourceSet := attrValueSet(attrs, model.AttrSourceType)
	noteSet := attrValueSet(attrs, model.AttrNote)
	rules := append([]config.RouteFamilyRule(nil), cfg.RouteFamilies.Families...)
	sort.SliceStable(rules, func(i, j int) bool {
		return rules[i].Priority > rules[j].Priority
	})
	for i := range rules {
		rule := &rules[i]
		if !rule.Enabled {
			continue
		}
		if !routeFamilyRuleMatches(rule, rawLower, sourceSet, noteSet) {
			continue
		}
		return rule
	}
	return nil
}

func routeFamilyRuleMatches(rule *config.RouteFamilyRule, rawLower string, sourceSet map[string]struct{}, noteSet map[string]struct{}) bool {
	if rule == nil {
		return false
	}
	if len(rule.Match.RawMustContainAny) == 0 && len(rule.Match.SourceTypeIn) == 0 && len(rule.Match.NoteIn) == 0 {
		return false
	}
	if len(rule.Match.RawMustContainAny) > 0 && !containsAnyFold(rawLower, rule.Match.RawMustContainAny) {
		return false
	}
	if len(rule.Match.RawMustNotContainAny) > 0 && containsAnyFold(rawLower, rule.Match.RawMustNotContainAny) {
		return false
	}
	if len(rule.Match.SourceTypeIn) > 0 && !setHasAnyFold(sourceSet, rule.Match.SourceTypeIn) {
		return false
	}
	if len(rule.Match.NoteIn) > 0 && !setHasAnyFold(noteSet, rule.Match.NoteIn) {
		return false
	}
	return true
}

func joinTokensForFamilyMatch(tokens []model.Token) string {
	if len(tokens) == 0 {
		return ""
	}
	parts := make([]string, 0, len(tokens))
	for _, tk := range tokens {
		t := strings.TrimSpace(tk.Text)
		if t == "" {
			continue
		}
		parts = append(parts, t)
	}
	return strings.Join(parts, " ")
}

func attrValueSet(attrs []model.Attribute, t model.AttrType) map[string]struct{} {
	out := map[string]struct{}{}
	for _, a := range attrs {
		if a.Type != t {
			continue
		}
		v := strings.ToUpper(strings.TrimSpace(a.Normalized))
		if v == "" {
			continue
		}
		out[v] = struct{}{}
	}
	return out
}

func containsAnyFold(rawLower string, needles []string) bool {
	for _, n := range needles {
		ns := strings.ToLower(strings.TrimSpace(n))
		if ns == "" {
			continue
		}
		if strings.Contains(rawLower, ns) {
			return true
		}
	}
	return false
}

func setHasAnyFold(set map[string]struct{}, vals []string) bool {
	for _, v := range vals {
		key := strings.ToUpper(strings.TrimSpace(v))
		if key == "" {
			continue
		}
		if _, ok := set[key]; ok {
			return true
		}
	}
	return false
}

func signatureSatisfiable(sig string, group string, episode string, episodeRange string, season string, packKind string, spKind string, movieIndex string, hasTech bool, hasMovieKeyword bool) bool {
	fields := strings.Fields(sig)
	for _, f := range fields {
		switch f {
		case "G":
			if group == "" {
				return false
			}
		case "E":
			if episode == "" {
				return false
			}
		case "R":
			if episodeRange == "" {
				return false
			}
		case "S":
			if season == "" {
				return false
			}
		case "SE":
			if season == "" || episode == "" {
				return false
			}
		case "M":
			if !hasMovieKeyword && movieIndex == "" {
				return false
			}
		case "I":
			if movieIndex == "" {
				return false
			}
		case "X":
			if !hasTech {
				return false
			}
		case "C":
			if packKind == "" {
				return false
			}
		case "SP":
			if spKind == "" {
				return false
			}
		case "OVA", "OAD", "ONA", "EX":
			if strings.ToUpper(spKind) != f {
				return false
			}
		case "T", "CONTENTS":
		default:
		}
	}

	return true
}

func templateSignatureSpecificity(sig string) int {
	fields := strings.Fields(strings.TrimSpace(sig))
	if len(fields) == 0 {
		return 0
	}
	score := 0
	for _, f := range fields {
		switch f {
		case "T", "CONTENTS":
			continue
		default:
			score++
		}
	}
	return score
}
