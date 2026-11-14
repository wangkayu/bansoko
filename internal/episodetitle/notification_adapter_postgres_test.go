package episodetitle_test

import (
	"context"
	"testing"

	"bansoko/internal/episodetitle"
	identitymetadataadapter "bansoko/internal/identitymetadata/adapter"
	plugintargetport "bansoko/internal/plugintarget/port"
	"bansoko/internal/testutil/disposabledb"
)

type notificationEpisodeClientFunc func(context.Context, episodetitle.Key) (episodetitle.List, error)

func (f notificationEpisodeClientFunc) Episodes(ctx context.Context, key episodetitle.Key) (episodetitle.List, error) {
	return f(ctx, key)
}

func TestNotificationEpisodeTitleResolverSharesPersistentCache(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	calls := 0
	resolver := &identitymetadataadapter.NotificationEpisodeTitleResolver{Cache: &episodetitle.PersistentCache{
		DB: db,
		Lookup: episodetitle.New(episodetitle.Options{Clients: map[episodetitle.Source]episodetitle.Client{
			episodetitle.SourceBGM: notificationEpisodeClientFunc(func(_ context.Context, key episodetitle.Key) (episodetitle.List, error) {
				calls++
				if key.WorkID != "123" || key.Season != 0 {
					t.Fatalf("lookup key=%+v", key)
				}
				return episodetitle.List{Episodes: []episodetitle.Episode{{Number: 7, Title: "Cached title"}}}, nil
			}),
		}}),
	}}
	request := plugintargetport.NotificationEpisodeTitleRequest{Source: "bgm", WorkID: "123", Episode: 7}
	for range 2 {
		answer, err := resolver.ResolveNotificationEpisodeTitle(context.Background(), request)
		if err != nil || !answer.Known || answer.Title != "Cached title" {
			t.Fatalf("answer=%+v err=%v", answer, err)
		}
	}
	if calls != 1 {
		t.Fatalf("upstream calls=%d, want one persistent-cache fill", calls)
	}
}
