package domain

import (
	"bytes"
	"testing"
)

func TestFreezeSourceTagsPreservesNativeEvidence(t *testing.T) {
	raw := []byte(`{"ignored":"not evidence","title_tags":{"schema":"v2","implementation":"frozen-parser","tags":[],"diagnostic":"empty"},"anibt":{"tags":["tag:HEVC","language:CHS"]}}`)
	frozen, err := FreezeSourceTags(raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := frozen.Object["ignored"]; ok {
		t.Fatal("unrelated source facts leaked into evidence")
	}
	if got := frozen.Object["anibt"].Object["tags"].Array[0].String; got != "tag:HEVC" {
		t.Fatalf("stored tag reclassified: %q", got)
	}
	if got := frozen.Object["title_tags"].Object["implementation"].String; got != "frozen-parser" {
		t.Fatalf("parser drift: %q", got)
	}
	again, err := FreezeSourceTags(Canonical(frozen))
	if err != nil || !bytes.Equal(Canonical(frozen), Canonical(again)) {
		t.Fatalf("freeze is not idempotent: %v", err)
	}
}

func TestFreezeSourceTagsKeepsMissingEvidenceMissing(t *testing.T) {
	for _, raw := range []string{"", "null", "{}", `{"title":"Show 1080p"}`} {
		value, err := FreezeSourceTags([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		if value.Object["title_tags"].Kind != KindNull || value.Object["anibt"].Kind != KindNull {
			t.Fatalf("invented evidence: %s", Canonical(value))
		}
	}
}

func TestFreezeSourceTagsRejectsMalformedEvidence(t *testing.T) {
	for _, raw := range []string{"[1]", "{", `{"title_tags":[]}`, `{"anibt":{"tags":[1]}}`, `{"title_tags":{"schema":"v2","implementation":"p","tags":[],"surprise":true}}`} {
		if _, err := FreezeSourceTags([]byte(raw)); err == nil {
			t.Fatalf("accepted malformed evidence: %s", raw)
		}
	}
}
