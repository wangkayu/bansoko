package target

import (
	"testing"

	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/library/domain"
)

// An overwrite target above the skip or version policy exists only through an
// approved replacement. Acceptance proves each one is observed absent or has
// its approval frozen into the delivery; anything else is refused before the
// download. Under the overwrite policy every overwrite target is authorized by
// the policy itself.
func TestUncoveredApprovedTarget(t *testing.T) {
	set := Set{Targets: []Spec{
		{ID: "a", TargetPath: "/library/a.mkv", Intent: IntentReplace, CollisionPolicy: domain.CollisionOverwrite},
		{ID: "b", TargetPath: "/library/b.mkv", Intent: IntentReplace, CollisionPolicy: domain.CollisionOverwrite},
		{ID: "c", TargetPath: "/library/c.mkv", Intent: IntentEnsurePresent, CollisionPolicy: domain.CollisionBlock},
	}}
	observations := []Observation{
		{TargetID: "a", State: ObservationPresentMismatched},
		{TargetID: "b", State: ObservationMissing},
		{TargetID: "c", State: ObservationPresentMatched},
	}
	effective := func(policy animedomain.CollisionPolicy) animedomain.EffectiveConfig {
		return animedomain.EffectiveConfig{Rules: animedomain.EffectiveRules{FileEffect: animedomain.FileEffectRules{CollisionPolicy: policy}}}
	}
	for _, policy := range []animedomain.CollisionPolicy{animedomain.CollisionPolicyFail, animedomain.CollisionPolicyVersioned} {
		if got := UncoveredApprovedTarget(set, observations, effective(policy), map[string]bool{"/library/a.mkv": true}); got != "" {
			t.Fatalf("%s: covered present target and absent target reported %q", policy, got)
		}
		if got := UncoveredApprovedTarget(set, observations, effective(policy), map[string]bool{}); got != "/library/a.mkv" {
			t.Fatalf("%s: present target without approval = %q, want /library/a.mkv", policy, got)
		}
	}
	if got := UncoveredApprovedTarget(set, observations, effective(animedomain.CollisionPolicyOverwrite), map[string]bool{}); got != "" {
		t.Fatalf("overwrite policy authorizes every overwrite target, got %q", got)
	}
}
