package bootstrap

import (
	"strings"
	"testing"

	"bansoko/internal/update/packagemaker"
)

func TestValidateUnifiedReleaseMetadata(t *testing.T) {
	for _, test := range []struct {
		name    string
		files   []packagemaker.FileRecord
		wantErr string
	}{
		{name: "unified", files: []packagemaker.FileRecord{{Path: packagemaker.ServerEntryPath}}},
		{name: "retired layout", files: []packagemaker.FileRecord{
			{Path: packagemaker.ServerEntryPath}, {Path: packagemaker.MigrateEntryPath},
		}, wantErr: "retired"},
		{name: "missing server", wantErr: "no server binary"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validateUnifiedReleaseMetadata(packagemaker.Metadata{Files: test.files})
			if test.wantErr == "" && err != nil {
				t.Fatal(err)
			}
			if test.wantErr != "" && (err == nil || !strings.Contains(err.Error(), test.wantErr)) {
				t.Fatalf("error = %v, want %q", err, test.wantErr)
			}
		})
	}
}

func TestVerifiedReleaseCommandUsesExplicitModes(t *testing.T) {
	root := t.TempDir()
	dir := writeReleaseTree(t, root, "release-command")
	for _, test := range []struct{ kind, mode string }{{kind: "server", mode: "serve"}, {kind: "migrate", mode: "migrate"}} {
		command, err := verifiedReleaseCommand(dir, test.kind)
		if err != nil {
			t.Fatal(err)
		}
		want := []string{releaseServerBin(dir), test.mode}
		if strings.Join(command.argv(), "\x00") != strings.Join(want, "\x00") {
			t.Fatalf("%s argv = %v, want %v", test.kind, command.argv(), want)
		}
	}
}
