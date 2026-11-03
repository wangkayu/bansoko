package filemanager

import (
	"context"
	"io/fs"
	"os"
	"unicode/utf8"
)

// maxSubtreeManagedFiles bounds the entries one managed-only scan reads below
// a directory. Beyond it the flag stays absent and the folder stays visible:
// a browse must not walk an unbounded subtree to answer a display filter.
const maxSubtreeManagedFiles = 10000

// scanSubtree collects the paths of every non-directory entry below directory,
// at any depth, each with the spellings owner rows may use for it. ok is false
// when the answer cannot be decided: a directory below cannot be read, or the
// subtree holds more entries than limit.
//
// The scan never follows a symbolic link. A link is one more leaf, judged by
// the facts of its own path, never descended into — the same rule a browse
// applies. A regular leaf carries a single spelling: every component above it
// was reached through real directories, so no second name for it exists; a
// link may resolve elsewhere, so it carries its target spelling too.
func scanSubtree(directory string, limit int) ([][]string, bool) {
	var leaves [][]string
	directories := []string{directory}
	seen := 0
	for len(directories) > 0 {
		current := directories[len(directories)-1]
		directories = directories[:len(directories)-1]
		dirents, err := os.ReadDir(current)
		if err != nil {
			return nil, false
		}
		for _, dirent := range dirents {
			name := dirent.Name()
			if !utf8.ValidString(name) {
				// A name that cannot round-trip through a path is not
				// browsable; a browse skips it, so the scan does too.
				continue
			}
			seen++
			if seen > limit {
				return nil, false
			}
			path := joinAbsolute(current, name)
			if dirent.IsDir() {
				directories = append(directories, path)
				continue
			}
			if dirent.Type()&fs.ModeSymlink != 0 {
				leaves = append(leaves, absoluteSpellings(path))
			} else {
				leaves = append(leaves, []string{path})
			}
		}
	}
	return leaves, true
}

// markManagedOnly sets ManagedOnly on every directory entry of a page. The
// facts of all scanned leaves are read in one snapshot, and each leaf is
// judged exactly as its own row would be, so the flag never disagrees with
// what a browse of the folder shows.
func (s *Service) markManagedOnly(ctx context.Context, entries []Entry) error {
	type scan struct {
		index  int
		leaves [][]string
	}
	var scans []scan
	var paths []string
	for index := range entries {
		if entries[index].Kind != KindDirectory {
			continue
		}
		leaves, ok := scanSubtree(entries[index].Path, s.subtreeScanLimit)
		if !ok || len(leaves) == 0 {
			// Undecidable, or nothing below at all: the folder stays visible.
			continue
		}
		scans = append(scans, scan{index: index, leaves: leaves})
		for _, leaf := range leaves {
			paths = append(paths, leaf...)
		}
	}
	if len(scans) == 0 {
		return nil
	}
	facts, _, err := s.fileFacts(ctx, paths)
	if err != nil {
		return err
	}
	for _, current := range scans {
		managedOnly := true
		for _, leaf := range current.leaves {
			derived := deriveFile(facts, leaf, nil)
			if derived.state != StateManaged && derived.state != StateInProgress {
				managedOnly = false
				break
			}
		}
		entries[current.index].ManagedOnly = &managedOnly
	}
	return nil
}
