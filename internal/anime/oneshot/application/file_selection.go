package application

import (
	"fmt"
	"path"
	"sort"
	"strings"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/apperrors"
	"bansoko/internal/filekinds"
)

// maxOneShotSelectedPathBytes bounds one selected manifest path, the same
// bound the RSS file row publishes for source_path.
const maxOneShotSelectedPathBytes = 2048

// The manual one-shot file selection (owner 10-27). The user chooses the
// exact files of one seed to import, at any depth; the selection replaces the
// product's first-level gate and nothing else. The kind gate (video and
// subtitle only), the file rules, filename episode identity, the movie main
// video, the unknown-file route, same-directory subtitle binding and the
// collision policy all apply to the chosen files unchanged. The download is
// unchanged too: qBittorrent still downloads the whole torrent, and an
// unselected file stays in the download location untouched.

func fileSelectionInvalid(format string, args ...any) error {
	return &apperrors.Business{Status: 422, Code: "FILE_SELECTION_INVALID", Message: fmt.Sprintf(format, args...)}
}

// canonicalOneShotFileSelection checks the request shape of a manual selection
// and returns it canonical: normalized manifest paths in ascending order. nil
// stays nil (the product default); a present selection must name at least one
// file, with no blank, oversized or duplicate path.
func canonicalOneShotFileSelection(paths []string) ([]string, error) {
	if paths == nil {
		return nil, nil
	}
	if len(paths) == 0 {
		return nil, fileSelectionInvalid("file_selection must name at least one file; omit it for the default files")
	}
	if len(paths) > animedomain.MaxFilterInventoryEntries {
		return nil, fileSelectionInvalid("file_selection names more than %d files", animedomain.MaxFilterInventoryEntries)
	}
	seen := make(map[string]struct{}, len(paths))
	out := make([]string, 0, len(paths))
	for _, value := range paths {
		normalized := filekinds.NormalizePath(value)
		if normalized == "" {
			return nil, fileSelectionInvalid("file_selection contains a blank path")
		}
		if len(normalized) > maxOneShotSelectedPathBytes {
			return nil, fileSelectionInvalid("file_selection contains a path longer than %d bytes", maxOneShotSelectedPathBytes)
		}
		if _, dup := seen[normalized]; dup {
			return nil, fileSelectionInvalid("file_selection names %q twice", normalized)
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	sort.Strings(out)
	return out, nil
}

// resolveOneShotFileSelection proves a canonical manual selection against the
// seed's own manifest and the captured file rules, and returns it as the
// selection the plan freezes. Every selected path must be a file of this
// torrent, of the video or subtitle kind; a selected video must pass the file
// rules; at least one video is selected; and every selected subtitle binds,
// by the one same-directory strict-prefix rule, to a selected video. A
// selection the product would not place fails closed with
// FILE_SELECTION_INVALID. nil is the product default.
func resolveOneShotFileSelection(paths []string, files []acquisitiondomain.ReleaseFile, rules animedomain.FileFilterRules) (*filekinds.Selection, error) {
	if paths == nil {
		return nil, nil
	}
	manifest := make(map[string]struct{}, len(files))
	for _, file := range files {
		sourcePath := filekinds.NormalizePath(file.SourcePath)
		if sourcePath == "" {
			sourcePath = filekinds.NormalizePath(file.Filename)
		}
		manifest[sourcePath] = struct{}{}
	}
	selection := filekinds.NewSelection(paths)
	videos := []string{}
	subtitles := []string{}
	for _, selected := range selection.Paths() {
		if _, ok := manifest[selected]; !ok {
			return nil, fileSelectionInvalid("%q is not a file of this torrent", selected)
		}
		switch filekinds.ClassifyCurrent(selected) {
		case filekinds.KindVideo:
			if reason := animedomain.ManifestFileExclusionIn(filekinds.Version, rules, selected, selection); reason != "" {
				return nil, fileSelectionInvalid("%q is excluded by the file rules (%s)", selected, reason)
			}
			videos = append(videos, selected)
		case filekinds.KindSubtitle:
			subtitles = append(subtitles, selected)
		default:
			return nil, fileSelectionInvalid("%q is neither a video nor a subtitle", selected)
		}
	}
	if len(videos) == 0 {
		return nil, fileSelectionInvalid("file_selection must include at least one video")
	}
	stems := make([]string, len(videos))
	for index, video := range videos {
		name := path.Base(video)
		stems[index] = strings.TrimSuffix(name, path.Ext(name))
	}
	for _, subtitle := range subtitles {
		if filekinds.BindSubtitleInDirectory(subtitle, videos, stems) < 0 {
			return nil, fileSelectionInvalid("%q follows no selected video in its folder", subtitle)
		}
	}
	return selection, nil
}
