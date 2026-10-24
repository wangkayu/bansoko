package application

import (
	"os"
	"testing"

	"bansoko/internal/attrrules"
	"bansoko/internal/namingvars"
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
	attrFingerprint, err := attrrules.Fingerprint(rules)
	if err != nil {
		panic(err)
	}
	if err := attrrules.SetActive(rules, attrFingerprint); err != nil {
		panic(err)
	}

	vars := namingvars.Vars{"test": {"marker": "ok"}}
	varsFingerprint, err := namingvars.Fingerprint(vars)
	if err != nil {
		panic(err)
	}
	if err := namingvars.SetActive(vars, varsFingerprint); err != nil {
		panic(err)
	}

	os.Exit(m.Run())
}
