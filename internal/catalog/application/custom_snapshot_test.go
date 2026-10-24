package application

import (
	acquisitiondomain "bansoko/internal/acquisition/domain"
	"bansoko/internal/apperrors"
	"bansoko/internal/persistence/txscope"
	"context"
	"errors"
	"testing"
)

type customRevisionStub struct {
	source FrozenCustomSnapshotSource
	err    error
}

func (r customRevisionStub) ResolveCustomSnapshotSource(context.Context, string, int64, int64) (FrozenCustomSnapshotSource, error) {
	return r.source, r.err
}

type customFetchCounter struct{ calls int }

func (f *customFetchCounter) FetchCustomSnapshot(context.Context, string) ([]acquisitiondomain.FeedItem, error) {
	f.calls++
	return nil, errors.New("bad feed")
}

// Embedding is deliberate: these rejected requests must never call persistence.
type unusedCustomSnapshotStore struct{ FeedSnapshotStore }

func TestCustomExplicitFetchRejectsBeforeNetworkAndReportsBadFeed(t *testing.T) {
	counter := &customFetchCounter{}
	app := CustomSnapshotApplication{Revisions: customRevisionStub{source: FrozenCustomSnapshotSource{Identity: "candidate:revision:source", GroupName: "Only Group", Locator: "https://example.test/feed"}}, Fetcher: counter, Snapshots: unusedCustomSnapshotStore{}}
	if _, err := app.Fetch(context.Background(), "candidate", 0, 1); err == nil {
		t.Fatal("missing revision accepted")
	}
	if counter.calls != 0 {
		t.Fatal("invalid request fetched")
	}
	app.Revisions = customRevisionStub{err: errors.New("version conflict")}
	if _, err := app.Fetch(context.Background(), "candidate", 1, 1); err == nil {
		t.Fatal("conflict accepted")
	}
	if counter.calls != 0 {
		t.Fatal("conflict fetched")
	}
	app.Revisions = customRevisionStub{source: FrozenCustomSnapshotSource{Identity: "candidate:revision:source", GroupName: "Only Group", Locator: "https://example.test/feed"}}
	_, err := app.Fetch(context.Background(), "candidate", 1, 1)
	var business *apperrors.Business
	if !errors.As(err, &business) || business.Code != "CUSTOM_FEED_FETCH_FAILED" {
		t.Fatalf("failure not observable: %v", err)
	}
	if counter.calls != 1 {
		t.Fatalf("explicit fetch calls=%d", counter.calls)
	}
}

type customOwnerStub struct {
	source FrozenCustomSnapshotSource
	err    error
}

func (r customOwnerStub) ResolveOwnerCustomSnapshotSource(context.Context, string) (FrozenCustomSnapshotSource, error) {
	return r.source, r.err
}

func (r customOwnerStub) ValidateOwnerCustomSnapshotSourceTx(context.Context, txscope.Tx, string, FrozenCustomSnapshotSource) error {
	return r.err
}

// The pending-owner bench fetch resolves the owner's source first: a refused
// owner (started, retired, not custom) never reaches the network.
func TestCustomOwnerFetchRejectsBeforeNetwork(t *testing.T) {
	counter := &customFetchCounter{}
	app := CustomSnapshotApplication{Fetcher: counter, Snapshots: unusedCustomSnapshotStore{}}
	_, err := app.FetchOwner(context.Background(), "anime-1")
	var business *apperrors.Business
	if !errors.As(err, &business) || business.Code != "CUSTOM_FEED_SNAPSHOT_UNAVAILABLE" {
		t.Fatalf("unwired owner reader = %v", err)
	}
	started := &apperrors.Business{Status: 409, Code: "CUSTOM_SNAPSHOT_SCOPE_INVALID", Message: "started"}
	app.Owners = customOwnerStub{err: started}
	if _, err := app.FetchOwner(context.Background(), "anime-1"); !errors.Is(err, started) {
		t.Fatalf("started owner = %v", err)
	}
	if counter.calls != 0 {
		t.Fatal("a refused owner fetched")
	}
	app.Owners = customOwnerStub{source: FrozenCustomSnapshotSource{Identity: "custom-source:x", GroupRef: "custom-group:x", GroupName: "Only Group", Locator: "https://example.test/feed"}}
	if _, err := app.FetchOwner(context.Background(), "anime-1"); !errors.As(err, &business) || business.Code != "CUSTOM_FEED_FETCH_FAILED" {
		t.Fatalf("feed failure not observable: %v", err)
	}
	if counter.calls != 1 {
		t.Fatalf("owner fetch calls=%d", counter.calls)
	}
}
