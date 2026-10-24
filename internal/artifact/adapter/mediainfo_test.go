package adapter

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bansoko/internal/download/domain"
)

// realVideoAudioJSON is the shape `mediainfo --Output=JSON` prints for a
// 1920x1080 HEVC + AAC Matroska file, including a non-string value ("extra")
// that the mapping must tolerate.
const realVideoAudioJSON = `{
"creatingLibrary":{"name":"MediaInfoLib","version":"25.10","url":"https://mediaarea.net/MediaInfo"},
"media":{"@ref":"x.mkv","track":[{"@type":"General","VideoCount":"1","AudioCount":"1","FileExtension":"mkv","Format":"Matroska","Format_Version":"4",
"extra":{"ErrorDetectionType":"Per level 1"}},{"@type":"Video","Format":"HEVC","Format_Profile":"Main","CodecID":"V_MPEGH/ISO/HEVC","Width":"1920","Height":"1080","Default":"No"},{"@type":"Audio","Format":"AAC","Format_AdditionalFeatures":"LC","CodecID":"A_AAC-2","Default":"No"}]}
}`

func TestParseMediaInfoMapsVideoAudioAndContainer(t *testing.T) {
	facts, err := parseMediaInfo([]byte(realVideoAudioJSON))
	if err != nil {
		t.Fatal(err)
	}
	want := domain.MediaFacts{Resolution: "1080p", VideoCodec: "HEVC", AudioCodec: "AAC", Container: "mkv"}
	if facts != want {
		t.Fatalf("facts = %+v, want %+v", facts, want)
	}
}

func TestParseMediaInfoVocabulary(t *testing.T) {
	cases := []struct {
		name  string
		track string
		want  domain.MediaFacts
	}{
		{
			name:  "avc 720p mp4 ac-3",
			track: `{"@type":"General","Format":"MPEG-4","Format_Profile":"Base Media"},{"@type":"Video","Format":"AVC","Width":"1280","Height":"720"},{"@type":"Audio","Format":"AC-3"}`,
			want:  domain.MediaFacts{Resolution: "720p", VideoCodec: "AVC", AudioCodec: "AC3", Container: "mp4"},
		},
		{
			name:  "cropped 1080p keeps its class",
			track: `{"@type":"General","Format":"Matroska"},{"@type":"Video","Format":"HEVC","Width":"1920","Height":"800"}`,
			want:  domain.MediaFacts{Resolution: "1080p", VideoCodec: "HEVC", Container: "mkv"},
		},
		{
			name:  "pillarboxed 1080p by height",
			track: `{"@type":"General","Format":"Matroska"},{"@type":"Video","Format":"AV1","Width":"1440","Height":"1080"}`,
			want:  domain.MediaFacts{Resolution: "1080p", VideoCodec: "AV1", Container: "mkv"},
		},
		{
			name:  "2160p flac",
			track: `{"@type":"General","Format":"Matroska"},{"@type":"Video","Format":"HEVC","Width":"3840","Height":"2160"},{"@type":"Audio","Format":"FLAC"}`,
			want:  domain.MediaFacts{Resolution: "2160p", VideoCodec: "HEVC", AudioCodec: "FLAC", Container: "mkv"},
		},
		{
			name:  "480p xvid avi mp3",
			track: `{"@type":"General","Format":"AVI"},{"@type":"Video","Format":"MPEG-4 Visual","CodecID":"XVID","Width":"640","Height":"480"},{"@type":"Audio","Format":"MPEG Audio","Format_Profile":"Layer 3"}`,
			want:  domain.MediaFacts{Resolution: "480p", VideoCodec: "XVID", AudioCodec: "MP3", Container: "avi"},
		},
		{
			name:  "mpeg2 transport stream with truehd",
			track: `{"@type":"General","Format":"BDAV"},{"@type":"Video","Format":"MPEG Video","Format_Version":"Version 2","Width":"1920","Height":"1080"},{"@type":"Audio","Format":"MLP FBA"}`,
			want:  domain.MediaFacts{Resolution: "1080p", VideoCodec: "MPEG2", AudioCodec: "TRUEHD", Container: "m2ts"},
		},
		{
			name:  "unmapped codec keeps a safe token",
			track: `{"@type":"General","Format":"Some Container/2"},{"@type":"Video","Format":"VP9","Width":"1280","Height":"720"},{"@type":"Audio","Format":"Vorbis/x y"}`,
			want:  domain.MediaFacts{Resolution: "720p", VideoCodec: "VP9", AudioCodec: "VORBISXY", Container: "somecontainer2"},
		},
		{
			name:  "non-string width and height leave the resolution empty",
			track: `{"@type":"General","Format":"Matroska"},{"@type":"Video","Format":"HEVC","Width":1920,"Height":1080}`,
			want:  domain.MediaFacts{VideoCodec: "HEVC", Container: "mkv"},
		},
		{
			name:  "below the smallest class leaves the resolution empty",
			track: `{"@type":"General","Format":"Matroska"},{"@type":"Video","Format":"AVC","Width":"320","Height":"240"}`,
			want:  domain.MediaFacts{VideoCodec: "AVC", Container: "mkv"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			facts, err := parseMediaInfo([]byte(`{"media":{"track":[` + tc.track + `]}}`))
			if err != nil {
				t.Fatal(err)
			}
			if facts != tc.want {
				t.Fatalf("facts = %+v, want %+v", facts, tc.want)
			}
		})
	}
}

func TestParseMediaInfoNormalizesHDR(t *testing.T) {
	cases := []struct {
		name  string
		video string
		want  string
	}{
		{name: "hdr10 compatibility", video: `"HDR_Format_Compatibility":"HDR10"`, want: "hdr10"},
		{name: "smpte st 2086 compatibility", video: `"HDR_Format_Compatibility":"SMPTE ST 2086"`, want: "hdr10"},
		{name: "hlg compatibility", video: `"HDR_Format_Compatibility":"HLG"`, want: "hlg"},
		{name: "dolby vision alone", video: `"HDR_Format_Compatibility":"Dolby Vision"`, want: "dovi"},
		{name: "combined compatibility keeps the document order", video: `"HDR_Format_Compatibility":"HDR10, Dolby Vision"`, want: "hdr10,dovi"},
		{name: "repeated formats join once", video: `"HDR_Format_Compatibility":"HDR10, HLG, HLG"`, want: "hdr10,hlg"},
		{name: "sdr is empty", video: `"HDR_Format_Compatibility":"SDR"`, want: ""},
		{name: "case-insensitive", video: `"HDR_Format_Compatibility":"dolby vision"`, want: "dovi"},
		{name: "compatibility preferred over HDR_Format", video: `"HDR_Format":"SMPTE ST 2086, HDR10 compatible","HDR_Format_Compatibility":"HDR10"`, want: "hdr10"},
		{name: "hdr_format fallback finds the known words", video: `"HDR_Format":"Dolby Vision, Version 1.0, dvhe.05, BL+RPU"`, want: "dovi"},
		{name: "hdr_format smpte", video: `"HDR_Format":"SMPTE ST 2086, HDR10 compatible"`, want: "hdr10"},
		{name: "hdr_format hlg", video: `"HDR_Format":"HLG"`, want: "hlg"},
		{name: "hdr_format sdr is empty", video: `"HDR_Format":"SDR"`, want: ""},
		{name: "unknown compatibility passes through", video: `"HDR_Format_Compatibility":"HDR10+ (Annex S)"`, want: "hdr10+-(annex-s)"},
		{name: "unknown hdr_format passes through", video: `"HDR_Format":"SL-HDR1"`, want: "sl-hdr1"},
		{name: "absent is empty", video: "", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			comma := ""
			if tc.video != "" {
				comma = ","
			}
			document := `{"media":{"track":[{"@type":"General","Format":"Matroska"},{"@type":"Video","Format":"HEVC","Width":"1920","Height":"1080"` + comma + tc.video + `}]}}`
			facts, err := parseMediaInfo([]byte(document))
			if err != nil {
				t.Fatal(err)
			}
			if facts.HDR != tc.want {
				t.Fatalf("HDR = %q, want %q", facts.HDR, tc.want)
			}
		})
	}
	// A file without a video track has no HDR either.
	if facts, err := parseMediaInfo([]byte(`{"media":{"track":[{"@type":"General","Format":"FLAC"}]}}`)); err != nil || facts.HDR != "" {
		t.Fatalf("audio only: HDR = %q, err %v; want empty", facts.HDR, err)
	}
}

func TestParseMediaInfoAudioOnlyLeavesVideoFactsEmpty(t *testing.T) {
	facts, err := parseMediaInfo([]byte(`{"media":{"track":[{"@type":"General","Format":"FLAC"},{"@type":"Audio","Format":"FLAC"}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	want := domain.MediaFacts{AudioCodec: "FLAC", Container: "flac"}
	if facts != want {
		t.Fatalf("facts = %+v, want %+v", facts, want)
	}
}
func TestParseMediaInfoMultipleTracksPrefersDefaultThenFirst(t *testing.T) {
	// A dual-audio release lists a commentary first; the Default flag wins.
	flagged, err := parseMediaInfo([]byte(`{"media":{"track":[
{"@type":"General","Format":"Matroska"},
{"@type":"Video","Format":"AVC","Width":"1280","Height":"720","Default":"No"},
{"@type":"Video","Format":"HEVC","Width":"1920","Height":"1080","Default":"Yes"},
{"@type":"Audio","Format":"AC-3","Default":"No"},
{"@type":"Audio","Format":"AAC","Default":"Yes"},
{"@type":"Text","Format":"ASS","Default":"Yes"}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	want := domain.MediaFacts{Resolution: "1080p", VideoCodec: "HEVC", AudioCodec: "AAC", Container: "mkv"}
	if flagged != want {
		t.Fatalf("flagged facts = %+v, want %+v", flagged, want)
	}

	// Without a Default flag the first stream of each kind is used.
	unflagged, err := parseMediaInfo([]byte(`{"media":{"track":[
{"@type":"General","Format":"Matroska"},
{"@type":"Video","Format":"AVC","Width":"1280","Height":"720"},
{"@type":"Video","Format":"HEVC","Width":"1920","Height":"1080"},
{"@type":"Audio","Format":"E-AC-3"},
{"@type":"Audio","Format":"AAC"}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	want = domain.MediaFacts{Resolution: "720p", VideoCodec: "AVC", AudioCodec: "EAC3", Container: "mkv"}
	if unflagged != want {
		t.Fatalf("unflagged facts = %+v, want %+v", unflagged, want)
	}
}

func TestParseMediaInfoMissingTracksYieldEmptyFacts(t *testing.T) {
	for name, document := range map[string]string{
		"no tracks key":      `{"media":{"@ref":"x.mkv"}}`,
		"empty track list":   `{"media":{"track":[]}}`,
		"empty object":       `{}`,
		"general only":       `{"media":{"track":[{"@type":"General"}]}}`,
		"text only":          `{"media":{"track":[{"@type":"Text","Format":"ASS"}]}}`,
		"null media":         `{"media":null}`,
		"unknown track type": `{"media":{"track":[{"@type":"Menu","Format":"x"}]}}`,
	} {
		t.Run(name, func(t *testing.T) {
			facts, err := parseMediaInfo([]byte(document))
			if err != nil {
				t.Fatalf("a document without streams is not an error: %v", err)
			}
			if !facts.IsZero() {
				t.Fatalf("facts = %+v, want none", facts)
			}
		})
	}
}

func TestParseMediaInfoMalformedJSON(t *testing.T) {
	for name, document := range map[string]string{
		"empty":       ``,
		"not json":    `MediaInfo: unable to read the file`,
		"truncated":   `{"media":{"track":[{"@type":"Video","Format":"HEV`,
		"wrong top":   `[1,2,3]`,
		"wrong media": `{"media":"x"}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := parseMediaInfo([]byte(document))
			if !errors.Is(err, domain.ErrMediaProbeOutputInvalid) {
				t.Fatalf("error = %v, want ErrMediaProbeOutputInvalid", err)
			}
		})
	}
}

func TestCodecTokenIsFileNameSafeAndBounded(t *testing.T) {
	if got := codecToken("../../etc/passwd; rm -rf /"); got != "ETCPASSWDRMRF" {
		t.Fatalf("token = %q", got)
	}
	if got := codecToken(strings.Repeat("a", 100)); len(got) != 16 {
		t.Fatalf("token length = %d, want 16", len(got))
	}
	if got := codecToken("***"); got != "" {
		t.Fatalf("token = %q, want empty", got)
	}
}

// fakeTool writes an executable /bin/sh script into its own directory and
// returns its path. The script stands in for the mediainfo binary.
func fakeTool(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mediainfo")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

// mediaFile creates a regular file the probe may point the tool at, in its own
// directory so that file names with shell metacharacters stay isolated.
func mediaFile(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte("not a real video"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestOSMediaProberReadsFactsFromTheTool(t *testing.T) {
	tool := fakeTool(t, "cat <<'EOF'\n"+realVideoAudioJSON+"\nEOF")
	facts, err := (OSMediaProber{Binary: tool}).Probe(context.Background(), mediaFile(t, "a.mkv"))
	if err != nil {
		t.Fatal(err)
	}
	want := domain.MediaFacts{Resolution: "1080p", VideoCodec: "HEVC", AudioCodec: "AAC", Container: "mkv"}
	if facts != want {
		t.Fatalf("facts = %+v, want %+v", facts, want)
	}
}

func TestOSMediaProberPassesThePathAsOneArgumentWithoutAShell(t *testing.T) {
	evidence := filepath.Join(t.TempDir(), "argv")
	// The tool records its arguments verbatim, one per line, then answers with
	// an empty document.
	tool := fakeTool(t, `for a in "$@"; do printf '%s\n' "$a"; done > "`+evidence+`"
echo '{"media":{}}'`)
	hostile := "a b;$(touch pwned)`touch pwned2`'\"&|<>*?.mkv"
	path := mediaFile(t, hostile)

	if _, err := (OSMediaProber{Binary: tool}).Probe(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	recorded, err := os.ReadFile(evidence)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(recorded), "\n"), "\n")
	if len(lines) != 2 || lines[0] != "--Output=JSON" || lines[1] != path {
		t.Fatalf("argv = %q, want [--Output=JSON %q]", lines, path)
	}
	for _, leaked := range []string{"pwned", "pwned2"} {
		for _, dir := range []string{filepath.Dir(path), "."} {
			if _, err := os.Stat(filepath.Join(dir, leaked)); err == nil {
				t.Fatalf("a shell expanded the file name: %s exists in %s", leaked, dir)
			}
		}
	}
}

func TestOSMediaProberAnchorsRelativePathsSoTheyCannotReadAsOptions(t *testing.T) {
	evidence := filepath.Join(t.TempDir(), "argv")
	tool := fakeTool(t, `printf '%s\n' "$2" > "`+evidence+`"
echo '{"media":{}}'`)
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.WriteFile("--Output=XML", []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := (OSMediaProber{Binary: tool}).Probe(context.Background(), "--Output=XML"); err != nil {
		t.Fatal(err)
	}
	recorded, err := os.ReadFile(evidence)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.TrimSpace(string(recorded))
	if !filepath.IsAbs(got) || !strings.HasSuffix(got, "--Output=XML") {
		t.Fatalf("path argument = %q, want an absolute path", got)
	}
}

func TestOSMediaProberRunsWithAMinimalEnvironment(t *testing.T) {
	t.Setenv("BANSOKO_SECRET_SENTINEL", "must-not-leak")
	evidence := filepath.Join(t.TempDir(), "env")
	tool := fakeTool(t, `env > "`+evidence+`"
echo '{"media":{}}'`)
	if _, err := (OSMediaProber{Binary: tool}).Probe(context.Background(), mediaFile(t, "a.mkv")); err != nil {
		t.Fatal(err)
	}
	recorded, err := os.ReadFile(evidence)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(recorded), "BANSOKO_SECRET_SENTINEL") {
		t.Fatalf("the tool inherited the process environment:\n%s", recorded)
	}
}

func TestOSMediaProberMissingToolIsUnavailable(t *testing.T) {
	_, err := (OSMediaProber{Binary: filepath.Join(t.TempDir(), "no-such-mediainfo")}).Probe(context.Background(), mediaFile(t, "a.mkv"))
	if !errors.Is(err, domain.ErrMediaProbeUnavailable) {
		t.Fatalf("error = %v, want ErrMediaProbeUnavailable", err)
	}
}

func TestOSMediaProberToolFailureIsFailed(t *testing.T) {
	tool := fakeTool(t, "echo 'boom' >&2\nexit 3")
	_, err := (OSMediaProber{Binary: tool}).Probe(context.Background(), mediaFile(t, "a.mkv"))
	if !errors.Is(err, domain.ErrMediaProbeFailed) {
		t.Fatalf("error = %v, want ErrMediaProbeFailed", err)
	}
	if strings.Contains(err.Error(), "boom") {
		t.Fatalf("tool output leaked into the error: %v", err)
	}
}

func TestOSMediaProberMalformedOutputIsOutputInvalid(t *testing.T) {
	tool := fakeTool(t, "echo 'this is not json'")
	_, err := (OSMediaProber{Binary: tool}).Probe(context.Background(), mediaFile(t, "a.mkv"))
	if !errors.Is(err, domain.ErrMediaProbeOutputInvalid) {
		t.Fatalf("error = %v, want ErrMediaProbeOutputInvalid", err)
	}
}

func TestOSMediaProberTimeoutStopsTheTool(t *testing.T) {
	// The tool starts a child that holds the output pipe open, so the call also
	// proves that a grandchild cannot keep it past its deadline.
	tool := fakeTool(t, "sleep 5 &\nwait")
	started := time.Now()
	_, err := (OSMediaProber{Binary: tool, Timeout: 200 * time.Millisecond}).Probe(context.Background(), mediaFile(t, "a.mkv"))
	elapsed := time.Since(started)
	if !errors.Is(err, domain.ErrMediaProbeTimeout) {
		t.Fatalf("error = %v, want ErrMediaProbeTimeout", err)
	}
	if elapsed > 10*time.Second {
		t.Fatalf("probe returned after %s, want it bounded by the timeout and wait delay", elapsed)
	}
}

func TestOSMediaProberOversizedOutputIsRejectedAndStopsTheTool(t *testing.T) {
	// An endless stream stands in for a hostile or broken answer.
	tool := fakeTool(t, "while :; do echo xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx; done")
	started := time.Now()
	_, err := (OSMediaProber{Binary: tool, MaxOutputBytes: 4096, Timeout: 20 * time.Second}).Probe(context.Background(), mediaFile(t, "a.mkv"))
	if !errors.Is(err, domain.ErrMediaProbeOutputInvalid) {
		t.Fatalf("error = %v, want ErrMediaProbeOutputInvalid", err)
	}
	if time.Since(started) > 10*time.Second {
		t.Fatalf("an oversized answer was not cut short: %s", time.Since(started))
	}
}

func TestOSMediaProberOutputAtTheLimitIsAccepted(t *testing.T) {
	document := `{"media":{"track":[{"@type":"General","Format":"Matroska"}]}}`
	tool := fakeTool(t, "printf '%s' '"+document+"'")
	facts, err := (OSMediaProber{Binary: tool, MaxOutputBytes: len(document)}).Probe(context.Background(), mediaFile(t, "a.mkv"))
	if err != nil {
		t.Fatal(err)
	}
	if facts.Container != "mkv" {
		t.Fatalf("facts = %+v", facts)
	}
}

func TestOSMediaProberRefusesNonRegularFilesWithoutRunningTheTool(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "ran")
	tool := fakeTool(t, `: > "`+marker+`"
echo '{"media":{}}'`)
	dir := t.TempDir()
	link := filepath.Join(dir, "link.mkv")
	target := mediaFile(t, "real.mkv")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	for name, path := range map[string]string{
		"directory": dir,
		"symlink":   link,
		"missing":   filepath.Join(dir, "missing.mkv"),
		"empty":     "",
	} {
		_, err := (OSMediaProber{Binary: tool}).Probe(context.Background(), path)
		if !errors.Is(err, domain.ErrMediaProbeFailed) {
			t.Fatalf("%s: error = %v, want ErrMediaProbeFailed", name, err)
		}
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the tool ran for a path that is not a regular file")
	}
}

func TestOSMediaProberHonoursACanceledParentContext(t *testing.T) {
	tool := fakeTool(t, "sleep 5 &\nwait")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := (OSMediaProber{Binary: tool}).Probe(ctx, mediaFile(t, "a.mkv"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}
