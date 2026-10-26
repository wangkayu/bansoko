package domain

// TitleTagSet is the title-parse tag set of one release: the non-episode
// canonical attributes the pinned reltitle parser read from the release title,
// written "type:value" (e.g. "resolution:1080p", "sub_lang:SC"). It is frozen
// into Release.source_facts together with the parser implementation version and
// the digest of its embedded dictionary bundle, so a later evaluation reads the
// same tags the release was observed with.
type TitleTagSet struct {
	// Schema identifies the tag projection contract.
	Schema string
	// Implementation is the pinned parser implementation ("reltitle@<module
	// version>", tracked by reltitletags.ImplementationVersion).
	Implementation string
	// ConfigDigest is "lexicon:sha256:<hex>" over the complete embedded bundle.
	ConfigDigest string
	// Tags are "type:value", de-duplicated, sorted.
	Tags []string
	// ValueTags are reltitle values frozen for the template value namespace
	// only ("title:<v>", "group:<v>", "version:<v>"). They are never filter
	// tags and never episode identity.
	ValueTags []string
	// Diagnostic explains an empty set (parser unavailable or failed); empty
	// when the parse succeeded, even if it found no tag.
	Diagnostic string
}

// TitleTagSchema is the title tag projection schema. Only a set that carries
// it contributes ValueTags.
const TitleTagSchema = "reltitle-attrs-v1"

// Title tag value dimensions (ValueTags).
const (
	TitleValueDimensionTitle   = "title"
	TitleValueDimensionGroup   = "group"
	TitleValueDimensionVersion = "version"
)

// Title tag diagnostics. None of them fails the release; an empty set fails a
// non-empty whitelist like any tag-less release.
const (
	TitleTagDiagnosticParserUnavailable = "reltitle_unavailable"
	TitleTagDiagnosticParseFailed       = "reltitle_parse_failed"
)
