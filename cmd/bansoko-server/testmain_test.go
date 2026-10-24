package main

import (
	"os"
	"testing"

	"bansoko/internal/attrrules"
)

func TestMain(m *testing.M) {
	rules := attrrules.Rules{
		Version:        1,
		Resolution:     []string{"2160p", "1440p", "1080p", "720p", "480p"},
		Quality:        []string{"remux", "web-dl", "webrip", "bluray", "bdrip", "hdtv"},
		Language:       []string{"chs", "cht", "sc", "tc", "jpn", "eng"},
		SpecialMarkers: []string{"special", "ncop", "nced"},
		GroupBrackets:  []string{"[", "]"},
	}
	fingerprint, err := attrrules.Fingerprint(rules)
	if err != nil {
		panic(err)
	}
	if err := attrrules.SetActive(rules, fingerprint); err != nil {
		panic(err)
	}

	os.Exit(m.Run())
}
