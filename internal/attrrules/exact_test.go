package attrrules

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func exactFixture(t *testing.T) (Rules, string, []byte) {
	t.Helper()
	rules := Rules{Version: 1, Resolution: []string{"1080p"}, Quality: []string{"WebRip"}, Language: []string{"CHS"}, SpecialMarkers: []string{"SP"}, GroupBrackets: []string{"[", "]"}}
	digest, err := Fingerprint(rules)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(rules)
	if err != nil {
		t.Fatal(err)
	}
	return rules, digest, raw
}

func TestLoadExactReturnsTheVerifiedPinnedRules(t *testing.T) {
	want, digest, raw := exactFixture(t)
	got, err := LoadExact(context.Background(), ProfileVersion(digest), digest, func(_ context.Context, fp string) ([]byte, error) {
		if fp != digest {
			t.Fatalf("read for %q, want the pinned digest", fp)
		}
		return raw, nil
	})
	if err != nil {
		t.Fatalf("LoadExact: %v", err)
	}
	if got.Version != want.Version || len(got.Resolution) != 1 || got.Resolution[0] != "1080p" {
		t.Fatalf("loaded rules differ: %+v", got)
	}
}

func TestLoadExactFailsClosedInsteadOfSubstitutingAnotherRuleSet(t *testing.T) {
	_, digest, raw := exactFixture(t)
	ok := func(context.Context, string) ([]byte, error) { return raw, nil }
	if _, err := LoadExact(context.Background(), "wrong@"+digest, digest, ok); err == nil {
		t.Fatal("a version that does not match the digest must be refused")
	}
	if _, err := LoadExact(context.Background(), ProfileVersion(""), "", ok); err == nil {
		t.Fatal("an empty digest must be refused")
	}
	missing := func(context.Context, string) ([]byte, error) { return nil, errors.New("no rows") }
	if _, err := LoadExact(context.Background(), ProfileVersion(digest), digest, missing); err == nil {
		t.Fatal("an unreadable pinned source must be an error, not the active rule set")
	}
	other := Rules{Version: 1, Resolution: []string{"720p"}, Quality: []string{"x"}, Language: []string{"x"}, SpecialMarkers: []string{"x"}, GroupBrackets: []string{"[", "]"}}
	tampered, _ := json.Marshal(other)
	if _, err := LoadExact(context.Background(), ProfileVersion(digest), digest, func(context.Context, string) ([]byte, error) { return tampered, nil }); err == nil {
		t.Fatal("content that does not match its fingerprint must be refused")
	}
}
