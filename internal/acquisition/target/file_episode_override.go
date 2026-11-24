package target

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"bansoko/internal/episodeidentity"
	"bansoko/internal/filekinds"
)

// MaxFileEpisodeOverrides bounds the sparse per-file numbering input. One
// override names one selected video; subtitles always follow their video.
const MaxFileEpisodeOverrides = 10000

// ErrFileEpisodeOverrideInvalid marks a malformed or out-of-scope override.
var ErrFileEpisodeOverrideInvalid = errors.New("file episode override is invalid")

// FileEpisodeOverride replaces one video's final episode and optionally its
// owner naming season. A nil Season inherits the main naming season; Episode
// is final and never receives the automatic episode offset. EpisodeEnd, when
// greater than Episode, makes the file cover the consecutive range
// Episode..EpisodeEnd (at most episodeidentity.MaxManualEpisodesPerFile episodes);
// zero or equal to Episode is the single-episode form.
type FileEpisodeOverride struct {
	Path       string `json:"path"`
	Season     *int   `json:"season,omitempty"`
	Episode    int    `json:"episode"`
	EpisodeEnd int    `json:"episode_end,omitempty"`
}

// CanonicalFileEpisodeOverrides normalizes, validates and sorts sparse
// overrides. Empty input canonicalizes to nil so it has the same identity as
// an omitted override list.
func CanonicalFileEpisodeOverrides(overrides []FileEpisodeOverride) ([]FileEpisodeOverride, error) {
	if len(overrides) == 0 {
		return nil, nil
	}
	if len(overrides) > MaxFileEpisodeOverrides {
		return nil, fmt.Errorf("%w: more than %d files", ErrFileEpisodeOverrideInvalid, MaxFileEpisodeOverrides)
	}
	seen := make(map[string]struct{}, len(overrides))
	out := make([]FileEpisodeOverride, 0, len(overrides))
	for _, override := range overrides {
		path := filekinds.NormalizePath(override.Path)
		if path == "" {
			return nil, fmt.Errorf("%w: path is blank", ErrFileEpisodeOverrideInvalid)
		}
		if len(path) > 2048 {
			return nil, fmt.Errorf("%w: path is too long", ErrFileEpisodeOverrideInvalid)
		}
		if _, duplicate := seen[path]; duplicate {
			return nil, fmt.Errorf("%w: %q is named twice", ErrFileEpisodeOverrideInvalid, path)
		}
		if override.Episode < 1 {
			return nil, fmt.Errorf("%w: episode must be at least 1", ErrFileEpisodeOverrideInvalid)
		}
		episodeEnd := override.EpisodeEnd
		if episodeEnd == override.Episode {
			// A range that names one episode is the single-episode form, so its
			// canonical JSON and request digest stay identical to omitting it.
			episodeEnd = 0
		}
		if episodeEnd != 0 {
			if episodeEnd < override.Episode {
				return nil, fmt.Errorf("%w: episode_end must not precede episode", ErrFileEpisodeOverrideInvalid)
			}
			if episodeEnd-override.Episode+1 > episodeidentity.MaxManualEpisodesPerFile {
				return nil, fmt.Errorf("%w: one file covers at most %d episodes", ErrFileEpisodeOverrideInvalid, episodeidentity.MaxManualEpisodesPerFile)
			}
		}
		if override.Season != nil && *override.Season < 0 {
			return nil, fmt.Errorf("%w: season must be at least 0", ErrFileEpisodeOverrideInvalid)
		}
		seen[path] = struct{}{}
		canonical := FileEpisodeOverride{Path: path, Episode: override.Episode, EpisodeEnd: episodeEnd}
		if override.Season != nil {
			season := *override.Season
			canonical.Season = &season
		}
		out = append(out, canonical)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// ManualFileEpisodeIdentity resolves an explicit final integer, or a
// consecutive final range when episodeEnd exceeds episode, without consulting
// filename evidence or applying an offset. The synthetic value evidence keeps
// the ordinary resolver's identity and coverage-key contract: a range resolves
// to one multi-episode identity with every covered episode key ascending.
func ManualFileEpisodeIdentity(path string, episode, episodeEnd int) episodeidentity.EpisodeIdentity {
	if episodeEnd > episode {
		return episodeidentity.NewManualRangeIdentity(path, episode, episodeEnd)
	}
	return episodeidentity.NewManualSingleIdentity(path, episode)
}

func fileEpisodeOverrideMap(overrides []FileEpisodeOverride) map[string]FileEpisodeOverride {
	out := make(map[string]FileEpisodeOverride, len(overrides))
	for _, override := range overrides {
		out[strings.TrimSpace(override.Path)] = override
	}
	return out
}
