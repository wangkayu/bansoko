package filemanager

import (
	"context"
	"sort"
	"strings"
)

// RootPicker lists one page of the subdirectories of an absolute directory so
// the owner can walk to the folder to authorize. It reads outside the library
// roots on purpose: the picker exists to choose the first one or the next one.
// The refusal set still applies, a symbolic link is never followed, and only
// directory names are read — no file, no ownership derivation and no scan.
func (s *Service) RootPicker(ctx context.Context, absolutePath, cursor string, limit int) (RootPickerListing, error) {
	cleaned, ok := cleanAbsolute(strings.TrimSpace(absolutePath))
	if !ok {
		return RootPickerListing{}, invalid(CodePathInvalid, "path must be an absolute path")
	}
	// Refuse the refusal set before touching the filesystem, so a refused
	// path is never even probed for existence.
	if err := refuseDenied(s.zone, cleaned); err != nil {
		return RootPickerListing{}, err
	}
	directory, walked, err := walkAbsolute(cleaned, false)
	if err != nil {
		return RootPickerListing{}, err
	}
	if !walked.IsDir() {
		return RootPickerListing{}, invalid(CodeNotADirectory, "path is not a directory: "+directory)
	}
	raw, truncated, err := readDirectory(directory, walked)
	if err != nil {
		return RootPickerListing{}, err
	}
	folders := make([]rawEntry, 0, len(raw))
	for _, entry := range raw {
		if entry.kind != KindDirectory {
			continue
		}
		// A refused subdirectory is not offered, so the tree never leads into
		// the refusal set; entering one directly is refused above.
		if s.zone.Denies(joinAbsolute(directory, entry.name)) {
			continue
		}
		folders = append(folders, entry)
	}
	sort.Slice(folders, func(i, j int) bool { return folders[i].name < folders[j].name })
	if limit <= 0 {
		limit = defaultPageLimit
	}
	if limit > maxPageLimit {
		limit = maxPageLimit
	}
	scope := cursorScope("root-picker:" + directory)
	start := 0
	if cursor != "" {
		key, err := decodeCursor(cursor, scope)
		if err != nil {
			return RootPickerListing{}, err
		}
		start = sort.Search(len(folders), func(index int) bool { return afterCursor(folders[index], key) })
	}
	end := start + limit
	if end > len(folders) {
		end = len(folders)
	}
	page := folders[start:end]
	listing := RootPickerListing{Path: directory, Truncated: truncated, Entries: make([]RootPickerEntry, 0, len(page))}
	if end < len(folders) {
		last := page[len(page)-1]
		listing.NextCursor = encodeCursor(cursorKey{Directory: true, Name: last.name, Scope: scope})
	}
	for _, entry := range page {
		listing.Entries = append(listing.Entries, RootPickerEntry{Name: entry.name, Path: joinAbsolute(directory, entry.name)})
	}
	return listing, nil
}
