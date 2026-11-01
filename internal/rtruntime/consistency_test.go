package rtruntime

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/wangkayu/reltitle/configs"
	"github.com/wangkayu/reltitle/pkg/config"
	"github.com/wangkayu/reltitle/pkg/parser"
)

// releaseTitles are release-shaped rather than clean ones: brackets, group
// names, SxxExx, episode ranges, Chinese and English titles. The parser's
// answer to these is what a subscription actually stores, so this is the
// surface where a subprocess and the linked library must not diverge.
var releaseTitles = []string{
	"[Lilith-Raws] 葬送的芙莉莲 - 28 [Baha][WEB-DL][1080p][AVC AAC][CHT][MP4]",
	"[Nekomoe kissaten&LoliHouse] 关于我转生变成史莱姆这档事 第三季 - 12 [WebRip 1080p HEVC-10bit AAC ASSx2].mkv",
	"[SubsPlease] Sousou no Frieren - 28 (1080p) [ABCD1234].mkv",
	"[VCB-Studio] Kono Subarashii Sekai ni Shukufuku wo! [01-10][Ma10p_1080p][x265_flac_aac]",
	"One Piece - 1100 [1080p][HEVC][Multi-Subs]",
	"[ANi] 鬼灭之刃 柱训练篇 - 08 [1080P][Baha][WEB-DL][AAC AVC][CHT][MP4]",
	"[Moozzi2] Monogatari Series Second Season (BD 1920x1080 x265-10Bit Flac) - TV + SP",
	"Spy x Family S02E05 1080p WEB-DL DDP5.1 H.264-NTb",
	"[SweetSub][天国大魔境][07][WebRip][1080p][AVC 8bit][简日双语]",
	"[jibaketa] Shingeki no Kyojin The Final Season Kanketsu-hen - 01 [1080p][Baha][WEB-DL][AAC AVC][CHT]",
}

var (
	realOnce sync.Once
	realPath string
	realErr  error
)

// realBinary builds the parser release binary from the module the program
// already depends on, once per test process. The build is pinned to the
// module cache, so this test either proves the installed binary agrees with
// the linked library or says the module cache is incomplete.
func realBinary(t *testing.T) string {
	t.Helper()
	realOnce.Do(func() {
		realPath = filepath.Join(binariesDir, "reltitle-real")
		realErr = buildGoBinary(realPath, "github.com/wangkayu/reltitle/cmd/reltitle")
	})
	if realErr != nil {
		t.Fatalf("build github.com/wangkayu/reltitle/cmd/reltitle: %v", realErr)
	}
	return realPath
}

func startReal(t *testing.T) *Client {
	t.Helper()
	client, err := Start(testContext(t), realBinary(t))
	if err != nil {
		t.Fatalf("start the reltitle binary: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func TestSubprocessParseMatchesTheLibrary(t *testing.T) {
	client := startReal(t)
	ctx := testContext(t)

	cfg, err := config.LoadAllFS(configs.FS)
	if err != nil {
		t.Fatalf("load the in-process configuration: %v", err)
	}
	inProcess := parser.New(cfg)

	for _, title := range releaseTitles {
		t.Run(title, func(t *testing.T) {
			want, err := inProcess.Parse(title)
			if err != nil {
				t.Fatalf("the library failed to parse its own title: %v", err)
			}
			if len(want.Attributes) == 0 {
				t.Fatalf("the title parsed to nothing; the fixture no longer exercises the parser")
			}
			wantJSON, err := json.Marshal(want)
			if err != nil {
				t.Fatalf("encode the library result: %v", err)
			}

			got, err := client.Parse(ctx, title)
			if err != nil {
				t.Fatalf("the subprocess refused a title the library parses: %v", err)
			}
			gotJSON, err := json.Marshal(got)
			if err != nil {
				t.Fatalf("encode the subprocess result: %v", err)
			}

			// Byte equality over the wire form is the contract: bansoko
			// stores and compares the projection of this JSON, so a field
			// that survives one path and not the other is a defect even when
			// both structs decode.
			if !bytes.Equal(wantJSON, gotJSON) {
				t.Fatalf("the subprocess disagrees with the library\nlibrary:    %s\nsubprocess: %s",
					abbreviate(wantJSON), abbreviate(gotJSON))
			}
		})
	}
}

func TestStartReportsTheEmbeddedBundleDigest(t *testing.T) {
	client := startReal(t)
	want, err := config.BundleDigest(configs.FS)
	if err != nil {
		t.Fatalf("digest the embedded bundle: %v", err)
	}
	if got := client.Info().ConfigDigest; got != want {
		t.Fatalf("the binary reports %q, but the linked bundle digests to %q", got, want)
	}
}

// abbreviate keeps a mismatch readable: the full JSON of a real release title
// is thousands of bytes, and the first difference is what a person needs.
func abbreviate(raw []byte) string {
	const limit = 1200
	if len(raw) <= limit {
		return string(raw)
	}
	return fmt.Sprintf("%s… (%d bytes)", raw[:limit], len(raw))
}
