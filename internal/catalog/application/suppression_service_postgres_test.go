package application

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"bansoko/internal/catalog/domain"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// testEntryRef mints a provider entry reference that is unique per run. The
// suppression table is keyed on (provider, provider_entry_ref) with no season
// column, so a fixed ref would leak between runs of the shared test database.
func testEntryRef(offset int64) string {
	return strconv.FormatInt(time.Now().UTC().UnixNano()%1_000_000_000_000+offset, 10)
}

// TestCatalogSuppressionsAreIdempotentAndProviderScoped covers the two
// properties the frontend blacklist depends on: repeating a call never fails,
// and the same entry ref under two providers stays two independent records.
func TestCatalogSuppressionsAreIdempotentAndProviderScoped(t *testing.T) {
	db := openCatalogTestDB(t)
	ctx := context.Background()
	service := NewService(db, nil)
	ref := testEntryRef(0)
	t.Cleanup(func() {
		for _, provider := range []string{ProviderMikan, ProviderAniBT} {
			_ = service.UnsuppressEntry(context.Background(), provider, ref)
		}
	})

	first, err := service.SuppressEntry(ctx, ProviderMikan, ref, "Mikan title")
	if err != nil {
		t.Fatal(err)
	}
	if first.Version != 1 || first.Provider != ProviderMikan || first.ProviderEntryRef != ref {
		t.Fatalf("unexpected first suppression: %+v", first)
	}

	repeat, err := service.SuppressEntry(ctx, ProviderMikan, ref, "Mikan title")
	if err != nil {
		t.Fatalf("repeated suppress was not idempotent: %v", err)
	}
	if repeat.ID != first.ID || repeat.Version != first.Version {
		t.Fatalf("identical re-suppress churned the record: first=%+v repeat=%+v", first, repeat)
	}

	retitled, err := service.SuppressEntry(ctx, ProviderMikan, ref, "Mikan title (renamed)")
	if err != nil {
		t.Fatal(err)
	}
	if retitled.ID != first.ID || retitled.TitleSnapshot != "Mikan title (renamed)" || retitled.Version != first.Version+1 {
		t.Fatalf("retitle did not update in place: %+v", retitled)
	}

	other, err := service.SuppressEntry(ctx, ProviderAniBT, ref, "AniBT title")
	if err != nil {
		t.Fatal(err)
	}
	if other.ID == first.ID {
		t.Fatalf("same ref under a second provider collided with the first: %+v", other)
	}

	listed := suppressionsForRef(ctx, t, service, ref)
	if len(listed) != 2 {
		t.Fatalf("expected one suppression per provider, got %+v", listed)
	}

	if err := service.UnsuppressEntry(ctx, ProviderMikan, ref); err != nil {
		t.Fatal(err)
	}
	remaining := suppressionsForRef(ctx, t, service, ref)
	if len(remaining) != 1 || remaining[0].Provider != ProviderAniBT {
		t.Fatalf("unsuppressing one provider affected the other: %+v", remaining)
	}
	if err := service.UnsuppressEntry(ctx, ProviderMikan, ref); err != nil {
		t.Fatalf("repeated unsuppress was not idempotent: %v", err)
	}
	if err := service.UnsuppressEntry(ctx, ProviderMikan, testEntryRef(1)); err != nil {
		t.Fatalf("unsuppressing an entry that was never suppressed failed: %v", err)
	}

	if _, err := service.SuppressEntry(ctx, ProviderMikan, "not-a-ref", "title"); !errors.Is(err, ErrSuppressionInvalid) {
		t.Fatalf("malformed entry ref was not rejected as invalid: %v", err)
	}
	if _, err := service.SuppressEntry(ctx, ProviderMikan, ref, ""); !errors.Is(err, ErrSuppressionInvalid) {
		t.Fatalf("empty title snapshot was not rejected as invalid: %v", err)
	}
}

// TestWorkspaceProviderMarksSuppressedEntriesIgnored pins the read model to the
// suppression table instead of the hardcoded neutral it used to return.
func TestWorkspaceProviderMarksSuppressedEntriesIgnored(t *testing.T) {
	db := openCatalogTestDB(t)
	ctx := context.Background()
	season := "suppress-test-" + time.Now().UTC().Format("150405.000000")
	suppressedRef, visibleRef := testEntryRef(0), testEntryRef(1)
	service := NewService(db, nil)
	service.SetProviderReader(ProviderMikan, catalogProviderReader{result: domain.ReadResult{
		SourceVersion: "mikan-source-1", ContractVersion: mikanSeasonContractVersion,
		Entries: []domain.Entry{
			{ProviderEntryRef: suppressedRef, Title: "Suppressed show"},
			{ProviderEntryRef: visibleRef, Title: "Visible show"},
		},
	}})
	t.Cleanup(func() { _ = service.UnsuppressEntry(context.Background(), ProviderMikan, suppressedRef) })

	if _, err := service.RefreshProviderSeason(ctx, ProviderMikan, season); err != nil {
		t.Fatal(err)
	}
	before, err := service.WorkspaceProvider(ctx, ProviderMikan, season)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range before.Items {
		if item.Preference != domain.PreferenceNeutral {
			t.Fatalf("entry was not neutral before suppression: %+v", item)
		}
	}

	if _, err := service.SuppressEntry(ctx, ProviderMikan, suppressedRef, "Suppressed show"); err != nil {
		t.Fatal(err)
	}
	after, err := service.WorkspaceProvider(ctx, ProviderMikan, season)
	if err != nil {
		t.Fatal(err)
	}
	preferences := map[string]domain.ItemPreference{}
	for _, item := range after.Items {
		preferences[item.ProviderEntryRef] = item.Preference
	}
	if preferences[suppressedRef] != domain.PreferenceIgnored {
		t.Fatalf("suppressed entry was not reported as ignored: %+v", preferences)
	}
	if preferences[visibleRef] != domain.PreferenceNeutral {
		t.Fatalf("suppression leaked onto an unrelated entry: %+v", preferences)
	}
}

func suppressionsForRef(ctx context.Context, t *testing.T, service *Service, ref string) []domain.Suppression {
	t.Helper()
	all, err := service.ListSuppressions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	scoped := make([]domain.Suppression, 0, 2)
	for _, suppression := range all {
		if suppression.ProviderEntryRef == ref {
			scoped = append(scoped, suppression)
		}
	}
	return scoped
}
