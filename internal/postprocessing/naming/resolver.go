package naming

import (
	"fmt"
	"strings"
	"sync"
)

// Resolver result states ()
const (
	ResolverStateValue    = "value"
	ResolverStateNull     = "null"
	ResolverStateConflict = "conflict"

	// ErrCodeResolverCatalogInvalid fails the build/startup catalog gate.
	ErrCodeResolverCatalogInvalid = "RESOLVER_CATALOG_INVALID"
)

// NamingDiagnostic is a bounded, user-visible resolver diagnostic. Missing
// optional inputs are null plus diagnostics, never a Subscription hold.
type NamingDiagnostic struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Path    string `json:"path,omitempty"`
}

// SourceSpan is a byte-offset span inside the resolver input string.
type SourceSpan struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

// ResolverEvidence records the exact rule, span, and captures behind one
// resolved value. Evidence never enters the template context; it flows with
// ResolverResult into previews and frozen snapshots.
type ResolverEvidence struct {
	ResolverKey string            `json:"resolver_key"`
	RuleID      string            `json:"rule_id"`
	RuleIndex   int               `json:"rule_index"`
	InputPath   string            `json:"input_path"`
	MatchedSpan *SourceSpan       `json:"matched_span,omitempty"`
	Captures    map[string]string `json:"captures,omitempty"`
}

// ResolverDefinition declares one resolver's identity, output contract, and
// dependency list.
type ResolverDefinition struct {
	Key          string
	OutputType   string
	Dependencies []string
	Nullable     bool
}

// ResolverResult is one resolver's atomic output with evidence.
type ResolverResult struct {
	Key         string
	State       string // value | null | conflict
	Value       any
	Evidence    []ResolverEvidence
	Diagnostics []NamingDiagnostic
}

// ResolverInput is the complete resolver input: the Layer 1 facts. A
// resolver never performs I/O beyond reading these values.
type ResolverInput struct {
	Facts NamingContext
}

// Resolver turns Layer 1 facts into one typed, atomic resolved value.
type Resolver interface {
	Definition() ResolverDefinition
	Resolve(input ResolverInput) ResolverResult
}

// ResolvedContext pairs the template-ready context (Parsed/Derived filled)
// with the per-resolver results carrying evidence.
type ResolvedContext struct {
	Context NamingContext
	Results []ResolverResult
}

// Registry is the validated resolver catalog. Keys are unique and
// dependencies form a DAG; resolution runs in stable topological order with
// registration order breaking ties.
type Registry struct {
	order      []string
	resolvers  map[string]Resolver
	duplicates []string
}

// NewRegistry registers resolvers in the given order. Duplicate keys are
// retained for Validate to report.
func NewRegistry(resolvers ...Resolver) *Registry {
	registry := &Registry{resolvers: map[string]Resolver{}}
	for _, resolver := range resolvers {
		key := resolver.Definition().Key
		if _, exists := registry.resolvers[key]; exists {
			registry.duplicates = append(registry.duplicates, key)
			continue
		}
		registry.resolvers[key] = resolver
		registry.order = append(registry.order, key)
	}
	return registry
}

// Validate enforces the catalog gate: unique keys, known dependencies,
// complete definitions, and an acyclic dependency graph.
func (r *Registry) Validate() error {
	if len(r.duplicates) > 0 {
		return &Error{Code: ErrCodeResolverCatalogInvalid,
			Message: fmt.Sprintf("duplicate resolver key %q", r.duplicates[0])}
	}
	for _, key := range r.order {
		definition := r.resolvers[key].Definition()
		if definition.Key == "" {
			return &Error{Code: ErrCodeResolverCatalogInvalid,
				Message: fmt.Sprintf("resolver %q has an incomplete definition", key)}
		}
		for _, dependency := range definition.Dependencies {
			if _, ok := r.resolvers[dependency]; !ok {
				return &Error{Code: ErrCodeResolverCatalogInvalid,
					Message: fmt.Sprintf("resolver %q depends on unknown resolver %q", key, dependency)}
			}
		}
	}
	if _, err := r.topologicalOrder(); err != nil {
		return err
	}
	return nil
}

// topologicalOrder returns a stable dependency order (Kahn; registration
// order breaks ties).
func (r *Registry) topologicalOrder() ([]string, error) {
	emitted := make(map[string]bool, len(r.order))
	order := make([]string, 0, len(r.order))
	for len(order) < len(r.order) {
		progress := false
		for _, key := range r.order {
			if emitted[key] {
				continue
			}
			ready := true
			for _, dependency := range r.resolvers[key].Definition().Dependencies {
				if !emitted[dependency] {
					ready = false
					break
				}
			}
			if ready {
				emitted[key] = true
				order = append(order, key)
				progress = true
			}
		}
		if !progress {
			remaining := []string{}
			for _, key := range r.order {
				if !emitted[key] {
					remaining = append(remaining, key)
				}
			}
			return nil, &Error{Code: ErrCodeResolverCatalogInvalid,
				Message: fmt.Sprintf("resolver dependency cycle involving %s", strings.Join(remaining, ", "))}
		}
	}
	return order, nil
}

// ResolveAll executes every resolver in topological order and fills the
// template-facing Parsed/Derived context fields from their atomic outputs.
// The registry must have passed Validate (DefaultRegistry panics otherwise).
func (r *Registry) ResolveAll(input ResolverInput) ResolvedContext {
	order, err := r.topologicalOrder()
	if err != nil {
		panic(fmt.Sprintf("naming: resolve on invalid registry: %v", err))
	}
	resolved := ResolvedContext{Context: input.Facts, Results: make([]ResolverResult, 0, len(order))}
	for _, key := range order {
		result := r.resolvers[key].Resolve(input)
		resolved.Results = append(resolved.Results, result)
		applyResolverResult(&resolved.Context, result)
	}
	return resolved
}

// applyResolverResult maps each resolver's atomic value into the template
// context. Null results leave the zero (nullable) fields untouched.
func applyResolverResult(ctx *NamingContext, result ResolverResult) {
	if result.State != ResolverStateValue || result.Value == nil {
		return
	}
	switch value := result.Value.(type) {
	case episodeValue:
		ctx.Parsed.Episode = EpisodeNamingVariables{
			Raw:         value.Raw,
			Display:     value.Display,
			Number:      value.Number,
			Offset:      value.Offset,
			SpecialKind: value.SpecialKind,
		}
	case languageValue:
		ctx.Parsed.Language = LanguageNamingVariables{
			Code:        value.Code,
			Description: value.Description,
			BCP47:       append([]string(nil), value.BCP47...),
			Languages:   append([]string(nil), value.Languages...),
		}
	case subtitleValue:
		ctx.Parsed.Subtitle = SubtitleNamingVariables{
			Format:          value.Format,
			OriginalSuffix:  value.OriginalSuffix,
			Languages:       append([]string(nil), value.Languages...),
			PrimaryLanguage: value.PrimaryLanguage,
			TagLanguages:    append([]string(nil), value.TagLanguages...),
			Flags:           value.Flags,
		}
	case subtitleModeValue:
		ctx.Release.SubtitleMode = value.SubtitleMode
	case effectiveTitleValue:
		ctx.Derived.EffectiveTitle = value.EffectiveTitle
		ctx.Derived.EffectiveTitleSource = value.EffectiveTitleSource
	case seasonFolderValue:
		ctx.Derived.SeasonFolder = value.SeasonFolder
	case mediaKindValue:
		ctx.Derived.EffectiveMediaKind = value.EffectiveMediaKind
	}
}

var (
	defaultRegistryOnce sync.Once
	defaultRegistry     *Registry
)

// DefaultRegistry builds the distribution resolver catalog and validates it
// once. A catalog failure is a startup gate and panics
// (RESOLVER_CATALOG_INVALID).
func DefaultRegistry() *Registry {
	defaultRegistryOnce.Do(func() {
		registry := NewRegistry(
			episodeResolver{},
			languageResolver{},
			subtitleResolver{},
			subtitleModeResolver{},
			effectiveTitleResolver{},
			seasonFolderResolver{},
			effectiveMediaKindResolver{},
		)
		if err := registry.Validate(); err != nil {
			panic(fmt.Sprintf("naming: default resolver catalog is invalid: %v", err))
		}
		defaultRegistry = registry
	})
	return defaultRegistry
}

// SummarizeResolverValue returns a bounded scalar summary of one resolved
// value for previews and frozen snapshots.
func SummarizeResolverValue(result ResolverResult) string {
	if result.State != ResolverStateValue || result.Value == nil {
		return ""
	}
	summary := fmt.Sprintf("%v", result.Value)
	const maxSummaryBytes = 512
	if len(summary) > maxSummaryBytes {
		return summary[:maxSummaryBytes]
	}
	return summary
}

// SummarizeResolverEvidence returns a bounded human-readable evidence
// summary: rule id, matched span, and captures.
func SummarizeResolverEvidence(evidence []ResolverEvidence) string {
	if len(evidence) == 0 {
		return ""
	}
	parts := make([]string, 0, len(evidence))
	for _, item := range evidence {
		var b strings.Builder
		b.WriteString(item.RuleID)
		fmt.Fprintf(&b, "[%d]", item.RuleIndex)
		b.WriteString(" " + item.InputPath)
		if item.MatchedSpan != nil {
			fmt.Fprintf(&b, "@%d-%d", item.MatchedSpan.Start, item.MatchedSpan.End)
		}
		if len(item.Captures) > 0 {
			b.WriteString(" captures{")
			first := true
			for group, value := range item.Captures {
				if !first {
					b.WriteString(", ")
				}
				first = false
				fmt.Fprintf(&b, "%s:%q", group, value)
			}
			b.WriteString("}")
		}
		parts = append(parts, b.String())
	}
	summary := strings.Join(parts, "; ")
	const maxEvidenceBytes = 512
	if len(summary) > maxEvidenceBytes {
		return summary[:maxEvidenceBytes]
	}
	return summary
}
