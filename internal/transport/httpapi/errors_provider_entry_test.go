package httpapi

import (
	"net/http"
	"testing"

	"bansoko/internal/apperrors"
)

func TestProviderEntryAlreadySubscribedMapsToTypedConflictWithIDs(t *testing.T) {
	mapped := mapApplicationError(&apperrors.ProviderEntryAlreadySubscribed{ManagedAnimeIDs: []string{"anime-1", "anime-2"}})
	if mapped.Status != http.StatusConflict || mapped.Code != "PROVIDER_ENTRY_ALREADY_SUBSCRIBED" {
		t.Fatalf("unexpected mapping: %+v", mapped)
	}
	if len(mapped.Details) != 1 || mapped.Details[0].ManagedAnimeIds == nil || len(*mapped.Details[0].ManagedAnimeIds) != 2 || (*mapped.Details[0].ManagedAnimeIds)[0] != "anime-1" {
		t.Fatalf("details must carry the existing ids: %+v", mapped.Details)
	}
	if !isKnownApplicationError(&apperrors.ProviderEntryAlreadySubscribed{}) {
		t.Fatal("must be a known application error, not an unknown 500")
	}
}
