package parser

import (
	"math/rand"
	"testing"
	"time"

	"github.com/wangkayu/reltitle/pkg/config"
)

// Malformed titles must never hang or panic the parser: it is fed arbitrary RSS titles.
func TestParseTerminatesOnMalformedTitles(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	titles := []string{
		"[", "]", "[[", "]]", "[]", "[[]]", "(", "【", "】",
		"Show [", "[a[b]", "[[x]", "Show - 01 [1080p", "[Group] Show - 01 [1080p",
		"[搬運[ANi] A - 01 [1080P]] [v2]", "【Group] Show - 01", "[Group】 Show - 01",
		"[Group] Show - 01 [[1080p]", "[Group] Show - 01 ((1080p)", "★", "★ ★ ★", "- - -", "/ / /",
	}
	for _, title := range titles {
		done := make(chan struct{})
		go func() {
			defer close(done)
			defer func() { _ = recover() }() // a panic is reported separately below
			_, _ = p.Parse(title)
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatalf("Parse(%q) did not finish within 5s", title)
		}
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("Parse(%q) panicked: %v", title, r)
				}
			}()
			_, _ = p.Parse(title)
		}()
	}
}

// Random strings built from brackets, separators and common title fragments. With the
// unclosed-bracket bug in removeBracketsAroundFullyConsumedBlocks this finds a hang
// within the first few hundred strings.
func TestParseTerminatesOnRandomFragmentStrings(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	frags := []string{"[", "]", "(", ")", "【", "】", "「", "」", "{", "}", " ", "-", "_", ".", "/", "|", "+", "★", "~", "～", ":", "：",
		"01", "12", "125", "03.5", "v2", "S01", "S02E05", "E03", "EP05", "第", "5", "话", "集", "季", "合集", "END", "Fin", "TV", "OVA", "SP", "Movie",
		"1080p", "WEB-DL", "AAC", "HEVC", "x264", "CHS", "CHT", "简繁", "内封", "MP4", "Group", "Show", "Title", "标题", "タイトル", "Season", "2nd",
		"BD", "BDRip", "Vol", "No.", "&", ",", "'", "!", "?", "总集篇", "01-12", "01~12", "ASSx2", "MULTi", "NCOP", "(2019)"}
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 8000; i++ {
		s := ""
		for j, k := 0, 1+rng.Intn(14); j < k; j++ {
			s += frags[rng.Intn(len(frags))]
		}
		done := make(chan any, 1)
		go func(s string) {
			defer func() { done <- recover() }()
			_, _ = p.Parse(s)
		}(s)
		select {
		case r := <-done:
			if r != nil {
				t.Fatalf("Parse(%q) panicked: %v", s, r)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("Parse(%q) did not finish within 3s", s)
		}
	}
}
