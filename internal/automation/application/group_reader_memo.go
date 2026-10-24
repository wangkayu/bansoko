package application

import (
	"context"
	"sync"

	providerdomain "bansoko/internal/provider/domain"
)

// memoGroupReader answers repeated reads of the same catalog entry within one
// dispatch run from the first answer (success or failure).
type memoGroupReader struct {
	inner GroupReader
	mu    sync.Mutex
	cache map[string]memoGroupRead
}

type memoGroupRead struct {
	result providerdomain.GroupReadResult
	err    error
}

func (r *memoGroupReader) ReadGroups(ctx context.Context, catalogEntryRef string) (providerdomain.GroupReadResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if cached, ok := r.cache[catalogEntryRef]; ok {
		return cached.result, cached.err
	}
	result, err := r.inner.ReadGroups(ctx, catalogEntryRef)
	r.cache[catalogEntryRef] = memoGroupRead{result: result, err: err}
	return result, err
}

func memoizeGroupReaders(readers map[string]GroupReader) map[string]GroupReader {
	memo := make(map[string]GroupReader, len(readers))
	for provider, reader := range readers {
		memo[provider] = &memoGroupReader{inner: reader, cache: map[string]memoGroupRead{}}
	}
	return memo
}
