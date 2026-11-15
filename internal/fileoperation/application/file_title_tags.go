package application

import (
	"path/filepath"
	"strings"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	"bansoko/internal/postprocessing/naming"
)

// fileTitleTagCache holds this prepare run's one reltitle parse of each
// artifact file's original name. The naming render and every frozen snapshot
// writer read the same set, so a later parser or file rename never changes
// what the operation already froze. It is invocation-local: nothing persists
// the cache, only the frozen block it produced.
type fileTitleTagCache struct {
	byFileID map[string]*acquisitiondomain.TitleTagSet
}

func newFileTitleTagCache() *fileTitleTagCache {
	return &fileTitleTagCache{byFileID: map[string]*acquisitiondomain.TitleTagSet{}}
}

// titleTags returns the cached set, parsing on first use. A nil cache (a
// caller outside prepare) still parses correctly, just without sharing.
func (c *fileTitleTagCache) titleTags(file artifactFileContext) *acquisitiondomain.TitleTagSet {
	if c == nil {
		return naming.ParseFileTitleTags(fileNamingFacts(file))
	}
	if set, ok := c.byFileID[file.id]; ok {
		return set
	}
	set := naming.ParseFileTitleTags(fileNamingFacts(file))
	c.byFileID[file.id] = set
	return set
}

// fileTitleTags is the file's frozen frt parse for this prepare run.
func (prep prepareContext) fileTitleTags(file artifactFileContext) *acquisitiondomain.TitleTagSet {
	return prep.titleTagCache.titleTags(file)
}

// fileTitleTagsBlock is the frozen snapshot member every file operation
// snapshot carries (frozen_render_snapshot.file_title_tags): the complete tag
// set with its schema, implementation, config digest and diagnostic. A nil
// set freezes null, which every reader treats as no value.
func fileTitleTagsBlock(set *acquisitiondomain.TitleTagSet) any {
	if set == nil {
		return nil
	}
	return acquisitiondomain.StoredTitleTagsFromSet(*set)
}

// fileNamingFacts is the naming-visible file identity. The frt parse and the
// naming render read this same stem.
func fileNamingFacts(file artifactFileContext) naming.FileNamingFacts {
	base := filepath.Base(file.sourcePath)
	return naming.FileNamingFacts{
		OriginalName: base,
		Basename:     strings.TrimSuffix(base, filepath.Ext(base)),
		Extension:    strings.TrimPrefix(strings.ToLower(filepath.Ext(base)), "."),
		SizeBytes:    file.sizeBytes,
		Kind:         file.mediaKind,
	}
}
