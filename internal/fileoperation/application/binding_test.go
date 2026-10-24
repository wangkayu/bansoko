package application

import (
	"testing"
)

// video builds the binder facts of a video from its filename.
func video(ref, filename string) bindingFileFacts {
	return fileBindingFacts(artifactFileContext{fileRef: ref, sourcePath: "/downloads/Show/" + filename})
}

func subtitle(ref, filename string) bindingFileFacts {
	return video(ref, filename)
}

func TestDecideSubtitleBindingPrefixMatch(t *testing.T) {
	videos := []bindingFileFacts{video("video-1", "Show - 01.mkv"), video("video-2", "Show - 02.mkv")}
	for _, name := range []string{"Show - 01.ass", "Show - 01.forced.ass", "Show - 01.zh-hans.ass", "Show - 01.chs&cht.ass"} {
		decision := decideSubtitleBinding(subtitle("sub", name), videos)
		if decision.state != bindingStateBound || decision.boundRef != "video-1" {
			t.Fatalf("%s: unexpected decision: %+v", name, decision)
		}
		if decision.method != bindingMethodBasenameMatch || decision.confidence != 1.0 {
			t.Fatalf("%s: unexpected method/confidence: %+v", name, decision)
		}
	}
}

// The prefix must be followed by a dot: `abcde.ass` is not abcd's subtitle.
func TestDecideSubtitleBindingNeedsTheDotAfterThePrefix(t *testing.T) {
	videos := []bindingFileFacts{video("video-1", "abcd.mp4")}
	for _, name := range []string{"abcde.ass", "abcd-forced.ass", "abc.ass", "ABCD.ass"} {
		if decision := decideSubtitleBinding(subtitle("sub", name), videos); decision.state != bindingStateMissing {
			t.Fatalf("%s must not bind abcd.mp4: %+v", name, decision)
		}
	}
	if decision := decideSubtitleBinding(subtitle("sub", "abcd.chs&cht.ass"), videos); decision.state != bindingStateBound || decision.boundRef != "video-1" {
		t.Fatalf("multi-part marker must bind: %+v", decision)
	}
}

// Several matching videos resolve to the longest video name.
func TestDecideSubtitleBindingLongestPrefixWins(t *testing.T) {
	videos := []bindingFileFacts{video("short", "Show.mkv"), video("long", "Show.S01E02.mkv"), video("other", "Show.S01E03.mkv")}
	decision := decideSubtitleBinding(subtitle("sub", "Show.S01E02.chs.ass"), videos)
	if decision.state != bindingStateBound || decision.boundRef != "long" {
		t.Fatalf("longest prefix must win: %+v", decision)
	}
	// A subtitle only the shorter name prefixes binds that one.
	decision = decideSubtitleBinding(subtitle("sub", "Show.chs.ass"), videos)
	if decision.state != bindingStateBound || decision.boundRef != "short" {
		t.Fatalf("the only prefix must bind: %+v", decision)
	}
}

// Same stem, different extension: the remaining tie goes to the first video in
// manifest path order, never to runtime arrival.
func TestDecideSubtitleBindingTieFollowsManifestPathOrder(t *testing.T) {
	sub := subtitle("sub", "abcd.chs.ass")
	ordered := []bindingFileFacts{video("mkv", "abcd.mkv"), video("mp4", "abcd.mp4")}
	if decision := decideSubtitleBinding(sub, ordered); decision.state != bindingStateBound || decision.boundRef != "mkv" {
		t.Fatalf("first in path order must win: %+v", decision)
	}
	reversed := []bindingFileFacts{video("mp4", "abcd.mp4"), video("mkv", "abcd.mkv")}
	if decision := decideSubtitleBinding(sub, reversed); decision.boundRef != "mp4" {
		t.Fatalf("the result follows the order the caller supplies: %+v", decision)
	}
}

// The episode and unique-video rules are gone: a subtitle with another
// name stays unbound even when it shares the episode or is the only one.
func TestDecideSubtitleBindingHasNoEpisodeOrUniqueVideoRule(t *testing.T) {
	videos := []bindingFileFacts{video("video-1", "Show - 01.mkv")}
	for _, name := range []string{"E01.ass", "notes.ass", "Show - 01 (2).ass", "01.ass"} {
		decision := decideSubtitleBinding(subtitle("sub", name), videos)
		if decision.state != bindingStateMissing {
			t.Fatalf("%s must stay unbound: %+v", name, decision)
		}
		if len(decision.diagnostics) != 1 || decision.diagnostics[0].Code != "subtitle_binding_missing" {
			t.Fatalf("%s: diagnostics = %+v", name, decision.diagnostics)
		}
	}
}

func TestDecideSubtitleBindingMissingWithoutVideos(t *testing.T) {
	decision := decideSubtitleBinding(subtitle("sub-1", "Show - 01.chs.ass"), nil)
	if decision.state != bindingStateMissing {
		t.Fatalf("state = %q, want missing", decision.state)
	}
}

func TestDecideSubtitleBindingNeverCrossBinds(t *testing.T) {
	// A multi-episode folder: every subtitle binds its own video.
	videos := []bindingFileFacts{video("video-1", "Show - 01.mkv"), video("video-2", "Show - 02.mkv"), video("video-3", "Show - 03.mkv")}
	decision := decideSubtitleBinding(subtitle("sub-2", "Show - 02.chs.ass"), videos)
	if decision.state != bindingStateBound || decision.boundRef != "video-2" {
		t.Fatalf("unexpected decision: %+v", decision)
	}
}

func TestFileBindingFacts(t *testing.T) {
	facts := fileBindingFacts(artifactFileContext{
		fileRef:      "file-1",
		sourcePath:   "/downloads/Show/Show - 02.mkv",
		relativePath: "Show/Show - 02.mkv",
	})
	if facts.filename != "Show - 02.mkv" || facts.basename != "Show - 02" || facts.ref != "file-1" {
		t.Fatalf("unexpected facts: %+v", facts)
	}
}

func TestBindingSnapshotRoundTrip(t *testing.T) {
	decision := bindingDecision{
		state:      bindingStateBound,
		boundRef:   "primary",
		method:     bindingMethodBasenameMatch,
		confidence: 1.0,
	}
	raw, err := marshalBindingSnapshot(decision, nil)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := decodeBindingSnapshot(raw)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Method != bindingMethodBasenameMatch || snapshot.Confidence != 1.0 || snapshot.Locked != nil {
		t.Fatalf("unexpected snapshot: %+v", snapshot)
	}
	if snapshot.Diagnostics == nil {
		t.Fatal("diagnostics must serialize as an empty array")
	}
	snapshot.Locked = &bindingLockedTarget{
		VideoFileRef:     "primary",
		VideoOperationID: "op-1",
		Directory:        "Show/Season 01",
		Basename:         "Show - E01",
		Extension:        "mkv",
	}
	locked, err := marshalBindingSnapshot(decision, snapshot.Locked)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeBindingSnapshot(locked)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Locked == nil || decoded.Locked.Basename != "Show - E01" || decoded.Locked.Directory != "Show/Season 01" {
		t.Fatalf("locked target did not round trip: %+v", decoded.Locked)
	}
	empty, err := decodeBindingSnapshot(nil)
	if err != nil || empty.Method != "" || empty.Locked != nil {
		t.Fatalf("nil snapshot must decode empty: %+v", empty)
	}
}
