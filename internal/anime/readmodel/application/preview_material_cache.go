package application

import (
	"context"
	"sync"

	"bansoko/internal/attrrules"
	"bansoko/internal/namingvars"
)

// previewMaterialCacheLimit bounds each cache. Entries are tiny and a deployment
// uses a handful of distinct programs, so the limit only guards against a
// pathological stream of digests; when it is reached the cache starts over.
const previewMaterialCacheLimit = 64

// previewMaterialCache holds the immutable inputs a preview evaluates with: the
// attr rules and the Vars program a business snapshot pins. Both are addressed
// by their content digest and verified against it on load, so a cached value can
// never differ from what a fresh read would return. The zero value is ready to
// use. Failed loads are never stored.
type previewMaterialCache struct {
	mu        sync.Mutex
	attrRules map[string]attrrules.Rules
	programs  map[string]namingvars.Program
}

func (c *previewMaterialCache) attrRulesFor(
	ctx context.Context,
	ref namingvars.ProgramRef,
	load func(context.Context, namingvars.ProgramRef) (attrrules.Rules, error),
) (attrrules.Rules, error) {
	key := ref.Version + "\x00" + ref.Digest
	c.mu.Lock()
	if rules, ok := c.attrRules[key]; ok {
		c.mu.Unlock()
		return rules, nil
	}
	c.mu.Unlock()
	rules, err := load(ctx, ref)
	if err != nil {
		return attrrules.Rules{}, err
	}
	c.mu.Lock()
	if c.attrRules == nil || len(c.attrRules) >= previewMaterialCacheLimit {
		c.attrRules = make(map[string]attrrules.Rules)
	}
	c.attrRules[key] = rules
	c.mu.Unlock()
	return rules, nil
}

func (c *previewMaterialCache) programFor(
	ctx context.Context,
	ref namingvars.ProgramRef,
	load func(context.Context, namingvars.ProgramRef) (namingvars.Program, error),
) (namingvars.Program, error) {
	key := ref.Version + "\x00" + ref.Digest
	c.mu.Lock()
	if program, ok := c.programs[key]; ok {
		c.mu.Unlock()
		return program, nil
	}
	c.mu.Unlock()
	program, err := load(ctx, ref)
	if err != nil {
		return namingvars.Program{}, err
	}
	c.mu.Lock()
	if c.programs == nil || len(c.programs) >= previewMaterialCacheLimit {
		c.programs = make(map[string]namingvars.Program)
	}
	c.programs[key] = program
	c.mu.Unlock()
	return program, nil
}

// size is the number of cached entries across both kinds.
func (c *previewMaterialCache) size() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.attrRules) + len(c.programs)
}
