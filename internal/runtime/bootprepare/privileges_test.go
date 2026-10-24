package bootprepare

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// fakePrivileges runs the real filesystem walk but fakes identity, ownership
// and the privileged calls, so root behaviour is testable unprivileged.
type fakePrivileges struct {
	OSPrivilegeSystem
	euid, uid, gid int
	owners         map[string]uint32 // path -> reported owner uid (default: 0)
	writableErr    error
	setuidNoop     bool
	chowned        []string
	calls          []string
}

type ownedInfo struct {
	fs.FileInfo
	stat *syscall.Stat_t
}

func (o ownedInfo) Sys() any { return o.stat }

func (f *fakePrivileges) Geteuid() int { return f.euid }
func (f *fakePrivileges) Getuid() int  { return f.uid }
func (f *fakePrivileges) Getgid() int  { return f.gid }
func (f *fakePrivileges) Lstat(path string) (fs.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	stat := &syscall.Stat_t{}
	stat.Uid = f.owners[path]
	return ownedInfo{FileInfo: info, stat: stat}, nil
}
func (f *fakePrivileges) Lchown(path string, uid, gid int) error {
	f.chowned = append(f.chowned, path)
	return nil
}
func (f *fakePrivileges) Writable(string) error { return f.writableErr }
func (f *fakePrivileges) Setgroups(gids []int) error {
	f.calls = append(f.calls, "setgroups")
	return nil
}
func (f *fakePrivileges) Setgid(gid int) error {
	f.calls = append(f.calls, "setgid")
	if !f.setuidNoop {
		f.gid = gid
	}
	return nil
}
func (f *fakePrivileges) Setuid(uid int) error {
	f.calls = append(f.calls, "setuid")
	if !f.setuidNoop {
		f.uid, f.euid = uid, uid
	}
	return nil
}

func TestIDsFromEnvironment(t *testing.T) {
	env := func(values map[string]string) func(string) string {
		return func(key string) string { return values[key] }
	}
	if uid, gid, err := IDsFromEnvironment(env(nil)); err != nil || uid != 1000 || gid != 1000 {
		t.Fatalf("defaults = %d:%d %v, want 1000:1000", uid, gid, err)
	}
	if uid, gid, err := IDsFromEnvironment(env(map[string]string{"PUID": " 0 ", "PGID": "27"})); err != nil || uid != 0 || gid != 27 {
		t.Fatalf("explicit = %d:%d %v", uid, gid, err)
	}
	for _, bad := range []map[string]string{{"PUID": "abc"}, {"PGID": "-1"}, {"PUID": "1000x"}} {
		if _, _, err := IDsFromEnvironment(env(bad)); err == nil {
			t.Fatalf("malformed %v accepted", bad)
		}
	}
}

func TestEstablishPrivilegesAsRootDropsAfterPreparingTrees(t *testing.T) {
	base := t.TempDir()
	config := filepath.Join(base, "config")
	updates := filepath.Join(base, "updates")
	if err := os.MkdirAll(filepath.Join(updates, "releases", "r1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(updates, "releases", "r1", "bansoko-server"), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	rootData := filepath.Join(base, "data")
	userData := filepath.Join(base, "data-user")
	for _, dir := range []string{rootData, filepath.Join(rootData, "media"), userData} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	sys := &fakePrivileges{
		owners: map[string]uint32{userData: 1000},
	}
	result, err := EstablishPrivileges(sys, PrivilegeOptions{
		UID: 1000, GID: 1000,
		OwnedTrees:  []string{config, updates},
		MountPoints: []string{rootData, userData, filepath.Join(base, "absent")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != PrivilegesDropped {
		t.Fatalf("outcome = %v", result.Outcome)
	}
	if info, err := os.Stat(config); err != nil || !info.IsDir() {
		t.Fatalf("config dir was not created: %v", err)
	}
	chowned := strings.Join(sys.chowned, "\n")
	for _, want := range []string{config, updates, filepath.Join(updates, "releases", "r1", "bansoko-server"), rootData} {
		if !strings.Contains(chowned+"\n", want+"\n") {
			t.Fatalf("%s was not chowned; chowned:\n%s", want, chowned)
		}
	}
	for _, never := range []string{filepath.Join(rootData, "media"), userData} {
		if strings.Contains(chowned+"\n", never+"\n") {
			t.Fatalf("%s must not be chowned (mount contents / user-owned mount)", never)
		}
	}
	if strings.Join(sys.calls, ",") != "setgroups,setgid,setuid" {
		t.Fatalf("drop order = %v", sys.calls)
	}
}

func TestEstablishPrivilegesFailsClosedWhenDropDoesNotTakeEffect(t *testing.T) {
	sys := &fakePrivileges{setuidNoop: true}
	_, err := EstablishPrivileges(sys, PrivilegeOptions{UID: 1000, GID: 1000, OwnedTrees: []string{t.TempDir()}})
	if err == nil || !strings.Contains(err.Error(), "did not take effect") {
		t.Fatalf("err = %v", err)
	}
}

func TestEstablishPrivilegesPUIDZeroStaysRoot(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "config")
	sys := &fakePrivileges{}
	result, err := EstablishPrivileges(sys, PrivilegeOptions{UID: 0, GID: 0, OwnedTrees: []string{dir}, MountPoints: []string{t.TempDir()}})
	if err != nil || result.Outcome != PrivilegesStayRoot {
		t.Fatalf("result = %+v err=%v", result, err)
	}
	if len(sys.calls) != 0 || len(sys.chowned) != 0 {
		t.Fatalf("PUID=0 changed identity or ownership: calls=%v chowned=%v", sys.calls, sys.chowned)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatal("owned tree not created")
	}
}

func TestEstablishPrivilegesNonRootChangesNothing(t *testing.T) {
	existing := t.TempDir()
	sys := &fakePrivileges{euid: 1234, uid: 1234, gid: 1234}
	result, err := EstablishPrivileges(sys, PrivilegeOptions{UID: 1000, GID: 1000,
		OwnedTrees: []string{existing, filepath.Join(existing, "missing")}, MountPoints: []string{existing}})
	if err != nil || result.Outcome != PrivilegesUnprivileged {
		t.Fatalf("result = %+v err=%v", result, err)
	}
	if len(sys.calls) != 0 || len(sys.chowned) != 0 {
		t.Fatalf("non-root start changed identity or ownership: calls=%v chowned=%v", sys.calls, sys.chowned)
	}
	sys.writableErr = errors.New("permission denied")
	if _, err := EstablishPrivileges(sys, PrivilegeOptions{UID: 1000, GID: 1000, OwnedTrees: []string{existing}}); err == nil ||
		!strings.Contains(err.Error(), "not writable") {
		t.Fatalf("unwritable owned tree err = %v", err)
	}
}
