package organizingpolicy

import "testing"

func TestUnclassifiedDoesNotInventIdentity(t *testing.T) {
	for _, reason := range []UnclassifiedReason{Unknown, SpecialWithoutNumber, RangeOrList} {
		got := DecideUnclassified(true, reason, Route, false)
		if !got.Applicable || got.ReplacementEligible || got.Reason != string(reason)+"_route" {
			t.Fatalf("route=%+v", got)
		}
		got = DecideUnclassified(false, reason, Route, false)
		if got.Applicable || got.Reason != "filtered" {
			t.Fatalf("revived=%+v", got)
		}
		got = DecideUnclassified(true, reason, Skip, false)
		if got.Applicable || got.Reason != string(reason)+"_skip" {
			t.Fatalf("skip=%+v", got)
		}
	}
	got := DecideUnclassified(true, Unknown, Route, true)
	if got.Applicable || got.Reason != "parser_error" {
		t.Fatalf("parser error hidden: %+v", got)
	}
}
func TestExplicitSeasonZeroIsPresent(t *testing.T) {
	zero := Season{Value: 0, Present: true}
	missing := Season{}
	if zero == missing || !zero.Present || zero.Value != 0 {
		t.Fatal("explicit season zero lost")
	}
}
