package application

import (
	"testing"

	acquisitiondomain "bansoko/internal/acquisition/domain"
)

// One prepare run parses each original file name once. The naming render and
// every snapshot writer read the same set; a later rename or parser change
// never alters the frozen block.
func TestFileTitleTagCacheParsesEachFileOnce(t *testing.T) {
	cache := newFileTitleTagCache()
	file := artifactFileContext{
		id:         "file-1",
		sourcePath: "/downloads/[文件组] Show - 01 [1080p].mkv",
		mediaKind:  "video",
		sizeBytes:  1,
	}
	first := cache.titleTags(file)
	if first == nil || first.Schema != acquisitiondomain.TitleTagSchema || first.Implementation == "" {
		t.Fatalf("parsed set = %#v, want the frozen reltitle shape", first)
	}
	if second := cache.titleTags(file); second != first {
		t.Fatal("the second read parsed again instead of reusing the set")
	}
	// A changed path is not a new parse for the same artifact file.
	file.sourcePath = "/library/Show/Show S01E01.mkv"
	if third := cache.titleTags(file); third != first {
		t.Fatal("a changed file name replaced the frozen set")
	}
	prep := prepareContext{titleTagCache: cache}
	if got := prep.fileTitleTags(file); got != first {
		t.Fatal("prepare does not read the shared set")
	}
	// A bound subtitle is its own file: its name is parsed once too, and the
	// video's frozen set is untouched.
	subtitle := artifactFileContext{
		id:         "file-2",
		sourcePath: "/downloads/[文件组] Show - 01 [1080p].chs.ass",
		mediaKind:  "subtitle",
	}
	sub := prep.fileTitleTags(subtitle)
	if sub == nil || sub == first {
		t.Fatalf("subtitle set = %#v, want its own parse", sub)
	}
	if again := prep.fileTitleTags(subtitle); again != sub {
		t.Fatal("the subtitle was parsed twice")
	}
	if got := prep.fileTitleTags(file); got != first {
		t.Fatal("the subtitle replaced the video's frozen set")
	}

	block := fileTitleTagsBlock(first)
	stored, ok := block.(*acquisitiondomain.StoredTitleTags)
	if !ok || stored == nil || stored.Schema != acquisitiondomain.TitleTagSchema {
		t.Fatalf("frozen block = %#v, want the complete stored tag set", block)
	}
	if fileTitleTagsBlock(nil) != nil {
		t.Fatal("a nameless file freezes null")
	}
}
