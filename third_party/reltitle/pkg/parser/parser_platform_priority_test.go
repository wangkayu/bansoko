package parser

import (
	"testing"

	"github.com/wangkayu/reltitle/pkg/config"
	"github.com/wangkayu/reltitle/pkg/model"
)

func TestParserPlatformPriority_BSTXBeatsTX(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	cases := []string{
		"[shincaps] LIAR GAME - 01 (BS-TX 1440x1080 MPEG2 AAC).ts",
		"[shincaps] Isekai Nonbiri Nouka 2 - 01 (BS-TX 1440x1080 MPEG2 AAC).ts",
	}

	for _, raw := range cases {
		res, err := p.Parse(raw)
		if err != nil {
			t.Fatalf("parse failed for %q: %v", raw, err)
		}
		if got := firstAttrValue(res, model.AttrPlatform); got != "BS-TX" {
			t.Fatalf("platform=%q want=%q for %q", got, "BS-TX", raw)
		}
	}
}

func TestParserPlatformPriority_BSTBSBeatsTBS(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	cases := []string{
		"[shincaps] Kanojo, Okarishimasu 5th Season - 01 (BS-TBS 1440x1080 MPEG2 AAC).ts",
		"[Group] Sample Show - 01 (BS-TBS 1440x1080 MPEG2 AAC).ts",
	}

	for _, raw := range cases {
		res, err := p.Parse(raw)
		if err != nil {
			t.Fatalf("parse failed for %q: %v", raw, err)
		}
		if got := firstAttrValue(res, model.AttrPlatform); got != "BS-TBS" {
			t.Fatalf("platform=%q want=%q for %q", got, "BS-TBS", raw)
		}
	}
}

func TestParserPlatformPriority_BSNTVRemainsSpecific(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p := New(cfg)

	raw := "[Group] Sample Show - 01 (BS-NTV 1440x1080 MPEG2 AAC).ts"
	res, err := p.Parse(raw)
	if err != nil {
		t.Fatalf("parse failed for %q: %v", raw, err)
	}
	if got := firstAttrValue(res, model.AttrPlatform); got != "BS-NTV" {
		t.Fatalf("platform=%q want=%q", got, "BS-NTV")
	}
}
