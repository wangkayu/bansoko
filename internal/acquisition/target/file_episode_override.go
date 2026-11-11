package target

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
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
// is final and never receives the automatic episode offset.
type FileEpisodeOverride struct {
	Path    string `json:"path"`
	Season  *int   `json:"season,omitempty"`
	Episode int    `json:"episode"`
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
		if override.Season != nil && *override.Season < 0 {
			return nil, fmt.Errorf("%w: season must be at least 0", ErrFileEpisodeOverrideInvalid)
		}
		seen[path] = struct{}{}
		canonical := FileEpisodeOverride{Path: path, Episode: override.Episode}
		if override.Season != nil {
			season := *override.Season
			canonical.Season = &season
		}
		out = append(out, canonical)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// ManualFileEpisodeIdentity resolves an explicit final integer without
// consulting filename evidence or applying an offset. The synthetic value
// evidence keeps the ordinary resolver's identity and coverage-key contract.
func ManualFileEpisodeIdentity(path string, episode int) episodeidentity.EpisodeIdentity {
	return episodeidentity.NewResolver().Resolve(episodeidentity.EpisodeIdentityInput{
		MediaKind: "tv",
		FileRef:   path,
		FileEvidence: episodeidentity.NewValueEvidence(
			episodeidentity.EpisodeSourceFileParser,
			"file_name",
			strconv.Itoa(episode),
		),
	})
}

func fileEpisodeOverrideMap(overrides []FileEpisodeOverride) map[string]FileEpisodeOverride {
	out := make(map[string]FileEpisodeOverride, len(overrides))
	for _, override := range overrides {
		out[strings.TrimSpace(override.Path)] = override
	}
	return out
}
