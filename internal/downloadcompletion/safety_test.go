package downloadcompletion

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testHash = "0123456789abcdef0123456789abcdef01234567"

type layout struct {
	root, save, library, content string
}

// newLayout builds root/downloads/Show with two videos and an nfo in a
// sub-folder, and an empty root/library.
func newLayout(t *testing.T) layout {
	t.Helper()
	root := t.TempDir()
	// macOS t.TempDir sits behind /var -> /private/var; resolve once so the
	// "lexical" paths below are the real ones unless a test builds a symlink.
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	l := layout{root: real, save: filepath.Join(real, "downloads"), library: filepath.Join(real, "library")}
	l.content = filepath.Join(l.save, "Show")
	for _, dir := range []string{l.library, filepath.Join(l.content, "extra")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"Show/ep1.mkv", "Show/ep2.mkv", "Show/extra/x.nfo"} {
		if err := os.WriteFile(filepath.Join(l.save, filepath.FromSlash(name)), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return l
}

func (l layout) input(paths ...string) SafetyInput {
	return SafetyInput{
		Hash: testHash,
		Torrent: Torrent{
			Hash: strings.ToUpper(testHash), SavePath: l.save + "/", ContentPath: l.content,
			FileNames: []string{"Show/ep1.mkv", "Show/ep2.mkv", "Show/extra/x.nfo"},
		},
		RemoteRoot: l.save, ApplicationRoot: l.save,
		OtherTasks: func() ([]Torrent, error) { return nil, nil },
		EachLibraryPath: func(visit func(string) error) error {
			for _, path := range paths {
				if err := visit(path); err != nil {
					return err
				}
			}
			return nil
		},
	}
}

func wantCode(t *testing.T, err error, code string) {
	t.Helper()
	var safety *SafetyError
	if !errors.As(err, &safety) {
		t.Fatalf("error = %v, want SafetyError %s", err, code)
	}
	if safety.Code != code {
		t.Fatalf("code = %s (%s), want %s", safety.Code, safety.Detail, code)
	}
}

func TestRemoveWithDataAllowsLibraryOutsideTheContent(t *testing.T) {
	l := newLayout(t)
	library := filepath.Join(l.library, "Show", "S01E01.mkv")
	// A hard link or copy placed in the library is a different entry.
	if err := os.MkdirAll(filepath.Dir(library), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(l.content, "ep1.mkv"), library); err != nil {
		t.Fatal(err)
	}
	if err := CheckRemoveWithData(l.input(library, filepath.Join(l.library, "gone", "S01E02.mkv"))); err != nil {
		t.Fatalf("clean layout refused: %v", err)
	}
}

func TestRemoveWithDataRefusesLibraryPathInsideTheContent(t *testing.T) {
	l := newLayout(t)
	cases := map[string]string{
		"equals a task file":                filepath.Join(l.content, "ep1.mkv"),
		"equals a task file in a subfolder": filepath.Join(l.content, "extra", "x.nfo"),
		"new file inside the content dir":   filepath.Join(l.content, "S01E03.mkv"),
		"deep inside the content dir":       filepath.Join(l.content, "extra", "deep", "a.mkv"),
		"differs only by case":              filepath.Join(l.save, "show", "EP1.MKV"),
	}
	for name, path := range cases {
		t.Run(name, func(t *testing.T) {
			wantCode(t, CheckRemoveWithData(l.input(path)), CodePlacementInsideContent)
		})
	}
}

func TestRemoveWithDataFollowsSymlinks(t *testing.T) {
	l := newLayout(t)
	link := filepath.Join(l.library, "linked")
	if err := os.Symlink(l.content, link); err != nil {
		t.Fatal(err)
	}
	// The library path looks harmless but its directory is the task content.
	wantCode(t, CheckRemoveWithData(l.input(filepath.Join(link, "ep1.mkv"))), CodePlacementInsideContent)
	wantCode(t, CheckRemoveWithData(l.input(filepath.Join(link, "brand-new.mkv"))), CodePlacementInsideContent)

	// A symlink entry in the library that points at a task file is its own
	// entry: qBittorrent deleting the file leaves the link, so it is allowed.
	entry := filepath.Join(l.library, "ep1.link.mkv")
	if err := os.Symlink(filepath.Join(l.content, "ep1.mkv"), entry); err != nil {
		t.Fatal(err)
	}
	if err := CheckRemoveWithData(l.input(entry)); err != nil {
		t.Fatalf("a symlink entry beside the content was refused: %v", err)
	}

	// A dangling directory link cannot be resolved: fail closed.
	dangling := filepath.Join(l.library, "dangling")
	if err := os.Symlink(filepath.Join(l.root, "missing"), dangling); err != nil {
		t.Fatal(err)
	}
	wantCode(t, CheckRemoveWithData(l.input(filepath.Join(dangling, "a.mkv"))), CodePathUnresolvable)
}

func TestRemoveWithDataIdentityAndFileList(t *testing.T) {
	l := newLayout(t)
	mismatch := map[string]func(*SafetyInput){
		"other hash":     func(in *SafetyInput) { in.Torrent.Hash = strings.Repeat("a", 40) },
		"empty hash":     func(in *SafetyInput) { in.Hash = ""; in.Torrent.Hash = "" },
		"other savepath": func(in *SafetyInput) { in.Torrent.SavePath = l.root },
	}
	for name, mutate := range mismatch {
		t.Run(name, func(t *testing.T) {
			in := l.input()
			mutate(&in)
			wantCode(t, CheckRemoveWithData(in), CodeQBTaskMismatch)
		})
	}
	invalid := map[string][]string{
		"parent segment": {"Show/../../etc/passwd"},
		"absolute":       {"/etc/passwd"},
		"empty segment":  {"Show//ep1.mkv"},
		"empty name":     {""},
		"control":        {"Show/ep\n1.mkv"},
	}
	for name, files := range invalid {
		t.Run("file list "+name, func(t *testing.T) {
			in := l.input()
			in.Torrent.FileNames = files
			wantCode(t, CheckRemoveWithData(in), CodeFileListInvalid)
		})
	}
	t.Run("empty file list", func(t *testing.T) {
		in := l.input()
		in.Torrent.FileNames = nil
		wantCode(t, CheckRemoveWithData(in), CodeContentUnresolved)
	})
	t.Run("no content path", func(t *testing.T) {
		in := l.input()
		in.Torrent.ContentPath = ""
		wantCode(t, CheckRemoveWithData(in), CodeContentUnresolved)
	})
	t.Run("content path outside the save root", func(t *testing.T) {
		in := l.input()
		in.Torrent.ContentPath = l.library
		wantCode(t, CheckRemoveWithData(in), CodeContentOutsideRoot)
	})
	t.Run("a file outside the content path", func(t *testing.T) {
		in := l.input()
		in.Torrent.FileNames = []string{"Show/ep1.mkv", "Other/ep2.mkv"}
		wantCode(t, CheckRemoveWithData(in), CodeContentUnresolved)
	})
	t.Run("relative frozen root", func(t *testing.T) {
		in := l.input()
		in.RemoteRoot = "downloads"
		wantCode(t, CheckRemoveWithData(in), CodeContentUnresolved)
	})
}

func TestRemoveWithDataLibraryPathValidation(t *testing.T) {
	l := newLayout(t)
	for name, path := range map[string]string{
		"relative":       "library/Show/a.mkv",
		"empty":          "",
		"parent segment": l.library + "/../downloads/Show/ep1.mkv",
		"control":        l.library + "/a\nb.mkv",
	} {
		t.Run(name, func(t *testing.T) {
			wantCode(t, CheckRemoveWithData(l.input(path)), CodePlacementPathInvalid)
		})
	}
	t.Run("no way to list library paths", func(t *testing.T) {
		in := l.input()
		in.EachLibraryPath = nil
		wantCode(t, CheckRemoveWithData(in), CodePlacementPathInvalid)
	})
	t.Run("the walk error is returned", func(t *testing.T) {
		in := l.input()
		boom := errors.New("database went away")
		in.EachLibraryPath = func(func(string) error) error { return boom }
		if err := CheckRemoveWithData(in); !errors.Is(err, boom) {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestRemoveWithDataNoSubfolderLayoutOnlyProtectsTheTaskFiles(t *testing.T) {
	l := newLayout(t)
	// "Don't create subfolder": the content path is the save root itself and
	// the files sit directly in it. Another file there is not the task's.
	for _, name := range []string{"a.mkv", "other.mkv"} {
		if err := os.WriteFile(filepath.Join(l.save, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	in := l.input(filepath.Join(l.save, "other.mkv"))
	in.Torrent.ContentPath = l.save
	in.Torrent.FileNames = []string{"a.mkv"}
	if err := CheckRemoveWithData(in); err != nil {
		t.Fatalf("a foreign file in the shared save root was refused: %v", err)
	}
	in = l.input(filepath.Join(l.save, "a.mkv"))
	in.Torrent.ContentPath = l.save
	in.Torrent.FileNames = []string{"a.mkv"}
	wantCode(t, CheckRemoveWithData(in), CodePlacementInsideContent)
}

func TestRemoveWithDataSingleFileTorrent(t *testing.T) {
	l := newLayout(t)
	file := filepath.Join(l.save, "movie.mkv")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	in := l.input(filepath.Join(l.library, "Movie", "Movie.mkv"))
	in.Torrent.ContentPath = file
	in.Torrent.FileNames = []string{"movie.mkv"}
	if err := CheckRemoveWithData(in); err != nil {
		t.Fatalf("single-file torrent refused: %v", err)
	}
	in = l.input(file)
	in.Torrent.ContentPath = file
	in.Torrent.FileNames = []string{"movie.mkv"}
	wantCode(t, CheckRemoveWithData(in), CodePlacementInsideContent)
}

func TestRemoveWithDataTranslatesTheFrozenRoots(t *testing.T) {
	l := newLayout(t)
	// qBittorrent saves under /downloads; this process sees the same tree at l.save.
	in := l.input(filepath.Join(l.content, "ep2.mkv"))
	in.RemoteRoot = "/downloads"
	in.ApplicationRoot = l.save
	in.Torrent.SavePath = "/downloads"
	in.Torrent.ContentPath = "/downloads/Show"
	wantCode(t, CheckRemoveWithData(in), CodePlacementInsideContent)

	in = l.input(filepath.Join(l.library, "S01E02.mkv"))
	in.RemoteRoot = "/downloads"
	in.ApplicationRoot = l.save
	in.Torrent.SavePath = "/downloads"
	in.Torrent.ContentPath = "/downloads/Show"
	if err := CheckRemoveWithData(in); err != nil {
		t.Fatalf("mapped layout refused: %v", err)
	}
}

func otherTask(hash, save, content string, files ...string) Torrent {
	return Torrent{Hash: hash, SavePath: save, ContentPath: content, FileNames: files}
}

const otherHash = "9999999999999999999999999999999999999999"

func TestRemoveWithDataRequiresOtherTasksToBeKnown(t *testing.T) {
	l := newLayout(t)
	in := l.input()
	in.OtherTasks = nil
	wantCode(t, CheckRemoveWithData(in), CodeOtherTasksUnreadable)

	boom := errors.New("qB list failed")
	in = l.input()
	in.OtherTasks = func() ([]Torrent, error) { return nil, boom }
	if err := CheckRemoveWithData(in); !errors.Is(err, boom) {
		t.Fatalf("a list that cannot be read must fail the check, got %v", err)
	}
}

func TestRemoveWithDataIgnoresOtherTasksElsewhere(t *testing.T) {
	l := newLayout(t)
	elsewhere := filepath.Join(l.root, "other-downloads")
	showTwo := filepath.Join(l.save, "Show Two")
	for _, task := range []Torrent{
		otherTask(otherHash, elsewhere, filepath.Join(elsewhere, "Show"), "Show/ep1.mkv"),
		// Same save root, different folder, even with the same file names.
		otherTask("8888888888888888888888888888888888888888", l.save, showTwo, "Show Two/ep1.mkv"),
		// Files directly in the save root that are not this task's.
		otherTask("7777777777777777777777777777777777777777", l.save, l.save, "loose.mkv"),
		// This task itself, listed under another spelling of its hash.
		otherTask(strings.ToUpper(testHash), l.save, l.content, "Show/ep1.mkv"),
	} {
		in := l.input()
		in.OtherTasks = func() ([]Torrent, error) { return []Torrent{task}, nil }
		if err := CheckRemoveWithData(in); err != nil {
			t.Fatalf("task %s in a disjoint place was refused: %v", task.Hash, err)
		}
	}
}

func TestRemoveWithDataRefusesWhenAnotherTaskSharesTheContent(t *testing.T) {
	l := newLayout(t)
	cases := map[string]Torrent{
		"same content folder (cross seed)": otherTask(otherHash, l.save, l.content, "Show/ep1.mkv"),
		"folder above ours":                otherTask(otherHash, l.root, l.save, "downloads/Show/ep1.mkv"),
		"folder inside ours":               otherTask(otherHash, l.save, filepath.Join(l.content, "extra"), "Show/extra/x.nfo"),
		"single file inside ours":          otherTask(otherHash, l.save, filepath.Join(l.content, "ep2.mkv"), "Show/ep2.mkv"),
		"loose file that is ours":          otherTask(otherHash, l.content, l.content, "ep1.mkv"),
		"loose file inside our folder":     otherTask(otherHash, l.content, l.content, "brand-new.mkv"),
		"differs only by case":             otherTask(otherHash, l.save, filepath.Join(l.save, "SHOW"), "SHOW/EP1.MKV"),
	}
	for name, task := range cases {
		t.Run(name, func(t *testing.T) {
			in := l.input()
			in.OtherTasks = func() ([]Torrent, error) { return []Torrent{task}, nil }
			wantCode(t, CheckRemoveWithData(in), CodeContentSharedWithOther)
		})
	}
	t.Run("through a symlinked folder", func(t *testing.T) {
		link := filepath.Join(l.root, "alias")
		if err := os.Symlink(l.content, link); err != nil {
			t.Fatal(err)
		}
		in := l.input()
		in.OtherTasks = func() ([]Torrent, error) {
			return []Torrent{otherTask(otherHash, l.root, link, "alias/ep1.mkv")}, nil
		}
		wantCode(t, CheckRemoveWithData(in), CodeContentSharedWithOther)
	})
	t.Run("loose files of the no-subfolder layout", func(t *testing.T) {
		in := l.input()
		in.Torrent.ContentPath = l.save
		in.Torrent.FileNames = []string{"a.mkv"}
		in.OtherTasks = func() ([]Torrent, error) {
			return []Torrent{otherTask(otherHash, l.save, l.save, "a.mkv", "b.mkv")}, nil
		}
		wantCode(t, CheckRemoveWithData(in), CodeContentSharedWithOther)
		in.OtherTasks = func() ([]Torrent, error) {
			return []Torrent{otherTask(otherHash, l.save, l.save, "b.mkv")}, nil
		}
		if err := CheckRemoveWithData(in); err != nil {
			t.Fatalf("a different loose file was refused: %v", err)
		}
	})
}

func TestRemoveWithDataRefusesAnUnreadableOtherTask(t *testing.T) {
	l := newLayout(t)
	for name, task := range map[string]Torrent{
		"no content path":                  otherTask(otherHash, l.save, ""),
		"no save path":                     otherTask(otherHash, "", l.content),
		"loose layout without a file list": otherTask(otherHash, l.save, l.save),
		"unsafe file name":                 otherTask(otherHash, l.save, l.save, "../escape.mkv"),
	} {
		t.Run(name, func(t *testing.T) {
			in := l.input()
			in.OtherTasks = func() ([]Torrent, error) { return []Torrent{task}, nil }
			wantCode(t, CheckRemoveWithData(in), CodeOtherTasksUnreadable)
		})
	}
}
