package parser

import (
	"encoding/json"
	"testing"

	"github.com/wangkayu/reltitle/pkg/config"
)

// NewDefault must behave exactly like a Parser built from the configs/
// directory, since both load the same YAML files.
func TestNewDefaultMatchesLoadAll(t *testing.T) {
	cfg, err := config.LoadAll("../../configs")
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	fromFiles := New(cfg)
	embedded, err := NewDefault()
	if err != nil {
		t.Fatalf("NewDefault: %v", err)
	}

	titles := []string{
		"[Group] Some Show - 03 [1080p]",
		"[Group] Some Show S02E05 [1080p]",
		"[Group] Some Movie [01-12 合集][1080p]",
		"[LoliHouse] Kimi no Na wa [Movie][BDRip][1920x1080][HEVC-10bit]",
	}
	for _, title := range titles {
		a, err := fromFiles.Parse(title)
		if err != nil {
			t.Fatalf("Parse(%q) from files: %v", title, err)
		}
		b, err := embedded.Parse(title)
		if err != nil {
			t.Fatalf("Parse(%q) embedded: %v", title, err)
		}
		ja, _ := json.Marshal(a)
		jb, _ := json.Marshal(b)
		if string(ja) != string(jb) {
			t.Fatalf("Parse(%q) differs between file-loaded and embedded configs:\nfiles:   %s\nembedded: %s", title, ja, jb)
		}
	}
}
