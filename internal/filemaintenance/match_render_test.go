package filemaintenance

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"bansoko/internal/postprocessing/naming"
)

func TestClassifyMatchRender(t *testing.T) {
	for name, tc := range map[string]struct {
		err   error
		code  string
		facts []string
	}{
		"missing facts keep their paths": {&missingFactsError{facts: []string{"media.resolution", "anime.group"}}, MatchRenderFactsMissing, []string{"media.resolution", "anime.group"}},
		"engine missing facts":           {&naming.Error{Code: naming.ErrCodeNamingFactMissing, Missing: []string{"media.resolution"}}, MatchRenderFactsMissing, []string{"media.resolution"}},
		"unknown vars":                   {&naming.Error{Code: naming.ErrCodeNamingVarsUnknown, Message: "vars"}, MatchRenderFactsMissing, nil},
		"unavailable evidence":           {renderFactsUnavailable("frozen source facts are invalid"), MatchRenderFactsMissing, nil},
		"anything else":                  {errors.New("template exploded"), MatchRenderTemplateInvalid, nil},
	} {
		t.Run(name, func(t *testing.T) {
			var typed *MatchRenderError
			if !errors.As(classifyMatchRender(errors.Join(errors.New("wrapped"), tc.err)), &typed) || typed.Code != tc.code || !reflect.DeepEqual(typed.Facts, tc.facts) {
				t.Fatalf("classified = %+v", typed)
			}
		})
	}
}

func TestMissingDirectoriesListsWhatIsAbsentParentsFirst(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "Show"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := missingDirectories(filepath.Join(root, "Show", "Season 01", "Extra", "ep.mkv"))
	want := []string{filepath.Join(root, "Show", "Season 01"), filepath.Join(root, "Show", "Season 01", "Extra")}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("missing = %v, %v", got, err)
	}
	if got, err := missingDirectories(filepath.Join(root, "Show", "ep.mkv")); err != nil || len(got) != 0 {
		t.Fatalf("an existing parent needs nothing: %v, %v", got, err)
	}
	if got, err := missingDirectories(filepath.Join(root, "ep.mkv")); err != nil || len(got) != 0 {
		t.Fatalf("the root itself needs nothing: %v, %v", got, err)
	}
}

func TestMissingDirectoriesRefusesSymlinksAndFiles(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "Linked")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "file"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, target := range map[string]string{
		"symlink": filepath.Join(root, "Linked", "Season 01", "ep.mkv"),
		"file":    filepath.Join(root, "file", "Season 01", "ep.mkv"),
	} {
		if got, err := missingDirectories(target); err == nil {
			t.Fatalf("%s must be refused, got %v", name, got)
		}
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Fatal("nothing is created outside the root")
	}
}

func TestCreatedDirectoriesKeepsOnlyThePlannedOnesThatExist(t *testing.T) {
	root := t.TempDir()
	made, absent := filepath.Join(root, "a"), filepath.Join(root, "b")
	if err := os.Mkdir(made, 0o755); err != nil {
		t.Fatal(err)
	}
	attempt, _ := json.Marshal(userOperationAttempt{OperationID: "op", Stage: "effect", CreateDirs: []string{made, absent}})
	if got := createdDirectories(attempt); !reflect.DeepEqual(got, []string{made}) {
		t.Fatalf("created = %v", got)
	}
	if got := createdDirectories(nil); got == nil || len(got) != 0 {
		t.Fatalf("no checkpoint created nothing, and reads as an empty list: %v", got)
	}
}
