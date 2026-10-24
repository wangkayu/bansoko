//go:build linux

package application

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"bansoko/internal/library/domain"
)

// Check the complete controlled media directories after an interrupted commit.
// Path-level publication keeps no capture namespace: the only publication
// entries are the permit temp, the (reserved) final target and the source.
// No path existence is used to grant another action.
func assertPublicationPrefix(t *testing.T, op domain.FileOperation, p domain.PublicationPreparation, steps []string, ordinal int, isReceipt bool) {
	t.Helper()
	completed := ordinal - 1
	if isReceipt {
		completed = ordinal
	}
	if completed > len(steps) {
		completed = len(steps)
	}
	done := map[string]bool{}
	for _, step := range steps[:completed] {
		done[step] = true
	}
	expected := map[string]string{}
	if !done["source_cleanup"] {
		expected[op.SourcePath] = "source-content"
	}
	final := op.TargetPath
	if op.CollisionPolicy == domain.CollisionVersion {
		expected[op.TargetPath] = "occupied previous object"
		ext := filepath.Ext(final)
		final = final[:len(final)-len(ext)] + " (2)" + ext
		if done["reserve_version"] {
			expected[final] = ""
		}
	}
	if done["stage_temp"] {
		expected[p.TempPath] = "source-content"
	}
	if done["publish_target"] {
		// The staged temp was renamed onto the final path.
		expected[final] = "source-content"
		delete(expected, p.TempPath)
	}
	if done["temp_cleanup"] {
		delete(expected, p.TempPath)
	}
	assertMediaInventory(t, op, expected, "media prefix")
}

func assertCleanupFaultInventory(t *testing.T, op domain.FileOperation, _ domain.PublicationPreparation, point, publishedPath string) {
	t.Helper()
	expected := map[string]string{publishedPath: "source-content"}
	if op.CollisionPolicy == domain.CollisionVersion {
		expected[op.TargetPath] = "occupied previous object"
	}
	// The probe fires right before the source unlink, so the source is still
	// at its own path; the staged temp was renamed onto the target.
	expected[op.SourcePath] = "source-content"
	assertMediaInventory(t, op, expected, "cleanup "+point+" media inventory")
}

func assertMediaInventory(t *testing.T, op domain.FileOperation, expected map[string]string, label string) {
	t.Helper()
	roots := []string{filepath.Dir(op.SourcePath), filepath.Dir(op.TargetPath)}
	sort.Slice(roots, func(i, j int) bool { return len(roots[i]) < len(roots[j]) })
	scanned := []string{}
	actual := map[string]string{}
	for _, root := range roots {
		duplicate := false
		for _, prior := range scanned {
			rel, err := filepath.Rel(prior, root)
			if err == nil && (rel == "." || rel != ".." && len(rel) > 0 && rel[:1] != ".") {
				duplicate = true
			}
		}
		if duplicate {
			continue
		}
		scanned = append(scanned, root)
		if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if path == root || entry.IsDir() {
				return nil
			}
			bytes, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			actual[path] = string(bytes)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("%s: actual=%v expected=%v", label, actual, expected)
	}
}
