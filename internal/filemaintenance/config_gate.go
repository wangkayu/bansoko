package filemaintenance

import (
	"context"
	"errors"
	"sort"
	"strings"

	"bansoko/internal/postprocessing/naming"
)

// configItemNamesLimit bounds the variable names one template item carries,
// the length isConfigIncompleteReason accepts.
const configItemNamesLimit = 200

// CurrentConfigHold reports what the anime's current configuration lacks to
// render the given import files: each missing item with the number of files it
// blocks. It is the import plan's configuration gate and gives the refusal a
// maintenance render of the placed file would give — a missing binding
// (currentAnimeConfig's own check, counting every file) and the engine's
// undefined-variable refusal under the current rules and the current naming
// renderer. Each file context carries the frozen release and file facts; the
// anime facts and the configuration are replaced with the current ones here.
// Any other render error holds nothing: the plan executes its frozen target
// and reports the failure itself. It performs no filesystem or provider I/O.
func (r *NamingTargetRenderer) CurrentConfigHold(ctx context.Context, animeID string, files []naming.NamingContext) (map[string]int, error) {
	if len(files) == 0 {
		return nil, nil
	}
	config, err := r.currentAnimeConfig(ctx, animeID)
	var incomplete *ConfigIncompleteError
	if err != nil && !errors.As(err, &incomplete) {
		return nil, err
	}
	hold := map[string]int{}
	unbound := map[string]bool{}
	for _, item := range config.missingBindings() {
		hold[item] = len(files)
		root := strings.TrimPrefix(item, "binding_missing:")
		unbound[root] = true
	}
	anime, err := r.animeFacts(ctx, animeID)
	if err != nil {
		return nil, err
	}
	rules, err := r.renderRules(ctx, animeID)
	if errors.Is(err, naming.ErrRenderFactsUnavailable) {
		return hold, nil
	}
	if err != nil {
		return nil, err
	}
	engine := naming.NewEngine()
	compiled, err := engine.CompileRulesWithVars(rules, files[0].Vars)
	if err != nil {
		return hold, nil
	}
	for _, facts := range files {
		facts.Anime.ID, facts.Anime.Title, facts.Anime.TitleOverride, facts.Anime.ProviderTitle = animeID, anime.title, anime.titleOverride, anime.providerTitle
		config.apply(&facts.Anime)
		resolved := naming.DefaultRegistry().ResolveAll(naming.ResolverInput{Facts: facts})
		variant := compiled.TV
		if naming.MediaKind(resolved.Context.Derived.EffectiveMediaKind) == naming.MediaKindMovie {
			variant = compiled.Movie
		}
		undefined := map[string]bool{}
		for _, template := range []naming.CompiledTemplate{variant.Directory, variant.VideoFilename} {
			_, err := engine.RenderDetailed(template, resolved.Context)
			var named *naming.Error
			if !errors.As(err, &named) || named.Code != naming.ErrCodeNamingFactUndefined {
				continue
			}
			for _, name := range named.Missing {
				// A variable of an unbound work is the binding item already.
				if root, _, _ := strings.Cut(name, "."); !unbound[root] {
					undefined[name] = true
				}
			}
		}
		if item := templateVariableItem(undefined); item != "" {
			hold[item]++
		}
	}
	return hold, nil
}

// templateVariableItem is the template item naming the undefined variables in
// order, keeping whole names within configItemNamesLimit.
func templateVariableItem(undefined map[string]bool) string {
	names := make([]string, 0, len(undefined))
	for name := range undefined {
		names = append(names, name)
	}
	sort.Strings(names)
	joined := ""
	for _, name := range names {
		next := name
		if joined != "" {
			next = joined + "," + name
		}
		if len(next) > configItemNamesLimit {
			break
		}
		joined = next
	}
	if joined == "" {
		return ""
	}
	return ConfigItemTemplateVariableUndefinedPrefix + joined
}
