package domain

import "testing"

func TestScanScopeRejectsHostPathsAndTraversal(t *testing.T) {
	valid := ScanScope{Kind: ScanScopeTargeted, RelativePath: "season/episode.mkv"}
	if !valid.Valid() {
		t.Fatal("valid relative targeted scope was rejected")
	}
	for _, scope := range []ScanScope{
		{Kind: ScanScopeTargeted, RelativePath: ""},
		{Kind: ScanScopeTargeted, RelativePath: "/tmp/episode.mkv"},
		{Kind: ScanScopeTargeted, RelativePath: "../episode.mkv"},
		{Kind: ScanScopeTargeted, RelativePath: "season/../../episode.mkv"},
	} {
		if scope.Valid() {
			t.Fatalf("unsafe targeted scope was accepted: %+v", scope)
		}
	}
}

func TestScanScopeFullDoesNotAcceptTargetPath(t *testing.T) {
	if !(ScanScope{Kind: ScanScopeFull}).Valid() {
		t.Fatal("full scope was rejected")
	}
	if (ScanScope{Kind: ScanScopeFull, RelativePath: "episode.mkv"}).Valid() {
		t.Fatal("full scope accepted a target path")
	}
}
