package eventactions

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"bansoko/internal/testutil/disposabledb"
)

func testRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func putScript(t *testing.T, root, name, source string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), []byte(source), mode); err != nil {
		t.Fatal(err)
	}
}

func TestPreflightNeverFollowsLinks(t *testing.T) {
	s := &Service{root: testRoot(t)}
	putScript(t, s.root, "ready.sh", "#!/bin/sh\nexit 0\n", 0700)
	putScript(t, s.root, "readonly.sh", "#!/bin/sh\nexit 0\n", 0600)
	putScript(t, s.root, "unknown.sh", "#!/no-such-interpreter\n", 0700)
	if err := os.Symlink("ready.sh", filepath.Join(s.root, "link.sh")); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"ready.sh": Ready, "readonly.sh": NoExecute, "unknown.sh": NoInterpreter, "never.sh": Missing, "link.sh": Unreadable, "../ready.sh": Unreadable} {
		file := s.File(name)
		if file.Check != want {
			t.Fatalf("%s check=%s want %s", name, file.Check, want)
		}
	}
	view := s.File("readonly.sh")
	if view.Source == "" || view.Interpreter != "/bin/sh" {
		t.Fatalf("unselectable script should remain readable: %+v", view)
	}
	list, err := s.Files()
	if err != nil || len(list) != 3 {
		t.Fatalf("directory view=%+v err=%v", list, err)
	}
}

func TestScriptInputIncludesOnlyRegisteredFacts(t *testing.T) {
	raw, err := scriptInput(invocation{Version: 1, Event: "file_operation_completed"}, []byte(`{
		"out.path":"/library/Example.mkv", "out.subtitles":["Example.ass"], "frt.res":"2160p",
		"release.title":"http://example.invalid/?token=private", "frt.invented":"hidden", "source_evidence":{}, "download.task":"not for this event"
	}`))
	if err != nil {
		t.Fatal(err)
	}
	var result invocation
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Facts) != 4 || result.Facts["frt.res"] != "2160p" || result.Facts["release.title"] != "[redacted]" || result.Facts["out.path"] != "/library/Example.mkv" {
		t.Fatalf("input = %s", raw)
	}
}

func TestEventActionsPostgres(t *testing.T) {
	if !canRun() {
		t.Skip("requires the unprivileged Linux execution identity")
	}
	if os.Getenv("BANSOKO_TEST_DATABASE_URL") == "" {
		t.Skip("requires disposable PostgreSQL")
	}
	db := disposabledb.ResetDisposableSchema(t)
	owner := disposabledb.Owner(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	s := New(db, nil, nil)
	s.root = testRoot(t)
	name := "after-import.sh"
	source := "#!/bin/sh\nIFS= read -r event || [ -n \"$event\" ]\ncase \"$event\" in *'\"out.subtitles\":[\"Example.ass\"]'*) exit 0;; *) exit 9;; esac\n"
	putScript(t, s.root, name, source, 0700)
	const kind = "file_operation_completed"
	admit := func(id, state string) {
		t.Helper()
		_, err := owner.ExecContext(ctx, `INSERT INTO notification_admissions (id,trigger_kind,source_owner_type,source_owner_id,source_owner_version,result_key,occurred_at,event_catalog_version,event_template_catalog_version,payload,state)
		VALUES($1,'file_operation_completed','file_operation',$1,1,'result',clock_timestamp(),'1','1','{"out.subtitles":["Example.ass"]}',$2)`, id, state)
		if err != nil {
			t.Fatal(err)
		}
	}
	admit("before-enable", "pending_target_resolution")
	binding, err := s.Save(ctx, Binding{Kind: kind, Script: &name, Enabled: true, TimeoutSeconds: 15})
	if err != nil || binding.Version != 1 {
		t.Fatalf("save=%+v %v", binding, err)
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	step := func(want bool) {
		t.Helper()
		got, err := s.step(ctx, conn)
		if err != nil || got != want {
			t.Fatalf("step=%v %v; want %v", got, err, want)
		}
	}
	admit("grouped", "pending_subtitle_completion")
	admit("subtitle", "resolved")
	step(false)
	if _, err := owner.ExecContext(ctx, `UPDATE notification_admissions SET state='pending_target_resolution' WHERE id='grouped'`); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.ExecContext(ctx, `UPDATE notification_admissions SET state='admission_failed' WHERE id='grouped'`); err != nil {
		t.Fatal(err)
	}
	step(true)
	step(false)
	if err := os.Remove(filepath.Join(s.root, name)); err != nil {
		t.Fatal(err)
	}
	admit("missing-first", "pending_target_resolution")
	admit("missing-second", "pending_target_resolution")
	step(true)
	step(true)
	step(false)
	warnings, err := s.Attempts(ctx, true)
	if err != nil || len(warnings) != 1 {
		t.Fatalf("missing warnings=%+v %v", warnings, err)
	}
	bindings, err := s.ListBindings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	index := slices.IndexFunc(bindings, func(b Binding) bool { return b.Kind == kind })
	if index < 0 || !bindings[index].Enabled || bindings[index].Check != Missing {
		t.Fatalf("binding lost after missing script: %+v", bindings)
	}
	putScript(t, s.root, name, source, 0700)
	if err := s.sweep(ctx, conn); err != nil {
		t.Fatal(err)
	}
	step(false)
	admit("later", "pending_target_resolution")
	step(true)
	if err := os.Remove(filepath.Join(s.root, name)); err != nil {
		t.Fatal(err)
	}
	admit("missing-again", "pending_target_resolution")
	step(true)
	attempts, err := s.Attempts(ctx, false)
	if err != nil || len(attempts) != 5 {
		t.Fatalf("attempts=%+v %v", attempts, err)
	}
	for i, expect := range []string{"missing", "completed", "missing", "missing", "completed"} {
		if attempts[i].State != expect {
			t.Fatalf("attempts=%+v", attempts)
		}
	}
	warnings, err = s.Attempts(ctx, true)
	if err != nil || len(warnings) != 2 {
		t.Fatalf("warnings after recovery=%+v %v", warnings, err)
	}
}
