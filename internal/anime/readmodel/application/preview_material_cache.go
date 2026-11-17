package application

import (
	"context"
	"sync"

	"bansoko/internal/namingvars"
)

// previewMaterialCacheLimit bounds the immutable program cache.
const previewMaterialCacheLimit = 64

// previewMaterialCache holds Vars programs addressed and verified by their
// content digest. The zero value is ready to use. Failed loads are never stored.
type previewMaterialCache struct {
	mu       sync.Mutex
	programs map[string]namingvars.Program
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

func (c *previewMaterialCache) size() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.programs)
}
