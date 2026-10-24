package application

import (
	"sort"
	"strings"
	"unicode"
)

// Display order of an RSS list. Both orders apply at read time only: the stored
// manifest Ordinal and every snapshot content hash stay as they were written.

// sortRSSListItemsNewestFirst orders seed rows by publish time, newest first.
// A row with no publish time goes last; ties keep the stable seed key order.
func sortRSSListItemsNewestFirst(items []RSSListItem) {
	sort.SliceStable(items, func(i, j int) bool {
		left, right := items[i].Seed.PublishedAt, items[j].Seed.PublishedAt
		switch {
		case left == nil && right == nil:
		case left == nil:
			return false
		case right == nil:
			return true
		case !left.Equal(*right):
			return left.After(*right)
		}
		return items[i].StableSeedKey < items[j].StableSeedKey
	})
}

// sortRSSListFilesNatural orders file rows by natural source path order.
func sortRSSListFilesNatural(files []RSSListFile) {
	sort.SliceStable(files, func(i, j int) bool {
		if c := naturalPathCompare(files[i].SourcePath, files[j].SourcePath); c != 0 {
			return c < 0
		}
		return files[i].Ordinal < files[j].Ordinal
	})
}

// sortRSSListView applies the item order (when withItems) and the file order of
// every row. The owner view keeps its query order of items, which already falls
// back to the discovery time for a release without a publish time.
func sortRSSListView(view *RSSListView, withItems bool) {
	for groupIndex := range view.Groups {
		group := &view.Groups[groupIndex]
		if withItems {
			sortRSSListItemsNewestFirst(group.Items)
		}
		for itemIndex := range group.Items {
			sortRSSListFilesNatural(group.Items[itemIndex].Files)
		}
	}
}

// naturalPathCompare compares two paths component by component, so a directory
// sorts with its own files and a file in a directory never interleaves with a
// sibling. Within a component, digit runs compare by numeric value and the rest
// case-insensitively; exact ties fall back to a byte comparison so the order
// is total and stable.
func naturalPathCompare(a, b string) int {
	left, right := strings.Split(a, "/"), strings.Split(b, "/")
	for index := 0; index < len(left) && index < len(right); index++ {
		// A directory (not the last component) sorts before a file at the same
		// level, like a file manager listing.
		leftDir, rightDir := index < len(left)-1, index < len(right)-1
		if leftDir != rightDir {
			if leftDir {
				return -1
			}
			return 1
		}
		if c := naturalCompare(left[index], right[index]); c != 0 {
			return c
		}
	}
	switch {
	case len(left) < len(right):
		return -1
	case len(left) > len(right):
		return 1
	}
	return strings.Compare(a, b)
}

func naturalCompare(a, b string) int {
	ar, br := []rune(a), []rune(b)
	i, j := 0, 0
	for i < len(ar) && j < len(br) {
		if isASCIIDigit(ar[i]) && isASCIIDigit(br[j]) {
			si := i
			for i < len(ar) && isASCIIDigit(ar[i]) {
				i++
			}
			sj := j
			for j < len(br) && isASCIIDigit(br[j]) {
				j++
			}
			x, y := strings.TrimLeft(string(ar[si:i]), "0"), strings.TrimLeft(string(br[sj:j]), "0")
			if len(x) != len(y) {
				if len(x) < len(y) {
					return -1
				}
				return 1
			}
			if c := strings.Compare(x, y); c != 0 {
				return c
			}
			continue
		}
		x, y := unicode.ToLower(ar[i]), unicode.ToLower(br[j])
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
		i++
		j++
	}
	switch {
	case len(ar)-i < len(br)-j:
		return -1
	case len(ar)-i > len(br)-j:
		return 1
	}
	return 0
}

func isASCIIDigit(r rune) bool { return r >= '0' && r <= '9' }
