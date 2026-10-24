package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	animeapplication "bansoko/internal/anime/application"
	rssapplication "bansoko/internal/rss/application"
	"bansoko/internal/transport/httpapi/generated"
)

func TestFrozenDownloadMappersPreserveNeutralFields(t *testing.T) {
	managedAnimeID := "anime-1"
	input, err := mapFrozenDownloadInput(animeapplication.FrozenDownloadInput{
		ReleaseStableItemKey: "stable-1", SelectedURLDigest: downloadStringPointer("sha256:url"), SourceLocatorDisplay: "https://example.test/feed",
		SelectedRelease: &animeapplication.SelectedReleaseIdentity{
			ReleaseID: "release-1", RSSSourceID: "source-1", StableItemKey: "stable-1",
			RawFacts: json.RawMessage(`{"title":"frozen"}`), SourceFacts: json.RawMessage(`{}`), InternalFacts: json.RawMessage(`{}`),
			ReleaseVersion: 2, SubscriptionID: "subscription-1", ManagedAnimeID: managedAnimeID,
		},
		SelectedSeed: json.RawMessage(`{"locator":"https://example.test/seed.torrent","provider":"mikan","provider_entry_ref":"entry-1","provider_group_ref":"group-1","published_at":"2026-09-11T00:00:00Z","title":"seed"}`),
		TargetFacts:  json.RawMessage(`{"admission":"download_required","targets":[],"observations":[]}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if input.SelectedUrlDigest == nil || *input.SelectedUrlDigest != "sha256:url" || input.SelectedRelease == nil || input.SelectedRelease.ManagedAnimeId != generated.ID(managedAnimeID) || input.SelectedSeed == nil || input.TargetFacts == nil {
		t.Fatalf("frozen input was not completely mapped: %+v", input)
	}
	policy, err := mapFrozenPolicySnapshot(animeapplication.FrozenPolicySnapshot{
		EvaluatorVersion: downloadStringPointer("eval-1"), MetadataVersion: 3, RulesVersion: 4,
		EffectiveSections: json.RawMessage(`{"metadata":{"title":"frozen"}}`), EffectiveDigest: "sha256:" + strings.Repeat("a", 64),
		SectionVersions: map[string]int64{"metadata": 3}, SectionSchemas: map[string]string{"metadata": "v2"},
		ConfigIdentity: animeapplication.ConfigRevisionIdentity{IdentityKind: "instance_materialized", ManagedAnimeID: &managedAnimeID, EffectiveDigest: "sha256:" + strings.Repeat("a", 64)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if policy.RulesVersion != 4 || policy.ConfigIdentity.IdentityKind != generated.ConfigRevisionIdentityIdentityKindInstanceMaterialized || policy.EffectiveSections["metadata"] == nil || policy.SectionVersions["metadata"] != 3 {
		t.Fatalf("frozen policy was not completely mapped: %+v", policy)
	}
}

func TestReleaseRetryMappers(t *testing.T) {
	accepted := mapReleaseRetryResult(rssapplication.ReleaseRetryResult{ReleaseID: "rel-1", Verdict: "accepted_pending_download", Outcome: rssapplication.RetryOutcomeAccepted, DownloadTaskID: "task-2"})
	if accepted.Outcome != generated.ReleaseRetryResultOutcomeAccepted || accepted.DownloadTaskId == nil || *accepted.DownloadTaskId != "task-2" || accepted.ReasonCode != nil {
		t.Fatalf("accepted = %+v", accepted)
	}
	filtered := mapReleaseRetryResult(rssapplication.ReleaseRetryResult{ReleaseID: "rel-1", Verdict: "filtered", Outcome: rssapplication.RetryOutcomeFiltered, ReasonCode: "filtered_by_rules"})
	if filtered.DownloadTaskId != nil || filtered.ReasonCode == nil || *filtered.ReasonCode != "filtered_by_rules" {
		t.Fatalf("filtered = %+v", filtered)
	}
	var apiErr *APIError
	if !errors.As(mapReleaseRetryError(&rssapplication.ReleaseNotRetryableError{Reason: rssapplication.RetryReasonDownloadInProgress}), &apiErr) {
		t.Fatal("not retryable must map to an API error")
	}
	if apiErr.Status != http.StatusConflict || apiErr.Code != "RELEASE_NOT_RETRYABLE" || len(apiErr.Details) != 1 || apiErr.Details[0].Code != rssapplication.RetryReasonDownloadInProgress {
		t.Fatalf("not retryable = %+v", apiErr)
	}
	other := errors.New("other")
	if mapReleaseRetryError(other) != other {
		t.Fatal("other errors pass through")
	}
}

func downloadStringPointer(value string) *string { return &value }
