package application

import (
	"context"
	"errors"
	"strings"
	"testing"

	animeapplication "bansoko/internal/anime/application"
	"bansoko/internal/apperrors"
)

// Human admission reads the seed's exact persisted torrent bytes instead of
// fetching the locator again. These tests pin that boundary: the bytes come from
// the snapshot the user reviewed, they are re-parsed in-process, and any
// identity or content problem fails closed with zero owner writes.

func persistedTorrentBytes(t *testing.T, name string, length int64) []byte {
	t.Helper()
	data := "d4:infod4:name" + itoa(len(name)) + ":" + name + "6:lengthi" + itoa64(length) + "eee"
	return []byte(data)
}

func itoa(value int) string {
	return itoa64(int64(value))
}

func itoa64(value int64) string {
	if value == 0 {
		return "0"
	}
	negative := value < 0
	if negative {
		value = -value
	}
	var digits []byte
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	if negative {
		return "-" + string(digits)
	}
	return string(digits)
}

func TestSelectedSeedHasSnapshotIdentity(t *testing.T) {
	base := boundOneShotSeed()
	if !selectedSeedHasSnapshotIdentity(base) {
		t.Fatal("a bound seed must report its snapshot identity")
	}
	cases := map[string]func(*SelectedSeed){
		"missing snapshot id":   func(s *SelectedSeed) { s.SnapshotID = "" },
		"zero snapshot version": func(s *SelectedSeed) { s.SnapshotVersion = 0 },
		"missing content hash":  func(s *SelectedSeed) { s.SnapshotContentHash = "" },
		"blank snapshot id":     func(s *SelectedSeed) { s.SnapshotID = "   " },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			seed := base
			mutate(&seed)
			if selectedSeedHasSnapshotIdentity(seed) {
				t.Fatalf("seed without a complete snapshot identity was accepted: %+v", seed)
			}
		})
	}
}

func TestReadPersistedSeedPayloadParsesWithoutFetching(t *testing.T) {
	bytes := persistedTorrentBytes(t, "show.mkv", 12)
	source := &fakeSeedPreviewSource{payload: animeapplication.SeedPayload{
		Bytes: bytes, PayloadDigest: "sha256:" + strings.Repeat("a", 64), StableSeedKey: "seed-01",
	}}
	service := &Service{seedPreviews: source}
	seed := boundOneShotSeed()

	payload, err := service.readPersistedSeedPayload(context.Background(), seed)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(payload.Bytes) != string(bytes) {
		t.Fatalf("payload bytes drifted: %q", payload.Bytes)
	}
	if payload.Metadata.TorrentName != "show.mkv" || payload.Metadata.TotalSize != 12 {
		t.Fatalf("payload was not parsed in-process: %+v", payload.Metadata)
	}
	if source.payloadCalls != 1 || source.calls != 0 {
		t.Fatalf("payload reads = %d, snapshot reads = %d; want exactly one payload read and no group read", source.payloadCalls, source.calls)
	}
	// The read names the seed's exact declared identity, never "latest".
	if source.lastIdentity.SnapshotID != seed.SnapshotID || source.lastIdentity.SnapshotVersion != seed.SnapshotVersion || source.lastIdentity.ContentHash != seed.SnapshotContentHash {
		t.Fatalf("source identity = %+v, want the seed's exact identity", source.lastIdentity)
	}
	if source.lastSeedKey != seed.StableSeedKey {
		t.Fatalf("source seed key = %q, want %q", source.lastSeedKey, seed.StableSeedKey)
	}
}

func TestReadPersistedSeedPayloadFailsClosed(t *testing.T) {
	seed := boundOneShotSeed()
	cases := []struct {
		name     string
		source   *fakeSeedPreviewSource
		service  *Service
		wantCode string
	}{
		{
			name:     "unconfigured source",
			service:  &Service{},
			wantCode: "SEED_PREVIEW_UNAVAILABLE",
		},
		{
			name:     "refresh required",
			source:   &fakeSeedPreviewSource{payloadErr: errRefreshRequiredForTest()},
			wantCode: "SEED_PREVIEW_STALE",
		},
		{
			name:     "unparseable persisted payload",
			source:   &fakeSeedPreviewSource{payload: animeapplication.SeedPayload{Bytes: []byte("not bencode"), StableSeedKey: "seed-01"}},
			wantCode: "TORRENT_FETCH_FAILED",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			service := tc.service
			if service == nil {
				service = &Service{seedPreviews: tc.source}
			}
			_, err := service.readPersistedSeedPayload(context.Background(), seed)
			business, ok := err.(*apperrors.Business)
			if !ok {
				t.Fatalf("error %v is not a typed business error", err)
			}
			if business.Code != tc.wantCode {
				t.Fatalf("code = %q, want %q", business.Code, tc.wantCode)
			}
		})
	}
}

func TestReadPersistedSeedPayloadPropagatesStoreFailure(t *testing.T) {
	source := &fakeSeedPreviewSource{payloadErr: errors.New("catalog store unavailable")}
	service := &Service{seedPreviews: source}
	_, err := service.readPersistedSeedPayload(context.Background(), boundOneShotSeed())
	if err == nil {
		t.Fatal("a store failure must not be swallowed")
	}
	if _, ok := err.(*apperrors.Business); ok {
		t.Fatalf("an infrastructure failure must not be misreported as a typed business rejection: %v", err)
	}
	if !strings.Contains(err.Error(), "catalog store unavailable") {
		t.Fatalf("store failure lost its cause: %v", err)
	}
}

func errRefreshRequiredForTest() error {
	return errRefreshRequiredSentinel{}
}

type errRefreshRequiredSentinel struct{}

func (errRefreshRequiredSentinel) Error() string { return "snapshot is stale" }
func (errRefreshRequiredSentinel) Is(target error) bool {
	return target == animeapplication.ErrProviderFeedSnapshotRefreshRequired
}
