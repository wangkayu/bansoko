package application

import "testing"

func TestAcquisitionCollisionOutcomeFromRecordedFacts(t *testing.T) {
	executed := func(f acquisitionCollisionFacts) acquisitionCollisionFacts {
		f.DeliveryOutcome, f.CompletionMode = "succeeded", "executed"
		return f
	}
	cases := []struct {
		name  string
		facts acquisitionCollisionFacts
		want  string
	}{
		{"block published", executed(acquisitionCollisionFacts{FrozenCollision: "block", OperationCollision: "block"}), AcquisitionCollisionNone},
		{"version at frozen target", executed(acquisitionCollisionFacts{OperationCollision: "version", FrozenTarget: "/m/T/S1/a.mkv", FinalPath: "/m/T/S1/a.mkv"}), AcquisitionCollisionNone},
		{"version suffix", executed(acquisitionCollisionFacts{OperationCollision: "version", FrozenTarget: "/m/T/S1/a.mkv", FinalPath: "/m/T/S1/a (1).mkv"}), AcquisitionCollisionVersioned},
		{"version without final path", executed(acquisitionCollisionFacts{OperationCollision: "version", FrozenTarget: "/m/T/S1/a.mkv"}), ""},
		{"overwrite displaced", executed(acquisitionCollisionFacts{OperationCollision: "overwrite", DisplacedState: "known"}), AcquisitionCollisionOverwritten},
		{"overwrite without proof", executed(acquisitionCollisionFacts{OperationCollision: "overwrite"}), ""},
		{"frozen policy when no operation", executed(acquisitionCollisionFacts{FrozenCollision: "block"}), AcquisitionCollisionNone},
		{"plan refused occupied", acquisitionCollisionFacts{PlanKind: "failed", PlanReasonCode: "TARGET_OCCUPIED"}, AcquisitionCollisionSkipped},
		{"publication refused occupied under block", acquisitionCollisionFacts{OperationCollision: "block", DeliveryOutcome: "failed", OperationFailure: "TARGET_OCCUPIED"}, AcquisitionCollisionSkipped},
		{"overwrite target vanished is not a skip", acquisitionCollisionFacts{OperationCollision: "overwrite", DeliveryOutcome: "failed", OperationFailure: "TARGET_OCCUPIED"}, ""},
		{"other failure", acquisitionCollisionFacts{OperationCollision: "block", DeliveryOutcome: "failed", OperationFailure: "SOURCE_MISSING"}, ""},
		{"plan naming collision", acquisitionCollisionFacts{PlanKind: "failed", PlanReasonCode: "PLAN_NAMING_COLLISION"}, ""},
		{"adopted in place", acquisitionCollisionFacts{OperationCollision: "block", DeliveryOutcome: "succeeded", CompletionMode: "adopted"}, ""},
		{"not delivered", acquisitionCollisionFacts{OperationCollision: "block"}, ""},
	}
	for _, tc := range cases {
		got := acquisitionCollisionOutcome(tc.facts)
		if (got == nil) != (tc.want == "") || (got != nil && *got != tc.want) {
			t.Errorf("%s: outcome = %v, want %q", tc.name, got, tc.want)
		}
	}
}

func TestLibraryRelativeDisplay(t *testing.T) {
	visible := libraryRelativeDisplay("/media/anime", "/media/anime/Title （2021）/Season 02/Title - S02E01.mkv")
	if visible.State != AcquisitionDisplayVisible || visible.Value == nil || *visible.Value != "Title （2021）/Season 02/Title - S02E01.mkv" {
		t.Fatalf("visible = %+v", visible)
	}
	// Outside the root the last three segments remain; never an absolute path.
	outside := libraryRelativeDisplay("/media/anime", "/other/lib/T/S1/a.mkv")
	if outside.Value == nil || *outside.Value != "T/S1/a.mkv" {
		t.Fatalf("outside = %+v", outside)
	}
	secret := libraryRelativeDisplay("/m", "/m/T/token=abc/a.mkv")
	if secret.State != AcquisitionDisplayRedacted || secret.Value != nil {
		t.Fatalf("secret = %+v", secret)
	}
}
