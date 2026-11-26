package application

import (
	"encoding/json"
	"testing"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	downloadadmission "bansoko/internal/download/admission"
	downloadapplication "bansoko/internal/download/application"
	downloaddomain "bansoko/internal/download/domain"

	"github.com/stretchr/testify/require"
)

func TestRSSFrozenDownloadInputContract(t *testing.T) {
	snapshot := releaseSnapshot{
		releaseID: "release-new", sourceID: "source-new", stableItemKey: "guid:seed",
		releaseVersion: 2, subscriptionID: "sub-new", managedAnimeID: "anime-new",
		locatorDisplay: "https://feeds.example.test", rawFacts: json.RawMessage(`{"title":"Episode"}`),
		sourceFacts: json.RawMessage(`{}`),
	}
	ref := releaseTorrentInputRef{ID: "torrent-input", PayloadDigest: "sha256:fixture", Filename: "episode.torrent", ParserVersion: "parser-1", TorrentVersion: "v1", InfoHashV1: "hash"}
	for _, adopt := range []bool{false, true} {
		name := "normal"
		acquisition := &targetAcquisition{}
		acquisition.set.Digest = "sha256:targets"
		item := acquisitiondomain.FeedItem{TorrentInfoHash: "hash"}
		if adopt {
			name = "cross-owner adoption"
			item.ReleaseFiles = []acquisitiondomain.ReleaseFile{{SourcePath: "episode.mkv", Filename: "episode.mkv"}}
			acquisition.downloaderAdmission = &downloadadmission.Result{
				PathSnapshot: downloaddomain.PathSnapshot{RemoteRoot: "/downloads", ApplicationRoot: "/downloads"},
				Provenance: &downloadadmission.Provenance{
					Kind: "unique_infohash_path", ProviderTaskID: "tag:previous-owner", ProviderInfoHash: "hash", OperationKeyChecked: "qb:release-new",
				},
			}
		}
		t.Run(name, func(t *testing.T) {
			raw, err := encodeFrozenDownloadInput(snapshot, item, ref, acquisition)
			require.NoError(t, err)
			decoded, err := downloadapplication.DecodeFrozenInput(raw)
			require.NoError(t, err)
			require.NotEmpty(t, decoded.SelectedRelease)
			require.NotEmpty(t, decoded.TargetFacts)
			if adopt {
				require.Equal(t, acquisition.downloaderAdmission.Provenance, decoded.AdoptionProvenance)
				require.Equal(t, &acquisition.downloaderAdmission.PathSnapshot, decoded.PathSnapshot)
				require.NotEmpty(t, decoded.ReleaseFiles)
			} else {
				require.Nil(t, decoded.AdoptionProvenance)
				require.Nil(t, decoded.ReleaseFiles)
			}
			roundTrip, err := json.Marshal(decoded)
			require.NoError(t, err)
			require.JSONEq(t, string(raw), string(roundTrip))
		})
	}
}
