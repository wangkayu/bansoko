package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	adapterconfig "bansoko/internal/plugintarget/config"
	plugintargetport "bansoko/internal/plugintarget/port"
	"bansoko/internal/templatecatalog/notification"
)

const maxNotificationEpisodeNumber = 10000

var errNotificationEpisodeTitleResolverUnavailable = errors.New("notification episode title resolver is not wired")

// resolveAdmissionEpisodeTitles fills only the provider title facts referenced
// by this target version's selected templates. Existing frozen titles win; a
// missing title is resolved through the shared cache before the delivery
// transaction starts. Unbound sources and invalid episode identities freeze as
// null without calling the resolver.
func (r *AdmissionResolver) resolveAdmissionEpisodeTitles(ctx context.Context, target TargetConfig, request plugintargetport.BusinessDeliveryRequest) (json.RawMessage, error) {
	if target.ConfigStatus != adapterconfig.StatusAvailable {
		return request.Payload, nil
	}
	titleTemplate, bodyTemplate, err := selectTemplate(target.TemplateSet, TriggerKind(request.TriggerKind))
	if err != nil {
		return nil, fmt.Errorf("select notification template for episode title resolution: %w", err)
	}
	references := notification.TemplateReferences(titleTemplate, bodyTemplate)
	if !references["bgm.ep_title"] && !references["tmdb.ep_title"] {
		return request.Payload, nil
	}
	facts, ok := notification.DecodeFacts(request.Payload)
	if !ok {
		return nil, errors.New("decode notification payload for episode title resolution")
	}
	for _, key := range notification.EpisodeTitleFactKeys {
		if references[key] {
			if _, present := facts[key]; !present {
				facts[key] = nil
			}
		}
	}
	episode, episodeReady := notificationEpisodeInt(facts["ep.number"], 1, maxNotificationEpisodeNumber)

	for _, source := range []struct {
		fact   string
		workID string
		bound  bool
	}{
		{fact: "bgm.ep_title", workID: notificationEpisodeString(facts["bgm.id"]), bound: notificationEpisodeString(facts["bgm.id"]) != ""},
		{fact: "tmdb.ep_title", workID: notificationEpisodeString(facts["tmdb.id"]), bound: notificationEpisodeString(facts["tmdb.id"]) != "" && strings.EqualFold(notificationEpisodeString(facts["tmdb.kind"]), "tv")},
	} {
		if !references[source.fact] || notificationEpisodeTitlePresent(facts[source.fact]) {
			continue
		}
		season := 0
		bound := episodeReady && source.bound
		if source.fact == "tmdb.ep_title" {
			var seasonReady bool
			season, seasonReady = notificationEpisodeInt(facts["tmdb.season"], 0, maxNotificationEpisodeNumber)
			bound = bound && seasonReady
		}
		if !bound {
			continue
		}
		if r.EpisodeTitles == nil {
			return nil, errNotificationEpisodeTitleResolverUnavailable
		}
		answer, err := r.EpisodeTitles.ResolveNotificationEpisodeTitle(ctx, plugintargetport.NotificationEpisodeTitleRequest{
			Source:  strings.TrimSuffix(source.fact, ".ep_title"),
			WorkID:  source.workID,
			Season:  season,
			Episode: episode,
		})
		if err != nil {
			return nil, fmt.Errorf("resolve %s during notification admission: %w", source.fact, err)
		}
		facts[source.fact] = nil
		if answer.Known {
			facts[source.fact] = notification.Text(answer.Title)
		}
	}
	encoded, err := json.Marshal(facts)
	if err != nil {
		return nil, fmt.Errorf("encode notification episode title facts: %w", err)
	}
	return maskPayloadURLs(encoded), nil
}

func notificationEpisodeTitlePresent(value any) bool {
	text, ok := value.(string)
	return ok && strings.TrimSpace(text) != ""
}

func notificationEpisodeString(value any) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

func notificationEpisodeInt(value any, minimum, maximum int) (int, bool) {
	var parsed int64
	var err error
	switch number := value.(type) {
	case json.Number:
		parsed, err = strconv.ParseInt(number.String(), 10, 64)
	case string:
		parsed, err = strconv.ParseInt(strings.TrimSpace(number), 10, 64)
	case float64:
		if number != math.Trunc(number) || number >= math.MaxInt64 || number < math.MinInt64 {
			return 0, false
		}
		parsed = int64(number)
	case int:
		parsed = int64(number)
	case int64:
		parsed = number
	default:
		return 0, false
	}
	if err != nil || parsed < int64(minimum) || parsed > int64(maximum) {
		return 0, false
	}
	return int(parsed), true
}
