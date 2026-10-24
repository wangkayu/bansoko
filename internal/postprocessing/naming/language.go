package naming

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"bansoko/internal/templatefacts"
)

// Language is the naming template language: JavaScript template literals that
// read the unified fact vocabulary (templatefacts registry, naming scenario).
const Language = "js-naming-1"

// SupportedLanguage reports whether language is the naming language the engine
// renders.
func SupportedLanguage(language string) bool {
	return language == Language
}

// languageProfile is everything the engine and analyser need for the language.
type languageProfile struct {
	// roots are the context roots a template reads; varsRoot is the context key
	// carrying Vars (the `vars` identifier binds it).
	roots    map[string]bool
	varsRoot string
	// index is the leaf whitelist; nullable the facts the missing check covers.
	index    catalogFieldIndex
	nullable map[string]bool
	// bindings is the wrapper's root declarations.
	bindings string
}

var (
	profileOnce   sync.Once
	sharedProfile *languageProfile
)

// languageProfileOf returns the process-wide profile of the naming language.
func languageProfileOf() *languageProfile {
	profileOnce.Do(func() { sharedProfile = buildProfile() })
	return sharedProfile
}

func buildProfile() *languageProfile {
	p := &languageProfile{varsRoot: "vars", roots: map[string]bool{}, nullable: map[string]bool{}}
	paths := []string{}
	for _, fact := range templatefacts.ScenarioFacts(templatefacts.ScenarioNaming) {
		paths = append(paths, fact.Path)
		if fact.NullableIn(templatefacts.ScenarioNaming) {
			p.nullable[fact.Path] = true
		}
	}
	p.index = indexPaths(paths)
	bindings := []string{}
	for _, root := range templatefacts.Roots(templatefacts.ScenarioNaming) {
		if root == "vars" {
			continue
		}
		p.roots[root] = true
		// A reserved root has no fields; it still resolves so a reference to one
		// of its (future) fields is an unknown-field error, not an identifier one.
		p.index.nodes[root] = true
		bindings = append(bindings, root+"=context."+root)
	}
	p.bindings = "const " + strings.Join(bindings, ", ") + ", vars=context.vars || {}"
	return p
}

func indexPaths(paths []string) catalogFieldIndex {
	index := catalogFieldIndex{leaves: map[string]bool{}, nodes: map[string]bool{}}
	for _, path := range paths {
		index.leaves[path] = true
		segments := strings.Split(path, ".")
		for i := 1; i <= len(segments); i++ {
			index.nodes[strings.Join(segments[:i], ".")] = true
		}
	}
	return index
}

// isRoot reports whether name is a context root of the language.
func (p *languageProfile) isRoot(name string) bool { return p.roots[name] }

// guardedRoots lists the context keys the declared-only guard wraps.
func (p *languageProfile) guardedRoots() []string {
	out := make([]string, 0, len(p.roots)+1)
	for root := range p.roots {
		out = append(out, root)
	}
	return append(out, p.varsRoot)
}

// object builds the plain template object for one render from its projected
// facts. With placeholders every execution-phase fact reads
// PreviewPlaceholder(path).
func (p *languageProfile) object(facts *templatefacts.FactSet, placeholders bool) (map[string]interface{}, error) {
	out := templatefacts.ScenarioObject(templatefacts.ScenarioNaming, facts)
	if placeholders {
		for _, fact := range templatefacts.ScenarioFacts(templatefacts.ScenarioNaming) {
			if fact.Phase != templatefacts.PhaseExecution {
				continue
			}
			segments := strings.Split(fact.Path, ".")
			node := out
			for _, segment := range segments[:len(segments)-1] {
				node, _ = node[segment].(map[string]any)
			}
			if node != nil {
				node[segments[len(segments)-1]] = PreviewPlaceholder(fact.Path)
			}
		}
	}
	data, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	if len(data) > MaxContextBytes {
		return nil, fmt.Errorf("naming context exceeds the %d byte limit", MaxContextBytes)
	}
	return out, nil
}

// missing returns the nullable required facts the projected facts have no
// value for. With placeholders an execution-phase fact is never missing.
func (p *languageProfile) missing(required []string, facts *templatefacts.FactSet, placeholders bool) []string {
	missing := []string{}
	for _, path := range required {
		if !p.nullable[path] {
			continue
		}
		fact, ok := templatefacts.Lookup(path)
		if !ok || (placeholders && fact.Phase == templatefacts.PhaseExecution) {
			continue
		}
		if templatefacts.Absent(fact.Value(facts)) {
			missing = append(missing, path)
		}
	}
	return missing
}
