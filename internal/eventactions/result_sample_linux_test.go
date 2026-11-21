//go:build event_script_sample

package eventactions

import (
	logapp "bansoko/internal/observability/application"
	"bansoko/internal/testutil/disposabledb"
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestEventActionReportsPostgres(t *testing.T) {
	if !canRun() || os.Getenv("BANSOKO_TEST_DATABASE_URL") == "" {
		t.Skip("requires disposable PostgreSQL and unprivileged Linux")
	}
	for _, tool := range []string{"curl", "jq"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Fatalf("sample requires %s: %v", tool, err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db := disposabledb.ResetDisposableSchema(t)
	owner := disposabledb.Owner(t)
	var output bytes.Buffer
	logger := logapp.NewTeedRuntimeLogger(&output)
	s := New(db, nil, logger)
	s.root = testRoot(t)
	const kind = "file_operation_completed"
	const name = "refresh-library.sh"
	var response atomic.Int32
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/Library/Refresh" || r.Header.Get("X-Emby-Token") != "DEMO_TOKEN" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		code := int(response.Load())
		w.WriteHeader(code)
		if code != 204 {
			_, _ = io.WriteString(w, "discarded_response_body")
		}
	}))
	defer mock.Close()
	source, err := os.ReadFile("testdata/refresh-library.sh")
	if err != nil {
		t.Fatal(err)
	}
	putScript(t, s.root, name, strings.Replace(string(source), "http://emby:8096", mock.URL, 1), 0700)
	bindings, err := s.ListBindings(ctx)
	if err != nil || len(bindings) != 3 {
		t.Fatalf("list bindings: %+v %v", bindings, err)
	}
	for _, binding := range bindings {
		if binding.RecordResult {
			t.Fatal("reporting must default to off")
		}
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	binding := Binding{Kind: kind, Script: &[]string{name}[0], Enabled: true, TimeoutSeconds: 15}
	for _, tc := range []struct {
		id           string
		record       bool
		status, exit int
		wantSummary  string
	}{
		{"report-on-success", true, 204, 0, "Emby 已接受媒体库刷新请求"},
		{"report-on-refused", true, 503, 1, "Emby 刷新请求返回非成功状态"},
		{"report-off", false, 204, 0, ""},
	} {
		binding.RecordResult = tc.record
		binding, err = s.Save(ctx, binding)
		if err != nil || binding.RecordResult != tc.record {
			t.Fatalf("save: %+v %v", binding, err)
		}
		response.Store(int32(tc.status))
		_, err = owner.ExecContext(ctx, `INSERT INTO notification_admissions (id,trigger_kind,source_owner_type,source_owner_id,source_owner_version,result_key,occurred_at,event_catalog_version,event_template_catalog_version,payload,state)
VALUES ($1,'file_operation_completed','file_operation',$1,1,'result',clock_timestamp(),'1','1','{}','pending_target_resolution')`, tc.id)
		if err != nil {
			t.Fatal(err)
		}
		if ok, err := s.step(ctx, conn); !ok || err != nil {
			t.Fatalf("step: %v %v", ok, err)
		}
		rows := logger.Tail().Entries(logapp.RuntimeLogAll, "", 1)
		wantHTTP := tc.status
		if !tc.record {
			wantHTTP = 0
		}
		if len(rows) != 1 || rows[0].ExitCode == nil || *rows[0].ExitCode != tc.exit || rows[0].HTTPStatus != wantHTTP || rows[0].ScriptSummary != tc.wantSummary || rows[0].ScriptName != name {
			t.Fatalf("%s: result = %+v, raw log = %s", tc.id, rows, output.String())
		}
	}
	if strings.Contains(output.String(), "DEMO_TOKEN") || strings.Contains(output.String(), "discarded_response_body") {
		t.Fatal("runtime log exposed request credentials or response body")
	}
}
