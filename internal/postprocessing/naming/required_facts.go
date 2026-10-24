package naming

import (
	"fmt"
	"sort"
	"strings"

	"bansoko/internal/templatefacts"
)

// ErrCodeNamingFactMissing: the rendered target is invalid and nullable facts
// the template read outside any conditional/fallback had no value and rendered
// empty (for example `-.mkv` for two missing work ids normalizes to an empty
// name). Missing names them. Under naming-render-1 (legacy replay) any such
// missing fact refuses the render by itself.
const ErrCodeNamingFactMissing = "NAMING_FACT_MISSING"

// namingFactMissingReasonPrefix is the product reason code family for the
// refusal; the suffix names the first missing fact (see FactReasonSlug).
const namingFactMissingReasonPrefix = "naming_fact_missing_"

// legacyRefused narrows the naming-render-1 refusal to the replayable facts.
// The provider episode title facts are exempt: no legacy render ever carried
// them (the facts postdate the legacy renderer) and their empty value is the
// episode title resolver's definitive answer — a provider absence, an unbound
// source or an unresolvable episode all render the empty fact by contract —
// so replaying them as a refusal would render a name the network resolution
// could never produce.
func legacyRefused(empty []string) []string {
	refused := make([]string, 0, len(empty))
	for _, path := range empty {
		if templatefacts.IsEpisodeTitleFactPath(path) {
			continue
		}
		refused = append(refused, path)
	}
	return refused
}

// MissingRequiredFacts returns, sorted, the nullable catalog facts among
// `required` (a compiled template's RequiredFacts: read outside any
// conditional, `||`, `??`, defaultValue or coalesce) whose value in ctx is
// absent: null or a blank string. A template that wants such a fact to be
// optional says so with one of those explicit fallbacks.
//
// The engine reports these facts on every render (Rendered.EmptyFacts); they
// refuse a render only when the result is invalid, or under naming-render-1.
func MissingRequiredFacts(required []string, ctx NamingContext) []string {
	if len(required) == 0 {
		return nil
	}
	facts := renderFacts(ctx, 0)
	return sortedMissing(languageProfileOf().missing(required, &facts, false))
}

func sortedMissing(missing []string) []string {
	if len(missing) == 0 {
		return nil
	}
	sort.Strings(missing)
	return missing
}

// FactReasonSlug is the reason-code suffix for one catalog fact path:
// lowercased, dots replaced by underscores (tmdb.id -> tmdb_id).
func FactReasonSlug(path string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(path), ".", "_"))
}

func missingFactsError(kind TemplateKind, missing []string) *Error {
	return &Error{
		Code:    ErrCodeNamingFactMissing,
		Field:   string(kind),
		Message: fmt.Sprintf("template needs facts this render does not have: %s; add an explicit fallback or bind them", strings.Join(missing, ", ")),
		Missing: append([]string(nil), missing...),
	}
}

// ReasonCode is the lowercase product reason code for this error. A missing
// fact names the first missing fact (naming_fact_missing_anime_tmdb_workid)
// so the run record and the preview can say which value is missing.
func (e *Error) ReasonCode() string {
	if e == nil {
		return ""
	}
	if e.Code == ErrCodeNamingFactMissing && len(e.Missing) > 0 {
		return namingFactMissingReasonPrefix + FactReasonSlug(e.Missing[0])
	}
	if e.Code == ErrCodeNamingFactUndefined && len(e.Missing) > 0 {
		return namingFactUndefinedReasonPrefix + FactReasonSlug(e.Missing[0])
	}
	return strings.ToLower(strings.TrimSpace(e.Code))
}
