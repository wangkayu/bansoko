package batchplan

import (
	"fmt"
	"testing"
)

func TestSameEpisodeTargets(t *testing.T) {
	cases := []struct {
		policy string
		manual bool
		want   string
	}{
		{"block", true, "[{true 0 } {false 0 target_duplicate} {false 0 target_duplicate}]"},
		{"fail", true, "[{true 0 } {false 0 target_duplicate} {false 0 target_duplicate}]"},
		{"version", true, "[{true 0 } {true 2 } {true 3 }]"},
		{"versioned", true, "[{true 0 } {true 2 } {true 3 }]"},
		{"overwrite", true, "[{false 0 target_replaced_in_release} {false 0 target_replaced_in_release} {true 0 }]"},
		{"version", false, "[{true 0 } {false 0 target_duplicate} {false 0 target_duplicate}]"},
		{"overwrite", false, "[{true 0 } {false 0 target_duplicate} {false 0 target_duplicate}]"},
	}
	for _, c := range cases {
		if got := fmt.Sprint(SameEpisodeTargets(c.policy, 3, c.manual)); got != c.want {
			t.Fatalf("%s manual=%t: %s, want %s", c.policy, c.manual, got, c.want)
		}
	}
	if len(SameEpisodeTargets("overwrite", 0, true)) != 0 {
		t.Fatal("empty group")
	}
}

func TestVersionedTargetPath(t *testing.T) {
	for in, want := range map[string]string{
		"/l/Show S01E01.mkv": "/l/Show S01E01 (2).mkv",
		"/l/v1.0/Show":       "/l/v1.0/Show (2)",
	} {
		if got := VersionedTargetPath(in, 2); got != want {
			t.Fatalf("%s: %s", in, got)
		}
	}
	if VersionedTargetPath("/l/a.mkv", 1) != "/l/a.mkv" {
		t.Fatal("version 1 is the target itself")
	}
}
