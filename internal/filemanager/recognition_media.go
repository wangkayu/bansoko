package filemanager

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sync"
	"time"

	"bansoko/internal/download/domain"
)

const recognitionMediaCacheCapacity = 512

// Share the process budget even when multiple FileManager services are wired.
var recognitionProbeSlots = make(chan struct{}, 4)

var errRecognitionMediaChanged = errors.New("file changed before or during the media probe")

type recognitionMediaKey struct {
	path, revision string
}

type recognitionMediaFlight struct {
	done  chan struct{}
	info  fs.FileInfo
	facts domain.MediaFacts
	err   error
}

type recognitionMediaEntry struct {
	key   recognitionMediaKey
	info  fs.FileInfo
	facts domain.MediaFacts
}

type recognitionMediaCache struct {
	prober  RecognitionMediaProber
	mu      sync.Mutex
	entries map[recognitionMediaKey]*list.Element
	lru     list.List
	flights map[recognitionMediaKey]*recognitionMediaFlight
}

func newRecognitionMediaCache(prober RecognitionMediaProber) *recognitionMediaCache {
	return &recognitionMediaCache{prober: prober, entries: map[recognitionMediaKey]*list.Element{}, flights: map[recognitionMediaKey]*recognitionMediaFlight{}}
}

func sameRecognitionFile(a, b fs.FileInfo) bool {
	return a != nil && b != nil && a.Mode().IsRegular() && b.Mode().IsRegular() &&
		os.SameFile(a, b) && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime())
}

func validateRecognitionFile(path string, expected fs.FileInfo) error {
	current, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !sameRecognitionFile(current, expected) {
		return errRecognitionMediaChanged
	}
	return nil
}

// probe shares successes across requests. Failed flights are removed as soon as
// they finish, while the plan retains the outcome for all rows using this video.
func (c *recognitionMediaCache) probe(ctx context.Context, file inspection, refresh, disableIO bool) (domain.MediaFacts, string, error) {
	if err := ctx.Err(); err != nil {
		return domain.MediaFacts{}, "", err
	}
	if c == nil {
		if disableIO {
			return domain.MediaFacts{}, "not_loaded", nil
		}
		return domain.MediaFacts{}, "probe_unavailable", nil
	}
	key := recognitionMediaKey{file.absolute, file.detail.Revision}
	if err := validateRecognitionFile(key.path, file.info); err != nil {
		return domain.MediaFacts{}, "probe_failed", nil
	}
	c.mu.Lock()
	if element := c.entries[key]; element != nil {
		entry := element.Value.(recognitionMediaEntry)
		if !refresh && sameRecognitionFile(entry.info, file.info) {
			c.lru.MoveToFront(element)
			c.mu.Unlock()
			return entry.facts, "", ctx.Err()
		}
		c.remove(element)
	}
	if disableIO {
		c.mu.Unlock()
		return domain.MediaFacts{}, "not_loaded", nil
	}
	flight := c.flights[key]
	if refresh || flight == nil || !sameRecognitionFile(flight.info, file.info) {
		// Installing a new flight also invalidates an in-flight result. Only
		// the currently registered flight may write back to the cache.
		flight = &recognitionMediaFlight{done: make(chan struct{}), info: file.info}
		c.flights[key] = flight
		go c.run(context.WithoutCancel(ctx), key, flight)
	}
	c.mu.Unlock()
	select {
	case <-ctx.Done():
		return domain.MediaFacts{}, "", ctx.Err()
	case <-flight.done:
		if err := ctx.Err(); err != nil {
			return domain.MediaFacts{}, "", err
		}
		return flight.facts, recognitionProbeReason(flight.err), nil
	}
}

func (c *recognitionMediaCache) remove(element *list.Element) {
	delete(c.entries, element.Value.(recognitionMediaEntry).key)
	c.lru.Remove(element)
}

func (c *recognitionMediaCache) run(ctx context.Context, key recognitionMediaKey, flight *recognitionMediaFlight) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	flight.facts, flight.err = c.inspect(ctx, key.path, flight.info)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.flights[key] == flight {
		delete(c.flights, key)
		if flight.err == nil {
			c.entries[key] = c.lru.PushFront(recognitionMediaEntry{key, flight.info, flight.facts})
			if c.lru.Len() > recognitionMediaCacheCapacity {
				c.remove(c.lru.Back())
			}
		}
	}
	close(flight.done)
}

func (c *recognitionMediaCache) inspect(ctx context.Context, path string, expected fs.FileInfo) (domain.MediaFacts, error) {
	select {
	case <-ctx.Done():
		return domain.MediaFacts{}, ctx.Err()
	case recognitionProbeSlots <- struct{}{}:
		defer func() { <-recognitionProbeSlots }()
	}
	if err := validateRecognitionFile(path, expected); err != nil {
		return domain.MediaFacts{}, err
	}
	facts, err := c.prober.Probe(ctx, path)
	if err != nil {
		return domain.MediaFacts{}, err
	}
	if err := validateRecognitionFile(path, expected); err != nil {
		return domain.MediaFacts{}, fmt.Errorf("after media probe: %w", err)
	}
	return facts, ctx.Err()
}

func recognitionProbeReason(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, domain.ErrMediaProbeTimeout), errors.Is(err, context.DeadlineExceeded):
		return "probe_timeout"
	case errors.Is(err, domain.ErrMediaProbeUnavailable):
		return "probe_unavailable"
	case errors.Is(err, domain.ErrMediaProbeOutputInvalid):
		return "invalid_output"
	default:
		return "probe_failed"
	}
}
