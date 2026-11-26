package application

import (
	"encoding/json"
	"testing"

	animeapplication "bansoko/internal/anime/application"
	downloadapplication "bansoko/internal/download/application"
	downloaddomain "bansoko/internal/download/domain"

	"github.com/stretchr/testify/require"
)

func TestOneShotFrozenDownloadInputContract(t *testing.T) {
	release := animeapplication.SelectedReleaseIdentity{
		ReleaseID: "release-once", RSSSourceID: "source-once", StableItemKey: "seed:once",
		ReleaseVersion: 1, SubscriptionID: "sub-once", ManagedAnimeID: "anime-once",
		RawFacts: json.RawMessage(`{"title":"Episode"}`), SourceFacts: json.RawMessage(`{}`),
	}
	seed := SelectedSeed{Locator: "https://seeds.example.test/episode.torrent", StableSeedKey: "seed:once", Title: "Episode"}
	for _, tc := range []struct {
		name, internalFacts, wantReleaseFiles string
		partial                               *downloaddomain.PartialDownload
	}{
		{name: "normal with no inventory", internalFacts: `{}`},
		{name: "normal with explicit empty inventory", internalFacts: `{"release_files":[]}`, wantReleaseFiles: `[]`},
		{
			name: "partial download", partial: &downloaddomain.PartialDownload{Paths: []string{"pack/01.mkv"}, FileCount: 2},
			internalFacts:    `{"release_files":[{"source_path":"pack/01.mkv","size_bytes":100},{"source_path":"pack/02.mkv","size_bytes":200}]}`,
			wantReleaseFiles: `[{"source_path":"pack/01.mkv","size_bytes":100},{"source_path":"pack/02.mkv","size_bytes":200}]`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := encodeOneShotFrozenInput(downloaddomain.FrozenInput{
				ReleaseStableItemKey: release.StableItemKey, TargetFacts: json.RawMessage(`{"target_count":1}`),
				TargetSetDigest: "sha256:targets", TorrentInputID: "torrent-input", TorrentPayloadDigest: "sha256:fixture",
				TorrentFilename: "episode.torrent", TorrentParserVersion: "parser-1", TorrentVersion: "v1",
				InfoHashV1: "hash", PartialDownload: tc.partial,
			}, release, seed, []byte(tc.internalFacts))
			require.NoError(t, err)
			decoded, err := downloadapplication.DecodeFrozenInput(raw)
			require.NoError(t, err)
			require.NotEmpty(t, decoded.SelectedRelease)
			require.NotEmpty(t, decoded.SelectedSeed)
			require.Equal(t, tc.partial, decoded.PartialDownload)
			require.Equal(t, tc.wantReleaseFiles, string(decoded.ReleaseFiles))
			roundTrip, err := json.Marshal(decoded)
			require.NoError(t, err)
			require.JSONEq(t, string(raw), string(roundTrip))
		})
	}
}
