package adapter

import (
	"context"
	"fmt"

	"bansoko/internal/episodetitle"
	plugintargetport "bansoko/internal/plugintarget/port"
)

// NotificationEpisodeTitleResolver connects notification admission to the same
// persistent provider-title cache used by naming. It owns no upstream clients
// or second cache, and it has no role after the delivery payload is frozen.
type NotificationEpisodeTitleResolver struct {
	Cache *episodetitle.PersistentCache
}

func (r *NotificationEpisodeTitleResolver) ResolveNotificationEpisodeTitle(ctx context.Context, request plugintargetport.NotificationEpisodeTitleRequest) (plugintargetport.NotificationEpisodeTitle, error) {
	if r == nil || r.Cache == nil {
		return plugintargetport.NotificationEpisodeTitle{}, fmt.Errorf("%w: the notification episode title cache is not wired", episodetitle.ErrTransient)
	}
	title, err := r.Cache.EpisodeTitle(ctx, episodetitle.Source(request.Source), request.WorkID, request.Season, request.Episode, false)
	if err != nil {
		return plugintargetport.NotificationEpisodeTitle{}, err
	}
	return plugintargetport.NotificationEpisodeTitle{Known: title.Known, Title: title.Title}, nil
}
