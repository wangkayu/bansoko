package application

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/apperrors"
)

func selectionManifest(paths ...string) []acquisitiondomain.ReleaseFile {
	files := make([]acquisitiondomain.ReleaseFile, 0, len(paths))
	for _, path := range paths {
		files = append(files, acquisitiondomain.ReleaseFile{SourcePath: path})
	}
	return files
}

func requireFileSelectionInvalid(t *testing.T, err error, fragment string) {
	t.Helper()
	var business *apperrors.Business
	if !errors.As(err, &business) || business.Status != 422 || business.Code != "FILE_SELECTION_INVALID" {
		t.Fatalf("err = %v, want 422 FILE_SELECTION_INVALID", err)
	}
	if !strings.Contains(business.Message, fragment) {
		t.Fatalf("message %q does not mention %q", business.Message, fragment)
	}
}

func TestCanonicalOneShotFileSelection(t *testing.T) {
	if got, err := canonicalOneShotFileSelection(nil); err != nil || got != nil {
		t.Fatalf("nil selection = %v, %v; want the default", got, err)
	}
	got, err := canonicalOneShotFileSelection([]string{"Pack/b.mkv", `Pack\a.mkv`})
	if err != nil || !reflect.DeepEqual(got, []string{"Pack/a.mkv", "Pack/b.mkv"}) {
		t.Fatalf("canonical = %v, %v", got, err)
	}
	for name, input := range map[string][]string{
		"at least one": {},
		"blank":        {"  "},
		"twice":        {"Pack/a.mkv", `Pack\a.mkv`},
		"longer than":  {strings.Repeat("a", maxOneShotSelectedPathBytes+1)},
	} {
		_, err := canonicalOneShotFileSelection(input)
		requireFileSelectionInvalid(t, err, name)
	}
}

func TestResolveOneShotFileSelection(t *testing.T) {
	manifest := selectionManifest(
		"Pack/Show - 01.mkv", "Pack/Show - 01.chs.ass",
		"Pack/S2/Show - 01.mkv", "Pack/S2/Show - 01.chs.ass",
		"Pack/NCOP.mkv", "Pack/cover.jpg",
	)
	rules := animedomain.FileFilterRules{NotIn: "NCOP"}

	selection, err := resolveOneShotFileSelection([]string{"Pack/S2/Show - 01.chs.ass", "Pack/S2/Show - 01.mkv"}, manifest, rules)
	if err != nil || !reflect.DeepEqual(selection.Paths(), []string{"Pack/S2/Show - 01.chs.ass", "Pack/S2/Show - 01.mkv"}) {
		t.Fatalf("a deep video and its subtitle = %v, %v", selection, err)
	}
	if selection, err := resolveOneShotFileSelection(nil, manifest, rules); err != nil || selection != nil {
		t.Fatalf("nil = %v, %v; want the default", selection, err)
	}

	for fragment, paths := range map[string][]string{
		"not a file of this torrent": {"Pack/Show - 02.mkv"},
		"neither a video nor":        {"Pack/Show - 01.mkv", "Pack/cover.jpg"},
		"excluded by the file rules": {"Pack/NCOP.mkv"},
		"at least one video":         {"Pack/Show - 01.chs.ass"},
		"follows no selected video":  {"Pack/Show - 01.mkv", "Pack/S2/Show - 01.chs.ass"},
	} {
		_, err := resolveOneShotFileSelection(paths, manifest, rules)
		requireFileSelectionInvalid(t, err, fragment)
	}
}
