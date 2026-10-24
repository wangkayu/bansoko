package episodeidentity

import (
	"encoding/json"
	"strings"
	"testing"
)

func resolver(t *testing.T) *Resolver {
	t.Helper()
	return NewResolver()
}

func valueEvidence(source EpisodeEvidenceSource, inputKind, raw string) EpisodeEvidence {
	return NewValueEvidence(source, inputKind, raw)
}

func missingEvidence(source EpisodeEvidenceSource) EpisodeEvidence {
	return NewMissingEvidence(source, "file_name")
}

// tvInput builds a resolver input with defaults: tv media, no offsets, no
// AniBT evidence. The evidence is the one subject's own: a filename (or the
// release title for the release-level identity).
func tvInput(evidence EpisodeEvidence) EpisodeIdentityInput {
	return EpisodeIdentityInput{MediaKind: "tv", FileEvidence: evidence}
}

func TestResolveMovieAlwaysZero(t *testing.T) {
	r := resolver(t)
	for _, offset := range []int{-5, 0, 3} {
		input := EpisodeIdentityInput{
			MediaKind:    "movie",
			FileEvidence: valueEvidence(EpisodeSourceFileParser, "file_name", "5"),
			Offset:       offset,
		}
		identity := r.Resolve(input)
		if identity.State != StateResolved || identity.Kind != KindMovie {
			t.Fatalf("offset=%d: state=%s kind=%s, want resolved/movie", offset, identity.State, identity.Kind)
		}
		if identity.RawEpisode == nil || *identity.RawEpisode != "0" ||
			identity.Offset != 0 || identity.Episode == nil || *identity.Episode != "0" {
			t.Fatalf("offset=%d: movie identity not 0/0/0: raw=%v offset=%d ep=%v",
				offset, identity.RawEpisode, identity.Offset, identity.Episode)
		}
		if identity.CoverageKey == nil || *identity.CoverageKey != "movie:0" {
			t.Fatalf("offset=%d: coverage=%v, want movie:0", offset, identity.CoverageKey)
		}
		if identity.SelectedSource == nil || *identity.SelectedSource != EpisodeSourceMovie {
			t.Fatalf("offset=%d: source=%v, want movie", offset, identity.SelectedSource)
		}
	}
}

func TestResolveFilenameEpisode(t *testing.T) {
	r := resolver(t)
	identity := r.Resolve(tvInput(valueEvidence(EpisodeSourceFileParser, "file_name", "7")))
	if identity.State != StateResolved || identity.Episode == nil || *identity.Episode != "7" {
		t.Fatalf("identity=%+v, want resolved episode 7", identity)
	}
	if identity.SelectedSource == nil || *identity.SelectedSource != EpisodeSourceFileParser {
		t.Fatalf("source=%v, want file_parser", identity.SelectedSource)
	}
}

// A filename without an episode is unresolved. There is no inheritance from
// the release title, even for a single-video torrent.
func TestResolveMissingFilenameEpisodeIsUnresolved(t *testing.T) {
	r := resolver(t)
	identity := r.Resolve(tvInput(missingEvidence(EpisodeSourceFileParser)))
	if identity.State != StateUnresolved {
		t.Fatalf("state=%s, want unresolved", identity.State)
	}
	if identity.FailureCode == nil || *identity.FailureCode != FailEpisodeMissing {
		t.Fatalf("failure=%v, want episode_missing", identity.FailureCode)
	}
	if identity.CoverageKey != nil || identity.Episode != nil {
		t.Fatalf("coverage=%v episode=%v: nothing may be borrowed for a file without an episode", identity.CoverageKey, identity.Episode)
	}
}

func TestResolveFileInvalidIsKept(t *testing.T) {
	r := resolver(t)
	for _, raw := range []string{"1a", "abc"} {
		identity := r.Resolve(tvInput(valueEvidence(EpisodeSourceFileParser, "file_name", raw)))
		if identity.State != StateInvalid || identity.FailureCode == nil || *identity.FailureCode != FailEpisodeInvalid {
			t.Fatalf("%q: state=%s failure=%v, want invalid/episode_invalid", raw, identity.State, identity.FailureCode)
		}
	}
}

func TestResolveUnsupportedShape(t *testing.T) {
	r := resolver(t)
	cases := []struct {
		name      string
		shape     string
		wantState string
		wantCode  string
	}{
		{"range", ShapeRange, StateUnsupported, FailEpisodeShapeUnsupported},
		{"list", ShapeList, StateUnsupported, FailEpisodeShapeUnsupported},
		{"season only", ShapeSeasonOnly, StateUnsupported, FailEpisodeShapeUnsupported},
		{"special", ShapeSpecial, StateUnsupported, FailSpecialUnsupported},
		{"unknown", ShapeUnknown, StateUnsupported, FailEpisodeShapeUnsupported},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			evidence := NewUnsupportedEvidence(EpisodeSourceFileParser, "file_name", tc.shape, "", "fixture")
			identity := r.Resolve(tvInput(evidence))
			if identity.State != tc.wantState {
				t.Fatalf("state=%s, want %s", identity.State, tc.wantState)
			}
			if identity.FailureCode == nil || *identity.FailureCode != tc.wantCode {
				t.Fatalf("failure=%v, want %s", identity.FailureCode, tc.wantCode)
			}
			if identity.CoverageKey != nil {
				t.Fatalf("coverage=%v, unsupported must never create coverage", *identity.CoverageKey)
			}
		})
	}
}

func TestResolveOffsetAppliedExactlyOnce(t *testing.T) {
	r := resolver(t)
	base := tvInput(valueEvidence(EpisodeSourceFileParser, "file_name", "12"))
	base.Offset = 3
	identity := r.Resolve(base)
	if identity.Episode == nil || *identity.Episode != "15" {
		t.Fatalf("episode=%v, want 15 (offset once)", identity.Episode)
	}
	if identity.RawEpisode == nil || *identity.RawEpisode != "12" {
		t.Fatalf("raw=%v, want 12 untouched", identity.RawEpisode)
	}
	if identity.Offset != 3 {
		t.Fatalf("offset=%d, want 3", identity.Offset)
	}
	if identity.CoverageKey == nil || *identity.CoverageKey != "tv:s1:e15" {
		t.Fatalf("coverage=%v, want tv:s1:e15", identity.CoverageKey)
	}
}

// A fraction anywhere in the selected episode (single, range or list) is not
// an episode identity: unresolved episode_fractional, no offset, no coverage,
// whatever the source (file name or release title).
func TestResolveFractionalEpisodeIsUnresolved(t *testing.T) {
	r := resolver(t)
	cases := []struct {
		name     string
		evidence EpisodeEvidence
	}{
		{"single file", valueEvidence(EpisodeSourceFileParser, "file_name", "12.5")},
		{"single title", valueEvidence(EpisodeSourceReleaseParser, "release_title", "12.5")},
		{"range", NewUnsupportedEvidence(EpisodeSourceFileParser, "file_name", ShapeRange, "12-12.5", "")},
		{"list", NewUnsupportedEvidence(EpisodeSourceFileParser, "file_name", ShapeList, "[12][12.5]", "")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := tvInput(tc.evidence)
			input.Offset = 2
			identity := r.Resolve(input)
			if identity.State != StateUnresolved || identity.FailureCode == nil || *identity.FailureCode != FailEpisodeFractional {
				t.Fatalf("state=%s failure=%v, want unresolved/episode_fractional", identity.State, identity.FailureCode)
			}
			if identity.CoverageKey != nil || identity.Episode != nil || identity.Offset != 0 {
				t.Fatalf("coverage=%v episode=%v offset=%d: a fraction gets no offset and no coverage", identity.CoverageKey, identity.Episode, identity.Offset)
			}
		})
	}
	// An integer range wider than one file stays an unsupported shape.
	identity := r.Resolve(tvInput(NewUnsupportedEvidence(EpisodeSourceFileParser, "file_name", ShapeRange, "12-24", "")))
	if identity.FailureCode == nil || *identity.FailureCode != FailEpisodeShapeUnsupported {
		t.Fatalf("integer range failure=%v, want episode_shape_unsupported", identity.FailureCode)
	}
}

// A filename range or list of 2..4 consecutive episodes is one file holding
// them: the identity is the first episode and occupies every member's key.
func TestResolveMultiEpisodeFile(t *testing.T) {
	r := resolver(t)
	cases := []struct {
		name, shape, capture string
		offset               int
		episode, end         string
		keys                 []string
	}{
		{"range", ShapeRange, "01-02", 0, "1", "2", []string{"tv:s1:e1", "tv:s1:e2"}},
		{"chinese range", ShapeRange, "第01-03话", 0, "1", "3", []string{"tv:s1:e1", "tv:s1:e2", "tv:s1:e3"}},
		{"E range", ShapeRange, "E11-E14", 0, "11", "14", []string{"tv:s1:e11", "tv:s1:e12", "tv:s1:e13", "tv:s1:e14"}},
		{"tilde range offset", ShapeRange, "13~14", -12, "1", "2", []string{"tv:s1:e1", "tv:s1:e2"}},
		{"S E list", ShapeList, "S01E05E06", 0, "5", "6", []string{"tv:s1:e5", "tv:s1:e6"}},
		{"bracket list", ShapeList, "[07][08]", 0, "7", "8", []string{"tv:s1:e7", "tv:s1:e8"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := tvInput(NewUnsupportedEvidence(EpisodeSourceFileParser, "file_name", tc.shape, tc.capture, ""))
			input.Offset = tc.offset
			identity := r.Resolve(input)
			if identity.State != StateResolved || identity.Shape != tc.shape {
				t.Fatalf("state=%s shape=%s failure=%v, want resolved %s", identity.State, identity.Shape, identity.FailureCode, tc.shape)
			}
			if identity.Episode == nil || string(*identity.Episode) != tc.episode || identity.EpisodeEnd == nil || string(*identity.EpisodeEnd) != tc.end {
				t.Fatalf("episode=%v end=%v, want %s-%s", identity.Episode, identity.EpisodeEnd, tc.episode, tc.end)
			}
			if identity.CoverageKey == nil || *identity.CoverageKey != tc.keys[0] || !identity.MultiEpisode() {
				t.Fatalf("coverage key=%v multi=%v, want first key %s", identity.CoverageKey, identity.MultiEpisode(), tc.keys[0])
			}
			if got := identity.Keys(); strings.Join(got, ",") != strings.Join(tc.keys, ",") {
				t.Fatalf("keys=%v, want %v", got, tc.keys)
			}
			if identity.Offset != tc.offset {
				t.Fatalf("offset=%d, want %d (applied once)", identity.Offset, tc.offset)
			}
		})
	}
}

// Only 2..4 consecutive ascending filename members form a multi-episode file;
// everything else keeps the unsupported shape, and release-title ranges never
// resolve.
func TestResolveMultiEpisodeBounds(t *testing.T) {
	r := resolver(t)
	cases := []struct {
		name     string
		evidence EpisodeEvidence
	}{
		{"five episodes", NewUnsupportedEvidence(EpisodeSourceFileParser, "file_name", ShapeRange, "01-05", "")},
		{"descending", NewUnsupportedEvidence(EpisodeSourceFileParser, "file_name", ShapeRange, "05-04", "")},
		{"same", NewUnsupportedEvidence(EpisodeSourceFileParser, "file_name", ShapeRange, "05-05", "")},
		{"list gap", NewUnsupportedEvidence(EpisodeSourceFileParser, "file_name", ShapeList, "[01][03]", "")},
		{"list repeat", NewUnsupportedEvidence(EpisodeSourceFileParser, "file_name", ShapeList, "S01E02E02", "")},
		{"list too long", NewUnsupportedEvidence(EpisodeSourceFileParser, "file_name", ShapeList, "[01][02][03][04][05]", "")},
		{"release title range", NewUnsupportedEvidence(EpisodeSourceReleaseParser, "release_title", ShapeRange, "01-02", "")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			identity := r.Resolve(tvInput(tc.evidence))
			if identity.State != StateUnsupported || identity.FailureCode == nil || *identity.FailureCode != FailEpisodeShapeUnsupported {
				t.Fatalf("state=%s failure=%v, want unsupported/episode_shape_unsupported", identity.State, identity.FailureCode)
			}
			if len(identity.Keys()) != 0 || identity.EpisodeEnd != nil {
				t.Fatalf("keys=%v end=%v, want none", identity.Keys(), identity.EpisodeEnd)
			}
		})
	}
	// Four members is the bound.
	identity := r.Resolve(tvInput(NewUnsupportedEvidence(EpisodeSourceFileParser, "file_name", ShapeList, "[01][02][03][04]", "")))
	if len(identity.Keys()) != MaxEpisodesPerFile {
		t.Fatalf("keys=%v, want %d", identity.Keys(), MaxEpisodesPerFile)
	}
	// A fractional member is refused before the shape is judged.
	identity = r.Resolve(tvInput(NewUnsupportedEvidence(EpisodeSourceFileParser, "file_name", ShapeRange, "01-01.5", "")))
	if identity.FailureCode == nil || *identity.FailureCode != FailEpisodeFractional {
		t.Fatalf("failure=%v, want episode_fractional", identity.FailureCode)
	}
}

// A single episode's frozen JSON carries none of the multi-episode fields.
func TestResolveSingleIdentityJSONUnchanged(t *testing.T) {
	r := resolver(t)
	identity := r.Resolve(tvInput(valueEvidence(EpisodeSourceFileParser, "file_name", "3")))
	raw, err := json.Marshal(identity)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"EpisodeEnd", "RawEpisodeEnd", "CoverageKeys"} {
		if strings.Contains(string(raw), field) {
			t.Fatalf("single identity JSON %s carries %s", raw, field)
		}
	}
}

// The resolver takes exactly the one offset its caller chose; a different
// offset is a different input and so a different digest.
func TestResolveUsesTheOneOffsetItIsGiven(t *testing.T) {
	r := resolver(t)
	base := tvInput(valueEvidence(EpisodeSourceFileParser, "file_name", "12"))
	zero := r.Resolve(base)
	if zero.Episode == nil || *zero.Episode != "12" || zero.Offset != 0 {
		t.Fatalf("zero offset: episode=%v offset=%d, want 12/0", zero.Episode, zero.Offset)
	}
	base.Offset = -2
	shifted := r.Resolve(base)
	if shifted.Episode == nil || *shifted.Episode != "10" || shifted.Offset != -2 {
		t.Fatalf("offset -2: episode=%v offset=%d, want 10/-2", shifted.Episode, shifted.Offset)
	}
	if shifted.InputDigest == zero.InputDigest {
		t.Fatal("a different offset must produce a different input digest")
	}
	if zero.ResolverVersion != "episode-identity-v1" {
		t.Fatalf("resolver version=%q, want episode-identity-v1", zero.ResolverVersion)
	}
}

// Coverage uses the bound season; a season is never read from evidence.
func TestResolveSeasonDefaultsToOne(t *testing.T) {
	r := resolver(t)
	base := tvInput(valueEvidence(EpisodeSourceFileParser, "file_name", "3"))
	identity := r.Resolve(base)
	if identity.CoverageKey == nil || *identity.CoverageKey != "tv:s1:e3" {
		t.Fatalf("coverage=%v, want tv:s1:e3", identity.CoverageKey)
	}
	base.Season = 4
	identity = r.Resolve(base)
	if identity.CoverageKey == nil || *identity.CoverageKey != "tv:s4:e3" {
		t.Fatalf("coverage=%v, want tv:s4:e3", identity.CoverageKey)
	}
}

func TestResolveAniBTStructuredEvidenceIsIgnored(t *testing.T) {
	r := resolver(t)
	anibt := valueEvidence(EpisodeSourceAniBT, "provider", "99")
	// A structured AniBT episode is not evidence about the physical file and
	// therefore cannot fill a missing parser result, whatever the mode.
	for _, mode := range []string{"disabled", "fill_missing", "override"} {
		base := tvInput(missingEvidence(EpisodeSourceFileParser))
		base.AniBTEvidence = &anibt
		base.AniBTMode = mode
		identity := r.Resolve(base)
		if identity.Episode != nil || identity.State != StateUnresolved {
			t.Fatalf("mode %s: identity=%+v, want unresolved without file evidence", mode, identity)
		}
	}
	// Parser evidence wins and the provider value cannot replace it.
	base := tvInput(valueEvidence(EpisodeSourceFileParser, "file_name", "5"))
	base.AniBTEvidence = &anibt
	base.AniBTMode = "override"
	if identity := r.Resolve(base); identity.Episode == nil || *identity.Episode != "5" {
		t.Fatalf("episode=%v, want 5 (provider must not override parser evidence)", identity.Episode)
	}
}

func TestResolveDeterministicDigest(t *testing.T) {
	r := resolver(t)
	a := tvInput(valueEvidence(EpisodeSourceFileParser, "file_name", "7"))
	b := tvInput(valueEvidence(EpisodeSourceFileParser, "file_name", "7"))
	if r.Resolve(a).InputDigest != r.Resolve(b).InputDigest {
		t.Fatal("identical inputs must produce identical digests")
	}
	b.FileEvidence = valueEvidence(EpisodeSourceFileParser, "file_name", "8")
	if r.Resolve(a).InputDigest == r.Resolve(b).InputDigest {
		t.Fatal("different inputs must produce different digests")
	}
}

// The release-level identity resolves the release title's own evidence.
func TestResolveReleaseTitleEvidence(t *testing.T) {
	r := resolver(t)
	identity := r.Resolve(tvInput(valueEvidence(EpisodeSourceReleaseParser, "release_title", "2")))
	if identity.State != StateResolved || identity.CoverageKey == nil || *identity.CoverageKey != "tv:s1:e2" {
		t.Fatalf("identity=%+v, want resolved tv:s1:e2", identity)
	}
	if identity.SelectedSource == nil || *identity.SelectedSource != EpisodeSourceReleaseParser {
		t.Fatalf("source=%v, want release_parser", identity.SelectedSource)
	}
}
