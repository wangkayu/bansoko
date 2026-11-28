package episodetitle

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestEpisodeTitlesBatchPreservesKnownOnTimeout(t *testing.T) {
	ctx := context.Background()
	clock := &fakeClock{now: time.Unix(100, 0).UTC()}
	cache, db := cacheHarness(t, clock, nil)
	key := WorkKey{Source: SourceTMDB, WorkID: "42", Season: 2}
	if err := cache.storeRows(ctx, cacheQuery{Source: key.Source, WorkID: key.WorkID, Season: key.Season},
		List{Episodes: []Episode{{Number: 2, Title: "From PostgreSQL"}}}, false, nil); err != nil {
		t.Fatal(err)
	}
	client := &fakeClient{fn: func(ctx context.Context, _ Key) (List, error) {
		<-ctx.Done()
		return List{}, ctx.Err()
	}}
	cache.Lookup = New(Options{Clients: map[Source]Client{key.Source: client}, QueryTimeout: 20 * time.Millisecond})
	episodes := make([]int, 24)
	for i := range episodes {
		episodes[i] = i + 1
	}
	work := map[WorkKey][]int{key: append(episodes, 2, 23)}
	answers, err := cache.EpisodeTitles(ctx, work, nil)
	if err != nil || len(answers) != 24 || client.calls.Load() != 1 {
		t.Fatalf("batch size=%d, provider calls=%d, error=%v", len(answers), client.calls.Load(), err)
	}
	for _, episode := range episodes {
		answer := answers[EpisodeKey{key, episode}]
		if episode == 2 {
			if !answer.Known || answer.Title.Title != "From PostgreSQL" || answer.Reason != "" {
				t.Fatalf("known episode: %+v", answer)
			}
		} else if answer.Known || answer.Reason != string(LookupUnreachable) {
			t.Fatalf("episode %d: %+v", episode, answer)
		}
	}
	var rows int
	if err := db.QueryRow(`SELECT count(*) FROM episode_title_cache`).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("failed lookup persisted rows: count=%d, error=%v", rows, err)
	}
	answers, err = cache.EpisodeTitles(ctx, work, map[WorkKey]bool{key: true})
	if err != nil || answers[EpisodeKey{key, 2}].Title.Title != "From PostgreSQL" ||
		answers[EpisodeKey{key, 23}].Reason != ReasonCacheMiss || client.calls.Load() != 1 {
		t.Fatalf("cache-only answers=%v, provider calls=%d, error=%v", answers, client.calls.Load(), err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := cache.EpisodeTitles(cancelled, work, nil); err != context.Canceled {
		t.Fatalf("batch cancellation: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.EpisodeTitles(ctx, work, nil); err == nil || errors.Is(err, ErrTransient) {
		t.Fatalf("database failure: %v", err)
	}
}

func TestEpisodeTitlesBatchStoresDefiniteNegative(t *testing.T) {
	ctx := context.Background()
	clock := &fakeClock{now: time.Unix(100, 0).UTC()}
	key := WorkKey{Source: SourceBGM, WorkID: "42"}
	client := &mutableClient{list: one()}
	cache, _ := cacheHarness(t, clock, map[Source]Client{key.Source: client})
	work := map[WorkKey][]int{key: {1, 3}}
	if _, err := cache.EpisodeTitles(ctx, work, nil); err != nil {
		t.Fatal(err)
	}
	options := map[WorkKey]bool{key: true}
	answers, err := cache.EpisodeTitles(ctx, work, options)
	if err != nil || answers[EpisodeKey{key, 1}].Title.Title != "One" || answers[EpisodeKey{key, 3}] != (DetailedTitle{}) {
		t.Fatalf("fresh answers=%+v error=%v", answers, err)
	}
	clock.Advance(7 * time.Hour)
	answers, err = cache.EpisodeTitles(ctx, work, options)
	if err != nil || !answers[EpisodeKey{key, 1}].Known || answers[EpisodeKey{key, 3}].Reason != ReasonCacheMiss || client.callCount() != 1 {
		t.Fatalf("expired answers=%+v calls=%d error=%v", answers, client.callCount(), err)
	}
}
