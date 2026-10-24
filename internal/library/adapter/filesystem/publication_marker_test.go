package filesystem

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPublicationMarkerRejectsSymlinkAndExclusiveReplacement(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "target")
	identity, err := WritePublicationMarker(target, "permit", []byte(`{"executor_final":false}`))
	if err != nil {
		t.Fatal(err)
	}
	if identity.Inode == 0 {
		t.Fatal("missing marker identity")
	}
	if _, err := WritePublicationMarker(target, "permit", []byte(`{}`)); err == nil {
		t.Fatal("replaced O_EXCL marker")
	}
	raw, got, err := ReadPublicationMarker(target, "permit")
	if err != nil || got.Inode != identity.Inode || string(raw) != `{"executor_final":false}` {
		t.Fatalf("read=%s identity=%+v err=%v", raw, got, err)
	}
	p, _ := PublicationMarkerPath(target, "symlink")
	if err := os.Symlink(filepath.Join(root, ".bansoko-publication-permit.json"), p); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadPublicationMarker(target, "symlink"); err == nil {
		t.Fatal("followed marker symlink")
	}
}
func TestPublicationRejectsNonCanonicalFrozenTarget(t *testing.T) {
	// Path normalization is not permission to change a frozen target.
	if _, err := PublicationMarkerPath("/library/../target", "permit"); err == nil {
		t.Fatal("accepted noncanonical target")
	}
	if _, err := PublicationMarkerPath("/library/target", "../permit"); err == nil {
		t.Fatal("accepted token path traversal")
	}
}
