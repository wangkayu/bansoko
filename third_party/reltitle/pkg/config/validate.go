package config

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Validate checks the loaded configuration for problems that would otherwise make a
// rule silently stop working: invalid regexes, duplicate ids, out-of-range
// confidences, unknown condition keys and dangling references between files.
// All problems are reported together.
func (c *Config) Validate() error {
	v := &validator{}
	v.lexicon(c.Lexicon)
	v.templates(c.TemplateCatalog)
	v.inference(c.Inference)
	v.routeFamilies(c.RouteFamilies, c.TemplateCatalog)
	v.groupRoutes(c.GroupRoute)
	v.groupSlot2(c.GroupSlot2Platform)
	v.packProfiles(c.PackProfileRules)
	v.titleKnowledge(c.TitleKnowledge)
	v.groupList(c.GroupList)
	v.dictionaries(c)
	return errors.Join(v.errs...)
}

// contentTypes are the values a template may declare in content_types.
var contentTypes = map[string]bool{"TV_EP": true, "BATCH": true, "MOVIE": true, "SP": true, "UNKNOWN": true}

// signatureFields are the slot names understood in a template signature
// (see signatureSatisfiable in pkg/parser).
var signatureFields = map[string]bool{
	"G": true, "T": true, "E": true, "R": true, "S": true, "SE": true, "M": true, "I": true,
	"X": true, "C": true, "SP": true, "OVA": true, "OAD": true, "ONA": true, "EX": true, "CONTENTS": true,
}

// (inference rule conditions are strongly typed as ContentTypeInferenceCondition,
// so an unknown key already fails at decode time via KnownFields.)

var lexiconScopes = map[string]bool{"": true, "bracket": true, "paren": true, "plain": true}

type validator struct {
	errs []error
}

func (v *validator) addf(file, format string, args ...any) {
	v.errs = append(v.errs, fmt.Errorf("%s: %s", file, fmt.Sprintf(format, args...)))
}

// unique reports ids that are empty or appear more than once.
func (v *validator) unique(file, kind string, ids []string) {
	seen := make(map[string]int, len(ids))
	for i, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			v.addf(file, "%s #%d has an empty id", kind, i+1)
			continue
		}
		if first, dup := seen[id]; dup {
			v.addf(file, "duplicate %s id %q (entries #%d and #%d)", kind, id, first+1, i+1)
			continue
		}
		seen[id] = i
	}
}

func (v *validator) confidence(file, where string, c float64) {
	if c < 0 || c > 1 {
		v.addf(file, "%s: confidence %v is outside [0, 1]", where, c)
	}
}

func (v *validator) regex(file, where, pattern string) {
	if strings.TrimSpace(pattern) == "" {
		return
	}
	if _, err := regexp.Compile(pattern); err != nil {
		v.addf(file, "%s: invalid regex %q: %v", where, pattern, err)
	}
}

func (v *validator) lexicon(l LexiconConfig) {
	const file = "lexicon.yaml"
	ids := make([]string, len(l.Entries))
	for i, e := range l.Entries {
		ids[i] = e.ID
	}
	v.unique(file, "entry", ids)

	for _, e := range l.Entries {
		if len(e.Tokens) == 0 && len(e.AnyOf) == 0 {
			v.addf(file, "entry %q has neither tokens nor any_of", e.ID)
		}
		if !lexiconScopes[e.Scope] {
			v.addf(file, "entry %q: unknown scope %q", e.ID, e.Scope)
		}
		v.confidence(file, fmt.Sprintf("entry %q", e.ID), e.Confidence)
		v.lexiconEmits(file, e.ID, e.Emit)
		for i, cl := range e.AnyOf {
			where := fmt.Sprintf("%s any_of[%d]", e.ID, i)
			if len(cl.Tokens) == 0 {
				v.addf(file, "entry %s has no tokens", where)
			}
			if !lexiconScopes[cl.Scope] {
				v.addf(file, "entry %s: unknown scope %q", where, cl.Scope)
			}
			v.confidence(file, "entry "+where, cl.Confidence)
			v.lexiconEmits(file, where, cl.Emit)
		}
	}
}

func (v *validator) lexiconEmits(file, where string, emits []LexiconEmit) {
	for _, em := range emits {
		if strings.TrimSpace(em.Type) == "" {
			v.addf(file, "entry %s has an emit without a type", where)
		}
	}
}

func (v *validator) templates(t TemplateCatalogConfig) {
	const file = "template_catalog.yaml"
	all := allTemplates(t)
	ids := make([]string, len(all))
	for i, tpl := range all {
		ids[i] = tpl.ID
	}
	v.unique(file, "template", ids)

	for _, tpl := range all {
		if len(tpl.ContentTypes) == 0 {
			v.addf(file, "template %q has no content_types", tpl.ID)
		}
		for _, ct := range tpl.ContentTypes {
			if !contentTypes[ct] {
				v.addf(file, "template %q: unknown content type %q", tpl.ID, ct)
			}
		}
		for _, f := range strings.Fields(tpl.Signature) {
			if !signatureFields[f] {
				v.addf(file, "template %q: unknown signature field %q", tpl.ID, f)
			}
		}
		v.confidence(file, fmt.Sprintf("template %q", tpl.ID), tpl.Confidence)
	}
}

func allTemplates(t TemplateCatalogConfig) []TemplateDef {
	var all []TemplateDef
	all = append(all, t.TVEpisodeTemplates...)
	all = append(all, t.BatchTemplates...)
	all = append(all, t.MovieTemplates...)
	all = append(all, t.SpecialTemplates...)
	all = append(all, t.UnknownTemplates...)
	return all
}

func (v *validator) inference(in InferenceRulesConfig) {
	const file = "inference_rules.yaml"
	names := make([]string, len(in.ContentTypeInference))
	for i, r := range in.ContentTypeInference {
		names[i] = r.Name
	}
	v.unique(file, "rule", names)

	for _, r := range in.ContentTypeInference {
		if strings.TrimSpace(r.Result) == "" {
			v.addf(file, "rule %q has no result", r.Name)
		}
		v.confidence(file, fmt.Sprintf("rule %q", r.Name), r.Confidence)
	}
}

func (v *validator) routeFamilies(rf RouteFamiliesConfig, t TemplateCatalogConfig) {
	const file = "route_families.yaml"
	ids := make([]string, len(rf.Families))
	for i, f := range rf.Families {
		ids[i] = f.ID
	}
	v.unique(file, "family", ids)

	routeIDs := make(map[string]bool)
	for _, tpl := range allTemplates(t) {
		if id := strings.TrimSpace(tpl.RouteID); id != "" {
			routeIDs[id] = true
		}
	}
	for _, f := range rf.Families {
		for _, id := range f.Select.AllowedRouteIDs {
			if !routeIDs[strings.TrimSpace(id)] {
				v.addf(file, "family %q: allowed_route_ids %q matches no template route_id", f.ID, id)
			}
		}
	}
}

// groupRoutes checks override ids. force_template_id is deliberately not required to
// exist in the template catalog: an override may force a virtual template id.
func (v *validator) groupRoutes(g GroupRouteConfig) {
	const file = "group_route_overrides.yaml"
	ids := make([]string, len(g.Overrides))
	for i, o := range g.Overrides {
		ids[i] = o.ID
	}
	v.unique(file, "override", ids)
}

func (v *validator) groupSlot2(g GroupSlot2PlatformConfig) {
	const file = "group_slot2_platform_rules.yaml"
	ids := make([]string, len(g.Rules))
	for i, r := range g.Rules {
		ids[i] = r.ID
		v.regex(file, fmt.Sprintf("rule %q group_regex", r.ID), r.Match.GroupRegex)
		v.confidence(file, fmt.Sprintf("rule %q unknown_platform_confidence", r.ID), r.Output.UnknownPlatformConfidence)
	}
	v.unique(file, "rule", ids)
}

func (v *validator) packProfiles(p PackProfileRulesConfig) {
	const file = "pack_profile_rules.yaml"
	ids := make([]string, len(p.Rules))
	for i, r := range p.Rules {
		ids[i] = r.ID
		if strings.TrimSpace(r.Emit) == "" {
			v.addf(file, "rule %q has no emit", r.ID)
		}
	}
	v.unique(file, "rule", ids)
}

func (v *validator) titleKnowledge(tk TitleKnowledgeConfig) {
	const file = "title_knowledge.yaml"
	ids := make([]string, len(tk.Entries))
	for i, e := range tk.Entries {
		ids[i] = e.ID
		if strings.TrimSpace(e.CanonicalTitle) == "" {
			v.addf(file, "entry %q has no canonical_title", e.ID)
		}
	}
	v.unique(file, "entry", ids)
}

func (v *validator) groupList(g GroupListConfig) {
	const file = "grouplist.yaml"
	owner := make(map[string]string)
	for i, grp := range g.Groups {
		if strings.TrimSpace(grp.Name) == "" {
			v.addf(file, "group #%d has no name", i+1)
			continue
		}
		if len(grp.Patterns) == 0 {
			v.addf(file, "group %q has no patterns", grp.Name)
		}
		for _, p := range grp.Patterns {
			key := strings.ToLower(strings.TrimSpace(p))
			if key == "" {
				v.addf(file, "group %q has an empty pattern", grp.Name)
				continue
			}
			if prev, dup := owner[key]; dup && prev != grp.Name {
				v.addf(file, "pattern %q is claimed by both %q and %q", p, prev, grp.Name)
				continue
			}
			owner[key] = grp.Name
		}
	}
}

func (v *validator) dictionaries(c *Config) {
	const synonyms = "dict_token_synonyms.yaml"
	ids := make([]string, len(c.DictTokenSynonyms.Rules))
	for i, r := range c.DictTokenSynonyms.Rules {
		ids[i] = r.ID
		if strings.TrimSpace(r.Emit.Type) == "" {
			v.addf(synonyms, "rule %q has an emit without a type", r.ID)
		}
	}
	v.unique(synonyms, "rule", ids)

	const promotion = "dict_note_promotion.yaml"
	ids = make([]string, len(c.DictNotePromotion.Rules))
	for i, r := range c.DictNotePromotion.Rules {
		ids[i] = r.ID
		if strings.TrimSpace(r.PromoteTo.Type) == "" {
			v.addf(promotion, "rule %q has no promote_to.type", r.ID)
		}
	}
	v.unique(promotion, "rule", ids)

	const noise = "dict_alias_noise_words.yaml"
	for i, p := range c.DictAliasNoiseWords.AliasNoise.RegexPatterns {
		v.regex(noise, fmt.Sprintf("regex_patterns[%d]", i), p)
	}
}
