//go:build linux || darwin

package filesystem

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"bansoko/internal/library/domain"
)

type publicationRun struct {
	steps    []string
	receipts []domain.PublicationStepReceipt
	join     domain.PublicationJoin
}

func (r *publicationRun) callbacks(t *testing.T, deny string) domain.TypedPublicationCallbacks {
	t.Helper()
	return domain.TypedPublicationCallbacks{
		StartDispatch: func(context.Context, domain.PublicationSession) (bool, error) { return true, nil },
		StartStep: func(_ context.Context, _ domain.PublicationSession, step string) (bool, error) {
			r.steps = append(r.steps, step)
			return step != deny, nil
		},
		RecordStep: func(_ context.Context, _ domain.PublicationSession, receipt domain.PublicationStepReceipt) error {
			r.receipts = append(r.receipts, receipt)
			return nil
		},
		Joined: func(_ context.Context, j domain.PublicationJoin) error { r.join = j; return nil },
	}
}

type publicationFixture struct {
	dir, source, target string
	data                []byte
	op                  domain.FileOperation
}

func newPublicationFixture(t *testing.T, kind domain.OperationKind, collision domain.CollisionPolicy) publicationFixture {
	t.Helper()
	dir := canonicalTemp(t)
	if err := os.MkdirAll(filepath.Join(dir, "downloads"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "library"), 0o755); err != nil {
		t.Fatal(err)
	}
	f := publicationFixture{dir: dir, source: filepath.Join(dir, "downloads", "ep01.mkv"), target: filepath.Join(dir, "library", "Show - 01.mkv"), data: []byte("episode bytes for path-level publication")}
	if err := os.WriteFile(f.source, f.data, 0o644); err != nil {
		t.Fatal(err)
	}
	f.op = domain.FileOperation{ID: "op-1", Operation: kind, CollisionPolicy: collision, SourcePath: f.source, TargetPath: f.target, ExpectedSizeKnown: true, ExpectedSizeBytes: int64(len(f.data))}
	return f
}

func runPublication(t *testing.T, executor OSFileExecutor, f publicationFixture, deny string) (*publicationRun, domain.PublicationPreparation, error) {
	t.Helper()
	prep, err := executor.PreparePublication(context.Background(), f.op, "permit-"+t.Name())
	if err != nil {
		return nil, prep, err
	}
	invocation, err := executor.NewPublicationInvocation()
	if err != nil {
		t.Fatal(err)
	}
	run := &publicationRun{}
	if f.op.Operation == domain.OperationHardlink && f.op.CollisionPolicy == domain.CollisionBlock {
		err = invocation.ExecuteHardlink(context.Background(), f.op, prep, run.callbacks(t, deny))
	} else {
		err = invocation.ExecuteCopy(context.Background(), f.op, prep, run.callbacks(t, deny))
	}
	return run, prep, err
}

func readString(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func absent(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Lstat(path)
	return errors.Is(err, os.ErrNotExist)
}

func inode(t *testing.T, path string) uint64 {
	t.Helper()
	o, err := observePath(path)
	if err != nil {
		t.Fatal(err)
	}
	return o.Identity.Inode
}

func TestPublicationMatrix(t *testing.T) {
	for _, kind := range []domain.OperationKind{domain.OperationCopy, domain.OperationMove, domain.OperationHardlink} {
		for _, collision := range []domain.CollisionPolicy{domain.CollisionBlock, domain.CollisionVersion, domain.CollisionOverwrite} {
			t.Run(string(kind)+"/"+string(collision), func(t *testing.T) {
				f := newPublicationFixture(t, kind, collision)
				if collision != domain.CollisionBlock {
					if err := os.WriteFile(f.target, []byte("old incumbent"), 0o644); err != nil {
						t.Fatal(err)
					}
				}
				sourceInode := inode(t, f.source)
				run, prep, err := runPublication(t, OSFileExecutor{}, f, "")
				if err != nil {
					t.Fatal(err)
				}
				final := f.target
				if collision == domain.CollisionVersion {
					final = versionedPath(f.target, 2)
					if readString(t, f.target) != "old incumbent" {
						t.Fatal("version replaced the occupied base target")
					}
				}
				if readString(t, final) != string(f.data) {
					t.Fatalf("final content %q", readString(t, final))
				}
				if !absent(t, prep.TempPath) {
					t.Fatal("temp left behind")
				}
				if kind == domain.OperationMove && !absent(t, f.source) {
					t.Fatal("move left the source")
				}
				if kind != domain.OperationMove && readString(t, f.source) != string(f.data) {
					t.Fatal("source changed")
				}
				if kind == domain.OperationHardlink && inode(t, final) != sourceInode {
					t.Fatal("hardlink target is not the source object")
				}
				if len(run.receipts) != len(run.join.CoveredSteps) {
					t.Fatalf("receipts=%d covered=%v", len(run.receipts), run.join.CoveredSteps)
				}
				for _, r := range run.receipts {
					if r.Err != nil {
						t.Fatalf("receipt %s: %v", r.Step, r.Err)
					}
					if r.Step == "publish_target" {
						if r.Target.SizeBytes != int64(len(f.data)) || r.TargetPath != final {
							t.Fatalf("publish receipt %+v", r)
						}
						if collision == domain.CollisionOverwrite && r.Displaced.SizeBytes != int64(len("old incumbent")) {
							t.Fatalf("overwrite displaced %+v", r.Displaced)
						}
						if collision == domain.CollisionVersion && r.Displaced.SizeBytes != 0 {
							t.Fatalf("version displaced %+v", r.Displaced)
						}
					}
				}
				entries, _ := os.ReadDir(filepath.Dir(f.target))
				for _, e := range entries {
					if strings.HasPrefix(e.Name(), ".bansoko-") {
						t.Fatalf("private entry left: %s", e.Name())
					}
				}
			})
		}
	}
}

func TestPublicationDeniedStepPreservesPrefix(t *testing.T) {
	for _, denied := range []string{"stage_temp", "publish_target", "temp_cleanup"} {
		t.Run(denied, func(t *testing.T) {
			f := newPublicationFixture(t, domain.OperationCopy, domain.CollisionBlock)
			run, prep, err := runPublication(t, OSFileExecutor{}, f, denied)
			if err == nil {
				t.Fatal("denied step executed")
			}
			if (denied == "stage_temp") != absent(t, prep.TempPath) && denied != "temp_cleanup" {
				t.Fatalf("temp state wrong for %s", denied)
			}
			if (denied == "temp_cleanup") == absent(t, f.target) {
				t.Fatalf("target state wrong for %s", denied)
			}
			if len(run.receipts) != len(run.join.CoveredSteps) {
				t.Fatalf("receipts=%d covered=%v", len(run.receipts), run.join.CoveredSteps)
			}
			if readString(t, f.source) != string(f.data) {
				t.Fatal("denied copy changed source")
			}
		})
	}
}

func TestPublicationHardlinkGrantAndJoin(t *testing.T) {
	for _, mode := range []string{"success", "dispatch_error", "dispatch_false", "step_false", "record_error", "deadline_expired"} {
		t.Run(mode, func(t *testing.T) {
			f := newPublicationFixture(t, domain.OperationHardlink, domain.CollisionBlock)
			executor := OSFileExecutor{}
			prep, err := executor.PreparePublication(context.Background(), f.op, "permit")
			if err != nil {
				t.Fatal(err)
			}
			invocation, err := executor.NewPublicationInvocation()
			if err != nil {
				t.Fatal(err)
			}
			var receipt domain.PublicationStepReceipt
			joined := false
			calls := 0
			callbacks := domain.TypedPublicationCallbacks{
				StartDispatch: func(context.Context, domain.PublicationSession) (bool, error) {
					calls++
					if mode == "dispatch_error" {
						return false, errors.New("uncertain commit")
					}
					return mode != "dispatch_false", nil
				},
				StartStep: func(context.Context, domain.PublicationSession, string) (bool, error) {
					calls++
					if !absent(t, f.target) {
						t.Fatal("mutation preceded grant")
					}
					return mode != "step_false", nil
				},
				CheckMutationDeadline: func(context.Context, domain.PublicationSession, string) error {
					if mode == "deadline_expired" {
						return context.DeadlineExceeded
					}
					return nil
				},
				RecordStep: func(_ context.Context, _ domain.PublicationSession, r domain.PublicationStepReceipt) error {
					calls++
					receipt = r
					if mode == "record_error" {
						return errors.New("receipt commit uncertain")
					}
					return nil
				},
				Joined: func(context.Context, domain.PublicationJoin) error { joined = true; return nil },
			}
			err = invocation.ExecuteHardlink(context.Background(), f.op, prep, callbacks)
			if (err == nil) != (mode == "success") {
				t.Fatalf("err=%v", err)
			}
			mutated := mode == "success" || mode == "record_error"
			if mutated == absent(t, f.target) {
				t.Fatalf("mutation=%v", mutated)
			}
			if mutated && (receipt.Err != nil || receipt.Target.Identity.Inode != prep.Source.Identity.Inode) {
				t.Fatalf("receipt %+v", receipt)
			}
			if mode != "dispatch_error" && mode != "dispatch_false" && !joined {
				t.Fatal("join not reported")
			}
			before := calls
			if invocation.ExecuteHardlink(context.Background(), f.op, prep, callbacks) == nil || calls != before {
				t.Fatal("invocation replayed")
			}
		})
	}
}

func TestPublicationPrepareRefusals(t *testing.T) {
	t.Run("source size differs from frozen size", func(t *testing.T) {
		f := newPublicationFixture(t, domain.OperationCopy, domain.CollisionBlock)
		f.op.ExpectedSizeBytes++
		_, err := (OSFileExecutor{}).PreparePublication(context.Background(), f.op, "permit")
		if domain.PublicationFailureCode(err) != domain.CodeSourceChanged {
			t.Fatalf("err=%v", err)
		}
	})
	t.Run("source final component is a symlink", func(t *testing.T) {
		f := newPublicationFixture(t, domain.OperationCopy, domain.CollisionBlock)
		link := filepath.Join(f.dir, "downloads", "link.mkv")
		if err := os.Symlink(f.source, link); err != nil {
			t.Fatal(err)
		}
		f.op.SourcePath = link
		if _, err := (OSFileExecutor{}).PreparePublication(context.Background(), f.op, "permit"); err == nil {
			t.Fatal("symlink source accepted")
		}
	})
	t.Run("target parent is a symlink", func(t *testing.T) {
		f := newPublicationFixture(t, domain.OperationCopy, domain.CollisionBlock)
		elsewhere := filepath.Join(f.dir, "elsewhere")
		if err := os.Mkdir(elsewhere, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(elsewhere, filepath.Join(f.dir, "linked")); err != nil {
			t.Fatal(err)
		}
		f.op.TargetPath = filepath.Join(f.dir, "linked", "Show - 01.mkv")
		if _, err := (OSFileExecutor{}).PreparePublication(context.Background(), f.op, "permit"); err == nil {
			t.Fatal("symlink parent accepted")
		}
		if err := (OSFileExecutor{}).EnsurePublicationParent("", filepath.Join(f.dir, "linked", "sub", "x.mkv")); err == nil {
			t.Fatal("B5b walked through a symlink")
		}
	})
	t.Run("overwrite target absent", func(t *testing.T) {
		f := newPublicationFixture(t, domain.OperationCopy, domain.CollisionOverwrite)
		_, err := (OSFileExecutor{}).PreparePublication(context.Background(), f.op, "permit")
		if domain.PublicationFailureCode(err) != domain.FileErrorCodeTargetOccupied {
			t.Fatalf("err=%v", err)
		}
	})
}

func TestPublicationBlockTargetOccupied(t *testing.T) {
	for _, kind := range []domain.OperationKind{domain.OperationCopy, domain.OperationHardlink} {
		t.Run(string(kind), func(t *testing.T) {
			f := newPublicationFixture(t, kind, domain.CollisionBlock)
			executor := OSFileExecutor{}
			prep, err := executor.PreparePublication(context.Background(), f.op, "permit")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(f.target, []byte("foreign"), 0o644); err != nil {
				t.Fatal(err)
			}
			invocation, _ := executor.NewPublicationInvocation()
			run := &publicationRun{}
			if kind == domain.OperationHardlink {
				err = invocation.ExecuteHardlink(context.Background(), f.op, prep, run.callbacks(t, ""))
			} else {
				err = invocation.ExecuteCopy(context.Background(), f.op, prep, run.callbacks(t, ""))
			}
			if domain.PublicationFailureCode(err) != domain.FileErrorCodeTargetOccupied {
				t.Fatalf("err=%v", err)
			}
			if readString(t, f.target) != "foreign" {
				t.Fatal("occupied target replaced")
			}
		})
	}
}

func TestPublicationOverwriteTargetChangedSinceFreeze(t *testing.T) {
	f := newPublicationFixture(t, domain.OperationCopy, domain.CollisionOverwrite)
	if err := os.WriteFile(f.target, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	executor := OSFileExecutor{}
	prep, err := executor.PreparePublication(context.Background(), f.op, "permit")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.target, []byte("a different size"), 0o644); err != nil {
		t.Fatal(err)
	}
	invocation, _ := executor.NewPublicationInvocation()
	run := &publicationRun{}
	err = invocation.ExecuteCopy(context.Background(), f.op, prep, run.callbacks(t, ""))
	if domain.PublicationFailureCode(err) != domain.FileErrorCodeTargetOccupied {
		t.Fatalf("err=%v", err)
	}
	if readString(t, f.target) != "a different size" {
		t.Fatal("changed target replaced")
	}
}

// A foreign object swapped into the permit temp before publication is never
// published: the temp must still be the object this invocation staged. The
// foreign object survives in place and the failure carries no terminal code.
func TestPublicationRefusesForeignStagedObject(t *testing.T) {
	for _, collision := range []domain.CollisionPolicy{domain.CollisionBlock, domain.CollisionOverwrite} {
		t.Run(string(collision), func(t *testing.T) {
			f := newPublicationFixture(t, domain.OperationCopy, collision)
			if collision == domain.CollisionOverwrite {
				if err := os.WriteFile(f.target, []byte("old"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			executor := OSFileExecutor{}
			executor.Hooks.BeforePublish = func(temp, _ string) error {
				foreign := temp + ".foreign"
				if err := os.WriteFile(foreign, f.data, 0o644); err != nil {
					return err
				}
				return os.Rename(foreign, temp)
			}
			_, prep, err := runPublication(t, executor, f, "")
			if err == nil || domain.TerminalPublicationCode(domain.PublicationFailureCode(err)) {
				t.Fatalf("foreign staged object published or failed terminally: %v", err)
			}
			if readString(t, prep.TempPath) != string(f.data) {
				t.Fatal("foreign staged object not left in place")
			}
			if collision == domain.CollisionOverwrite && readString(t, f.target) != "old" {
				t.Fatal("overwrite target replaced by a foreign object")
			}
			if collision == domain.CollisionBlock && !absent(t, f.target) {
				t.Fatal("block target published from a foreign object")
			}
		})
	}
}

// fakeSys wraps the real syscalls with injected errno results.
type fakeSys struct {
	creates, links, renames int
	link                    func(from, to verifiedEntry) error
	renameNoReplace         func(from, to verifiedEntry) error
	rename                  func(from, to verifiedEntry) error
	create                  func(entry verifiedEntry) (*os.File, error)
}

func (f *fakeSys) executor() OSFileExecutor {
	real := defaultPublicationSyscalls()
	sys := publicationSyscalls{
		link: func(from, to verifiedEntry) error {
			f.links++
			if f.link != nil {
				return f.link(from, to)
			}
			return real.link(from, to)
		},
		renameNoReplace: func(from, to verifiedEntry) error {
			f.renames++
			if f.renameNoReplace != nil {
				return f.renameNoReplace(from, to)
			}
			return real.renameNoReplace(from, to)
		},
		rename: func(from, to verifiedEntry) error {
			f.renames++
			if f.rename != nil {
				return f.rename(from, to)
			}
			return real.rename(from, to)
		},
		create: func(entry verifiedEntry) (*os.File, error) {
			f.creates++
			if f.create != nil {
				return f.create(entry)
			}
			return real.create(entry)
		},
	}
	return OSFileExecutor{publicationSys: &sys}
}

func TestPublicationHardlinkErrnoMappingNeverFallsBack(t *testing.T) {
	cases := []struct {
		name  string
		errno syscall.Errno
		probe syscall.Errno // result of the capability probe link; 0 = supported
		code  string
	}{
		{"cross device", syscall.EXDEV, 0, domain.CodeHardlinkCrossDevice},
		{"unsupported", syscall.EOPNOTSUPP, 0, domain.CodeHardlinkUnsupported},
		{"refused on a filesystem with links", syscall.EPERM, 0, domain.CodeHardlinkNotPermitted},
		{"refused on a filesystem without links", syscall.EPERM, syscall.EPERM, domain.CodeHardlinkUnsupported},
		{"link limit", syscall.EMLINK, 0, domain.CodeHardlinkLimit},
	}
	for _, collision := range []domain.CollisionPolicy{domain.CollisionBlock, domain.CollisionVersion} {
		for _, tc := range cases {
			t.Run(string(collision)+"/"+tc.name, func(t *testing.T) {
				hardlinkProbeCache.Range(func(k, _ any) bool { hardlinkProbeCache.Delete(k); return true })
				f := newPublicationFixture(t, domain.OperationHardlink, collision)
				if collision == domain.CollisionVersion {
					if err := os.WriteFile(f.target, []byte("occupied"), 0o644); err != nil {
						t.Fatal(err)
					}
				}
				fake := &fakeSys{}
				real := defaultPublicationSyscalls()
				fake.link = func(from, to verifiedEntry) error {
					if strings.HasPrefix(from.name, ".bansoko-link-probe-") {
						if tc.probe != 0 {
							return tc.probe
						}
						return real.link(from, to)
					}
					return tc.errno
				}
				run, prep, err := runPublication(t, fake.executor(), f, "")
				if domain.PublicationFailureCode(err) != tc.code {
					t.Fatalf("err=%v want %s", err, tc.code)
				}
				if readString(t, f.source) != string(f.data) {
					t.Fatal("source touched")
				}
				if !absent(t, prep.TempPath) {
					t.Fatal("a temp was created: copy fallback attempted")
				}
				// Only the capability probe may create a file; no copy staging.
				if fake.creates > 1 || (tc.errno != syscall.EPERM && fake.creates != 0) {
					t.Fatalf("creates=%d: copy fallback attempted", fake.creates)
				}
				for _, step := range run.steps {
					if step == "reserve_version" || step == "publish_target" && collision != domain.CollisionBlock {
						t.Fatalf("continued to %s after a hardlink failure", step)
					}
				}
				if collision == domain.CollisionBlock && !absent(t, f.target) {
					t.Fatal("target created")
				}
				entries, _ := os.ReadDir(filepath.Dir(f.target))
				for _, e := range entries {
					if strings.HasPrefix(e.Name(), ".bansoko-") {
						t.Fatalf("probe or temp left: %s", e.Name())
					}
				}
			})
		}
	}
}

func TestPublicationAbsentTargetLadder(t *testing.T) {
	real := defaultPublicationSyscalls()
	cases := []struct {
		name            string
		renameNoReplace func(from, to verifiedEntry) error
		link            func(from, to verifiedEntry) error
		rename          func(from, to verifiedEntry) error
		code            string
	}{
		{name: "renameat2 supported"},
		{name: "NFS: RENAME_NOREPLACE EINVAL uses link", renameNoReplace: func(verifiedEntry, verifiedEntry) error { return syscall.EINVAL }},
		{name: "no hardlinks: check then rename", renameNoReplace: func(verifiedEntry, verifiedEntry) error { return syscall.EINVAL }, link: func(verifiedEntry, verifiedEntry) error { return syscall.EPERM }},
		{name: "NFS retransmit: rename landed but reported EEXIST", renameNoReplace: func(from, to verifiedEntry) error {
			if err := real.renameNoReplace(from, to); err != nil {
				return err
			}
			return syscall.EEXIST
		}},
		{name: "NFS retransmit: link landed but reported EEXIST", renameNoReplace: func(verifiedEntry, verifiedEntry) error { return syscall.EINVAL }, link: func(from, to verifiedEntry) error {
			if err := real.link(from, to); err != nil {
				return err
			}
			return syscall.EEXIST
		}},
		{name: "NFS retransmit: rename landed but reported ENOENT", renameNoReplace: func(verifiedEntry, verifiedEntry) error { return syscall.ENOSYS }, link: func(verifiedEntry, verifiedEntry) error { return syscall.EOPNOTSUPP }, rename: func(from, to verifiedEntry) error {
			if err := real.rename(from, to); err != nil {
				return err
			}
			return syscall.ENOENT
		}},
		{name: "name too long", renameNoReplace: func(verifiedEntry, verifiedEntry) error { return syscall.ENAMETOOLONG }, code: domain.CodeTargetNameTooLong},
		{name: "name invalid on SMB/exFAT", renameNoReplace: func(verifiedEntry, verifiedEntry) error { return syscall.EINVAL }, link: func(verifiedEntry, verifiedEntry) error { return syscall.EINVAL }, code: domain.CodeTargetNameInvalid},
		{name: "permission denied", renameNoReplace: func(verifiedEntry, verifiedEntry) error { return syscall.EACCES }, code: domain.CodeTargetPermissionDenied},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newPublicationFixture(t, domain.OperationCopy, domain.CollisionBlock)
			fake := &fakeSys{renameNoReplace: tc.renameNoReplace, link: tc.link, rename: tc.rename}
			_, prep, err := runPublication(t, fake.executor(), f, "")
			if tc.code != "" {
				if domain.PublicationFailureCode(err) != tc.code {
					t.Fatalf("err=%v want %s", err, tc.code)
				}
				if !absent(t, f.target) {
					t.Fatal("target created on a failed publish")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if readString(t, f.target) != string(f.data) || !absent(t, prep.TempPath) {
				t.Fatal("ladder did not publish exactly once")
			}
		})
	}
}

func TestPublicationTempCreatePermissionDenied(t *testing.T) {
	f := newPublicationFixture(t, domain.OperationCopy, domain.CollisionBlock)
	fake := &fakeSys{create: func(verifiedEntry) (*os.File, error) { return nil, syscall.EACCES }}
	_, _, err := runPublication(t, fake.executor(), f, "")
	if domain.PublicationFailureCode(err) != domain.CodeTargetPermissionDenied {
		t.Fatalf("err=%v", err)
	}
}

// Crash recovery per step: each case puts the on-disk layout a crash (or an
// NFS false error) can leave and checks that path + size alone decides
// completed, re-publish or uncertain.
func TestPublicationLayoutsConvergeByPathAndSize(t *testing.T) {
	type layout struct {
		temp, target string // "" = absent
	}
	staged := "staged bytes"
	cases := []struct {
		step   string
		layout layout
		want   string // completed | republished | uncertain
	}{
		{"publish_target", layout{temp: staged}, "republished"},
		{"publish_target", layout{target: staged}, "completed"},
		{"publish_target", layout{temp: staged, target: "wrong size!"}, "uncertain"},
		{"publish_target", layout{target: "wrong size!"}, "uncertain"},
		{"temp_cleanup", layout{temp: staged, target: staged}, "completed"},
		{"temp_cleanup", layout{target: staged}, "completed"},
		{"temp_cleanup", layout{temp: "other size", target: staged}, "uncertain"},
		{"source_cleanup", layout{temp: staged}, "completed"},
		{"source_cleanup", layout{}, "completed"},
		{"source_cleanup", layout{temp: "changed!"}, "uncertain"},
	}
	for _, tc := range cases {
		t.Run(tc.step+"/"+tc.layout.temp+"|"+tc.layout.target, func(t *testing.T) {
			dir := canonicalTemp(t)
			temp, target := filepath.Join(dir, ".bansoko-permit-x"), filepath.Join(dir, "final.mkv")
			write := func(path, content string) {
				if content != "" {
					if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
						t.Fatal(err)
					}
				}
			}
			write(temp, tc.layout.temp)
			write(target, tc.layout.target)
			parent, err := openVerifiedDirectory("", dir, false)
			if err != nil {
				t.Fatal(err)
			}
			defer parent.Close()
			expected := domain.PathObservation{SizeBytes: int64(len(staged))}
			if o, err := observePath(temp); err == nil {
				expected.Identity = o.Identity
			}
			sys := defaultPublicationSyscalls()
			var got string
			switch tc.step {
			case "publish_target":
				if tc.layout.temp == "" {
					// The temp is gone: the publish happened iff final has the staged size.
					o, err := observePath(target)
					if err == nil && o.SizeBytes == expected.SizeBytes {
						got = "completed"
					} else {
						got = "uncertain"
					}
					break
				}
				tempEntry := verifiedEntry{parent: parent, name: filepath.Base(temp), canonical: temp}
				finalEntry := verifiedEntry{parent: parent, name: filepath.Base(target), canonical: target}
				if err := sys.publishAbsent(tempEntry, finalEntry, expected); err != nil {
					got = "uncertain"
				} else {
					got = "republished"
				}
			case "temp_cleanup", "source_cleanup":
				if err := sys.removePublicationObject(context.Background(), temp, expected, tc.layout.temp != "", parent.identity, nil); err != nil {
					got = "uncertain"
				} else {
					got = "completed"
				}
			}
			if got != tc.want {
				t.Fatalf("got %s want %s", got, tc.want)
			}
			if got == "uncertain" && tc.layout.target != "" && readString(t, target) != tc.layout.target {
				t.Fatal("uncertain layout was mutated")
			}
		})
	}
}

func TestObserverSameDevice(t *testing.T) {
	dir := canonicalTemp(t)
	source := filepath.Join(dir, "a", "src.mkv")
	if err := os.MkdirAll(filepath.Dir(source), 0o755); err != nil {
		t.Fatal(err)
	}
	same, err := (OSFileObserver{}).SameDevice(context.Background(), source, filepath.Join(dir, "missing", "deeper", "t.mkv"))
	if err != nil || !same {
		t.Fatalf("same=%v err=%v", same, err)
	}
}

func TestObservePathRefusesSymlinkAndNonRegular(t *testing.T) {
	dir := canonicalTemp(t)
	file := filepath.Join(dir, "f")
	if err := os.WriteFile(file, []byte("12345"), 0o644); err != nil {
		t.Fatal(err)
	}
	if o, err := observePath(file); err != nil || o.SizeBytes != 5 || o.Identity.Inode == 0 {
		t.Fatalf("o=%+v err=%v", o, err)
	}
	link := filepath.Join(dir, "l")
	if err := os.Symlink(file, link); err != nil {
		t.Fatal(err)
	}
	if _, err := observePath(link); err == nil {
		t.Fatal("symlink accepted")
	}
	if _, err := observePath(dir); err == nil {
		t.Fatal("directory accepted")
	}
	if _, err := observePath(filepath.Join(dir, "absent")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("absent err=%v", err)
	}
}
