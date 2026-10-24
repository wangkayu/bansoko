package target

import (
	"encoding/json"
	"testing"

	"bansoko/internal/library/domain"
)

func size(value int64) *int64 { return &value }

func TestPlanSortsTargetsAndUsesExactEntrySize(t *testing.T) {
	set, err := Plan("release-1", []Entry{
		{TargetRef: "b.mkv", SourcePath: "b.mkv", MediaKind: "video", TargetPath: "/library/b.mkv", ExpectedSizeBytes: size(20), Intent: IntentEnsurePresent, CollisionPolicy: domain.CollisionBlock},
		{TargetRef: "a.mkv", SourcePath: "a.mkv", MediaKind: "video", TargetPath: "/library/a.mkv", ExpectedSizeBytes: size(10), Intent: IntentEnsurePresent, CollisionPolicy: domain.CollisionBlock},
	})
	if err != nil {
		t.Fatal(err)
	}
	if set.Targets[0].TargetRef != "a.mkv" || set.Targets[0].ReleaseID != "release-1" || set.Targets[0].ExpectedSizeBytes != 10 || set.Digest == "" {
		t.Fatalf("unexpected target set: %+v", set)
	}
	replayed, err := Plan("release-1", []Entry{
		{TargetRef: "a.mkv", SourcePath: "a.mkv", MediaKind: "video", TargetPath: "/library/a.mkv", ExpectedSizeBytes: size(10), Intent: IntentEnsurePresent, CollisionPolicy: domain.CollisionBlock},
		{TargetRef: "b.mkv", SourcePath: "b.mkv", MediaKind: "video", TargetPath: "/library/b.mkv", ExpectedSizeBytes: size(20), Intent: IntentEnsurePresent, CollisionPolicy: domain.CollisionBlock},
	})
	if err != nil || replayed.Digest != set.Digest {
		t.Fatalf("reordered plan must be digest stable: %q / %q (%v)", set.Digest, replayed.Digest, err)
	}
}

func TestPlanRejectsDuplicatePathsAndUnknownSize(t *testing.T) {
	base := Entry{TargetRef: "a", SourcePath: "a.mkv", MediaKind: "video", TargetPath: "/library/a.mkv", ExpectedSizeBytes: size(1), Intent: IntentEnsurePresent, CollisionPolicy: domain.CollisionOverwrite}
	if _, err := Plan("release", []Entry{base, {TargetRef: "b", SourcePath: "b.mkv", MediaKind: "video", TargetPath: "/library/a.mkv", ExpectedSizeBytes: size(2), Intent: IntentEnsurePresent, CollisionPolicy: domain.CollisionOverwrite}}); err == nil {
		t.Fatal("duplicate final target path must fail")
	}
	unknown := base
	unknown.ExpectedSizeBytes = nil
	if _, err := Plan("release", []Entry{unknown}); err == nil {
		t.Fatal("unknown entry size must fail")
	}
}

func TestPlanNormalizesSnapshotsAndRejectsUnsafePaths(t *testing.T) {
	set, err := Plan("release", []Entry{{TargetRef: "a", SourcePath: "dir\\a.mkv", MediaKind: "video", TargetPath: "/library/dir/../a.mkv", ExpectedSizeBytes: size(1), Intent: IntentReplace, CollisionPolicy: domain.CollisionOverwrite, RenderSnapshot: json.RawMessage(`{"name":"a"}`)}})
	if err != nil {
		t.Fatal(err)
	}
	if set.Targets[0].SourcePath != "dir/a.mkv" || string(set.Targets[0].RenderSnapshot) != `{"name":"a"}` {
		t.Fatalf("unexpected normalized target: %+v", set.Targets[0])
	}
	unsafe := Entry{TargetRef: "a", SourcePath: "../a.mkv", MediaKind: "video", TargetPath: "/library/a.mkv", ExpectedSizeBytes: size(1), Intent: IntentEnsurePresent, CollisionPolicy: domain.CollisionBlock}
	if _, err := Plan("release", []Entry{unsafe}); err == nil {
		t.Fatal("unsafe source path must fail")
	}
}
