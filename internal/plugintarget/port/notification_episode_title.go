package port

import "context"

// NotificationEpisodeTitleRequest identifies one provider episode title. The
// admission resolver builds it only from frozen binding and episode facts.
type NotificationEpisodeTitleRequest struct {
	Source  string
	WorkID  string
	Season  int
	Episode int
}

// NotificationEpisodeTitle is one definite provider answer. Known=false means
// the provider has no title for the episode; lookup failures are errors.
type NotificationEpisodeTitle struct {
	Known bool
	Title string
}

// NotificationEpisodeTitleResolver performs a cache-first provider lookup
// during notification admission. Delivery rendering and retries never receive
// this interface and can only read the title frozen into the payload.
type NotificationEpisodeTitleResolver interface {
	ResolveNotificationEpisodeTitle(context.Context, NotificationEpisodeTitleRequest) (NotificationEpisodeTitle, error)
}
