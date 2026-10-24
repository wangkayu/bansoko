package application

import (
	"bansoko/internal/testutil/presetdraft"
	"context"
	"errors"
	"testing"

	"bansoko/internal/apperrors"
	"bansoko/internal/testutil/animetest"
	"bansoko/internal/transport/httpapi/generated"
)

// agentTestDraft returns a complete AnimeDraft with the oneshot origin: an
// instance_label is required and zero RSS sources are allowed. The origin is
// valid as a candidate snapshot for one-shot acquisition only.
func agentTestDraft(t *testing.T) generated.AnimeDraft {
	t.Helper()
	season := 1
	return generated.AnimeDraft{
		Origin:        generated.SubscriptionOriginOneshot,
		InstanceLabel: animetest.StringPtr("agent-run-1"),
		Title:         "Agent Show",
		Metadata: generated.AnimeMetadataDraft{
			Bgm:  &generated.NullableBGMBinding{WorkId: "bgm-agent"},
			Tmdb: &generated.NullableTMDBBinding{WorkId: "tmdb-agent", MediaKind: generated.TMDBMediaKindTv, Season: season},
		},
		Subgroups:  draftDefaultSubgroups(t),
		Rules:      draftDefaultRules(t),
		RssSources: []generated.RSSSourceDraft{},
	}
}

func assertOriginNotSubscribable(t *testing.T, err error) {
	t.Helper()
	var business *apperrors.Business
	if !errors.As(err, &business) || business.Status != 422 || business.Code != "ORIGIN_NOT_SUBSCRIBABLE" {
		t.Fatalf("error = %v, want 422 ORIGIN_NOT_SUBSCRIBABLE", err)
	}
}

func assertNoSubscriptionRows(t *testing.T, service *Service) {
	t.Helper()
	db := service.db
	if animetest.CountRows(t, db, "managed_anime") != 0 || animetest.CountRows(t, db, "subscriptions") != 0 {
		t.Fatal("a rejected oneshot-origin subscribe must write no managed anime or subscription")
	}
}

// TestSubscribeRejectsAgentOriginDraft proves the direct subscribe path no
// longer creates a persistent oneshot skeleton: the oneshot origin is for
// one-shot acquisition only.
func TestSubscribeRejectsAgentOriginDraft(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := draftNewTestService(t, db)
	_, err := service.SubscribeAnime(context.Background(), generated.SubscribeAnimeInput{Draft: presetdraft.Load(t, service.db, agentTestDraft(t)), Start: new(true)}, "subscribe-key-agent-001")
	assertOriginNotSubscribable(t, err)
	assertNoSubscriptionRows(t, service)
}

// TestActivateRejectsAgentOriginCandidate proves an oneshot-origin candidate can
// still be saved (it is the one-shot input snapshot) but never activated into a
// persistent subscription.
func TestActivateRejectsAgentOriginCandidate(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service := draftNewTestService(t, db)
	candidate := saveCandidate(t, service, agentTestDraft(t))
	if candidate.CurrentRevision == nil {
		t.Fatal("agent candidate must save a revision for one-shot input")
	}
	_, err := service.ActivateAnimeConfigCandidate(context.Background(), candidate.Id, candidate.Version, "activate-key-agent-001", CandidateSubscribeInput{Revision: *candidate.CurrentRevision, Start: true})
	assertOriginNotSubscribable(t, err)
	assertNoSubscriptionRows(t, service)
}
