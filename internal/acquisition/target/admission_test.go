package target

import (
	"testing"

	"bansoko/internal/library/domain"
)

func TestDecideAdoptsOnlyAllExactEnsureTargets(t *testing.T) {
	set, err := Plan("release", []Entry{
		{TargetRef: "a", SourcePath: "a.mkv", MediaKind: "video", TargetPath: "/library/a.mkv", ExpectedSizeBytes: size(1), Intent: IntentEnsurePresent, CollisionPolicy: domain.CollisionBlock},
		{TargetRef: "b", SourcePath: "b.ass", MediaKind: "subtitle", TargetPath: "/library/b.ass", ExpectedSizeBytes: size(2), Intent: IntentEnsurePresent, CollisionPolicy: domain.CollisionBlock},
	})
	if err != nil {
		t.Fatal(err)
	}
	result := Decide(set, []Observation{{TargetID: set.Targets[0].ID, State: ObservationPresentMatched}, {TargetID: set.Targets[1].ID, State: ObservationPresentMatched}})
	if result.Admission != AdmissionAdopted || result.Matched != 2 {
		t.Fatalf("unexpected adoption decision: %+v", result)
	}
	result = Decide(set, []Observation{{TargetID: set.Targets[0].ID, State: ObservationPresentMatched}, {TargetID: set.Targets[1].ID, State: ObservationPresentMismatched}})
	if result.Admission != AdmissionFailed || result.Reason != "target_collision_blocked" {
		t.Fatalf("block collision must fail closed: %+v", result)
	}
}

func TestDecideReplacementNeverAdoptsSameSizeIncumbent(t *testing.T) {
	set, err := Plan("release", []Entry{{TargetRef: "a", SourcePath: "a.mkv", MediaKind: "video", TargetPath: "/library/a.mkv", ExpectedSizeBytes: size(4), Intent: IntentReplace, CollisionPolicy: domain.CollisionOverwrite}})
	if err != nil {
		t.Fatal(err)
	}
	result := Decide(set, []Observation{{TargetID: set.Targets[0].ID, State: ObservationPresentMatched}})
	if result.Admission != AdmissionDownload || result.Reason != "replacement_requires_new_artifact" {
		t.Fatalf("replacement must not short-circuit: %+v", result)
	}
}

// DecideTargets names only the conflicting targets; Decide still decides the
// rest together.
func TestDecideTargetsReportsPerTargetConflicts(t *testing.T) {
	set, err := Plan("release", []Entry{
		{TargetRef: "a", SourcePath: "a.mkv", MediaKind: "video", TargetPath: "/library/a.mkv", ExpectedSizeBytes: size(1), Intent: IntentEnsurePresent, CollisionPolicy: domain.CollisionBlock},
		{TargetRef: "b", SourcePath: "b.mkv", MediaKind: "video", TargetPath: "/library/b.mkv", ExpectedSizeBytes: size(1), Intent: IntentEnsurePresent, CollisionPolicy: domain.CollisionBlock},
		{TargetRef: "c", SourcePath: "c.mkv", MediaKind: "video", TargetPath: "/library/c.mkv", ExpectedSizeBytes: size(1), Intent: IntentEnsurePresent, CollisionPolicy: domain.CollisionBlock},
		{TargetRef: "d", SourcePath: "d.mkv", MediaKind: "video", TargetPath: "/library/d.mkv", ExpectedSizeBytes: size(1), Intent: IntentReplace, CollisionPolicy: domain.CollisionBlock},
	})
	if err != nil {
		t.Fatal(err)
	}
	observations := []Observation{
		{TargetID: set.Targets[0].ID, State: ObservationMissing},
		{TargetID: set.Targets[1].ID, State: ObservationPresentMismatched},
		{TargetID: set.Targets[2].ID, State: ObservationUnreadable},
		{TargetID: set.Targets[3].ID, State: ObservationPresentMatched},
	}
	got := DecideTargets(set, observations)
	want := map[string]string{"b": "target_collision_blocked", "c": "target_unreadable", "d": "replacement_collision_blocked"}
	if len(got) != len(want) {
		t.Fatalf("blocked = %v, want %v", got, want)
	}
	for ref, reason := range want {
		if got[ref] != reason {
			t.Fatalf("blocked = %v, want %v", got, want)
		}
	}
}
