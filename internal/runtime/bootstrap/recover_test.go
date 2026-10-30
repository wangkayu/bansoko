package bootstrap

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"testing"

	embeddedupdate "bansoko/internal/update/journal"
)

type fakeRecoveryExecutor struct {
	status map[string]any
	err    error
	calls  int
}

func (f *fakeRecoveryExecutor) Status(context.Context) (map[string]any, error) {
	f.calls++
	return f.status, f.err
}

func TestParseRecoveryCommandSupportsStatusOnly(t *testing.T) {
	parsed, err := parseRecoveryCommand([]string{"status"})
	if err != nil || parsed.Action != "status" {
		t.Fatalf("status parsed=%+v err=%v", parsed, err)
	}
	for _, args := range [][]string{nil, {"resume"}, {"abort"}, {"status", "--operation", "op"}} {
		if _, err := parseRecoveryCommand(args); err == nil {
			t.Fatalf("accepted retired recovery arguments %v", args)
		}
	}
}

func TestRunRecoverStatusReportsRollbackBlocked(t *testing.T) {
	fake := &fakeRecoveryExecutor{status: map[string]any{"verdict": "status", "rollback_blocked": true, "step": "rollback_blocked"}}
	factory := func(action, evidence string) (recoveryExecutor, func(), error) {
		if action != "status" || evidence != "" {
			t.Fatalf("factory action=%q evidence=%q", action, evidence)
		}
		return fake, func() {}, nil
	}
	var stdout, stderr bytes.Buffer
	if code := runRecoverCommandWithFactory(context.Background(), []string{"status"}, &stdout, &stderr, factory); code != recoverExitOK {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if fake.calls != 1 || payload["rollback_blocked"] != true || payload["step"] != string(embeddedupdate.StepRollbackBlocked) {
		t.Fatalf("calls=%d payload=%v", fake.calls, payload)
	}
}

func TestMaintenanceDatabaseURLIsTheOwnerConnection(t *testing.T) {
	t.Setenv("BANSOKO_DATABASE_URL", " postgres://owner/db ")
	t.Setenv("BANSOKO_RUNTIME_DATABASE_URL", "postgres://bansoko_app/db")
	if got := maintenanceDatabaseURL(); got != "postgres://owner/db" {
		t.Fatalf("owner DSN = %q", got)
	}
}

func TestDispatchPreservesServerMigrateAndRecoverRouting(t *testing.T) {
	for _, tc := range []struct {
		args     []string
		wantKind string
	}{
		{args: nil, wantKind: "server"},
		{args: []string{"server"}, wantKind: "server"},
		{args: []string{"migrate"}, wantKind: "migrate"},
	} {
		called := ""
		code := dispatchWithRunners(tc.args, nil, io.Discard, io.Discard,
			func(kind string) int { called = kind; return 17 },
			func(context.Context, []string, io.Writer, io.Writer) int { t.Fatal("recover runner called"); return 0 },
			func(context.Context, []string, io.Reader, io.Writer, io.Writer) int {
				t.Fatal("reset runner called")
				return 0
			})
		if code != 17 || called != tc.wantKind {
			t.Fatalf("dispatch %v exit=%d kind=%q", tc.args, code, called)
		}
	}

	var gotRecoverArgs []string
	code := dispatchWithRunners([]string{"recover", "status"}, nil, io.Discard, io.Discard,
		func(string) int { t.Fatal("server runner called"); return 0 },
		func(_ context.Context, args []string, _, _ io.Writer) int { gotRecoverArgs = args; return 23 },
		func(context.Context, []string, io.Reader, io.Writer, io.Writer) int {
			t.Fatal("reset runner called")
			return 0
		})
	if code != 23 || len(gotRecoverArgs) != 1 || gotRecoverArgs[0] != "status" {
		t.Fatalf("recover dispatch exit=%d args=%v", code, gotRecoverArgs)
	}
}

func TestDispatchRejectsLegacySubcommandArgumentsAndUnknownCommands(t *testing.T) {
	for _, args := range [][]string{{"server", "extra"}, {"migrate", "extra"}, {"unknown"}} {
		var stdout, stderr bytes.Buffer
		if code := dispatchWithRunners(args, nil, &stdout, &stderr,
			func(string) int { t.Fatal("legacy runner called"); return 0 },
			func(context.Context, []string, io.Writer, io.Writer) int { t.Fatal("recover runner called"); return 0 },
			func(context.Context, []string, io.Reader, io.Writer, io.Writer) int {
				t.Fatal("reset runner called")
				return 0
			}); code != recoverExitUsage {
			t.Fatalf("dispatch %v exit=%d", args, code)
		}
		if stdout.Len() != 0 || stderr.Len() == 0 {
			t.Fatalf("dispatch %v stdout=%q stderr=%q", args, stdout.String(), stderr.String())
		}
	}
}
