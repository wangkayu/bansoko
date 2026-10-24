package migrations

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const typedSyncSignature = "public.download_finish_sync(text,text,bigint,text,integer,text,jsonb,text,text,boolean,bigint,bigint,text,text,text,jsonb,jsonb)"
const oldSyncSignature = "public.download_finish_sync(text,text,bigint,text,integer,text,jsonb,text,text,boolean,timestamp with time zone,text,text,jsonb,jsonb)"

func TestDownloadFinishSyncHasSingleTypedSignature(t *testing.T) {
	db := installedDB(t)
	var newExists, oldExists bool
	var count int
	if err := db.QueryRow("SELECT to_regprocedure($1) IS NOT NULL,to_regprocedure($2) IS NOT NULL,(SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='public' AND p.proname='download_finish_sync')", typedSyncSignature, oldSyncSignature).Scan(&newExists, &oldExists, &count); err != nil {
		t.Fatal(err)
	}
	if !newExists || oldExists || count != 1 {
		t.Fatalf("signatures new=%v old=%v count=%d", newExists, oldExists, count)
	}
}

func TestDownloadFinishSyncRuntimeACL(t *testing.T) {
	owner, runtimeDB, _ := progressProbe(t, "typed-acl", nil, nil)
	ctx := context.Background()
	var publicExecute, runtimeExecute bool
	if err := owner.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_proc p, LATERAL aclexplode(COALESCE(p.proacl,acldefault('f',p.proowner))) a WHERE p.oid=to_regprocedure($1) AND a.grantee=0 AND a.privilege_type='EXECUTE'),has_function_privilege('bansoko_runtime',$1,'EXECUTE')`, typedSyncSignature).Scan(&publicExecute, &runtimeExecute); err != nil {
		t.Fatal(err)
	}
	if publicExecute || !runtimeExecute {
		t.Fatalf("ACL public=%v runtime=%v", publicExecute, runtimeExecute)
	}
	for _, db := range []*sql.DB{owner, runtimeDB} {
		var oldExists bool
		if err := db.QueryRowContext(ctx, "SELECT to_regprocedure($1) IS NOT NULL", oldSyncSignature).Scan(&oldExists); err != nil {
			t.Fatal(err)
		}
		if oldExists {
			t.Fatal("legacy 15-parameter sync entry still exists")
		}
	}
}

func typedObservationCall(db *sql.DB, p queuedParams, work string, observationError bool, complete, total, state any) ([]byte, error) {
	var raw []byte
	err := db.QueryRowContext(context.Background(), `SELECT download_finish_sync($1,$2,1,'progress-session',1,'downloading','{"external_id":"typed-object","provider_state":"pauseddl","content_complete_bytes":999,"payload_total_bytes":999}',NULL,'', $3,$4::bigint,$5::bigint,$6::text,'typed-next',NULL,NULL,NULL)`, p.TaskID, work, observationError, complete, total, state).Scan(&raw)
	return raw, err
}

func TestDownloadFinishSyncShapeRejectionHasZeroWriteSet(t *testing.T) {
	owner, runtimeDB, p := progressProbe(t, "typed-shape", nil, nil)
	work := armSyncClaim(t, owner, runtimeDB, p, "typed-shape-sync")
	snapshot := func() string {
		var raw string
		if err := owner.QueryRowContext(context.Background(), `SELECT jsonb_build_object('task',to_jsonb(t),'work',to_jsonb(w),'root',to_jsonb(a),'works',(SELECT jsonb_agg(to_jsonb(x) ORDER BY x.id) FROM work_item_history x WHERE x.acquisition_id=t.acquisition_id))::text FROM download_tasks t JOIN work_item_history w ON w.id=$2 JOIN acquisitions a ON a.id=t.acquisition_id WHERE t.id=$1`, p.TaskID, work).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		return raw
	}
	for _, v := range []struct {
		name                   string
		error                  bool
		complete, total, state any
	}{
		{"error-with-bytes", true, int64(0), nil, nil}, {"error-with-total", true, nil, int64(0), nil}, {"error-with-state", true, nil, nil, ""},
		{"null-state", false, nil, nil, nil}, {"uppercase-state", false, nil, nil, "pausedDL"}, {"leading-space", false, nil, nil, " pauseddl"}, {"trailing-space", false, nil, nil, "pauseddl "}, {"overlong-state", false, nil, nil, strings.Repeat("a", 129)}, {"non-ascii-state", false, nil, nil, "暂停"},
		{"negative-bytes", false, int64(-1), nil, "downloading"}, {"negative-total", false, nil, int64(-1), "downloading"}, {"bytes-above-total", false, int64(2), int64(1), "downloading"},
	} {
		t.Run(v.name, func(t *testing.T) {
			before := snapshot()
			_, err := typedObservationCall(runtimeDB, p, work, v.error, v.complete, v.total, v.state)
			if sqlState(err) != "23514" || adoptAcceptPGMessage(err) != "SYNC_OBSERVATION_SHAPE" {
				t.Fatalf("wrong rejection: %v", err)
			}
			if after := snapshot(); before != after {
				t.Fatal("rejected observation mutated task/ticket/work/root")
			}
		})
	}
}

func TestDownloadFinishSyncNullVersusZeroAndCorrelationIsNotDecisionInput(t *testing.T) {
	for _, zero := range []bool{false, true} {
		t.Run(map[bool]string{false: "absent", true: "zero"}[zero], func(t *testing.T) {
			owner, runtimeDB, p := progressProbe(t, "typed-presence", map[string]any{"stall_seconds": 1}, nil)
			arrangeProgress(t, owner, p.TaskID, nil, 1000, "1 hour")
			work := armSyncClaim(t, owner, runtimeDB, p, "typed-presence-sync")
			var bytes any
			if zero {
				bytes = int64(0)
			}
			raw, err := typedObservationCall(runtimeDB, p, work, false, bytes, int64(1000), "downloading")
			if err != nil {
				t.Fatal(err)
			}
			var reply struct {
				State string `json:"state"`
				Code  string `json:"reason_code"`
			}
			if err = json.Unmarshal(raw, &reply); err != nil {
				t.Fatal(err)
			}
			want := "DOWNLOAD_PROGRESS_UNVERIFIABLE"
			if zero {
				want = "DOWNLOAD_STALLED"
			}
			_, code, mark, _, _, _ := readProgressFacts(t, owner, p.TaskID)
			if reply.State != "failed" || reply.Code != want || code != want || mark.Valid != zero || mark.Int64 != 0 {
				t.Fatalf("reply=%s mark=%+v", raw, mark)
			}
		})
	}
}

func TestDownloadFinishSyncPausedPastStallSingleFailureAndResume(t *testing.T) {
	for _, failure := range []bool{true, false} {
		t.Run(map[bool]string{true: "single-failed-lookup", false: "resume-no-growth"}[failure], func(t *testing.T) {
			owner, runtimeDB, p := progressProbe(t, "typed-pause", map[string]any{"stall_seconds": 1}, map[string]any{"max_consecutive_errors": 5, "max_total_errors": 100})
			mark := int64(420)
			arrangeProgress(t, owner, p.TaskID, &mark, 1000, "7 hours")
			work := armSyncClaim(t, owner, runtimeDB, p, "typed-pause-sync")
			if _, err := owner.ExecContext(context.Background(), `UPDATE download_tasks SET provider_correlation='{"external_id":"typed-object","state":"downloading","provider_state":"pauseddl"}' WHERE id=$1`, p.TaskID); err != nil {
				t.Fatal(err)
			}
			var bytes, total, state any
			if !failure {
				bytes = int64(420)
				total = int64(1000)
				state = "downloading"
			}
			raw, err := typedObservationCall(runtimeDB, p, work, failure, bytes, total, state)
			if err != nil {
				t.Fatal(err)
			}
			var reply struct {
				State string `json:"state"`
				Code  string `json:"reason_code"`
				Text  string `json:"reason_text"`
			}
			if err = json.Unmarshal(raw, &reply); err != nil {
				t.Fatal(err)
			}
			want := "DOWNLOAD_STALLED"
			if failure {
				want = "DOWNLOAD_OBSERVATION_UNAVAILABLE"
			}
			if reply.State != "failed" || reply.Code != want || strings.Contains(reply.Text, "连续多次") {
				t.Fatalf("reply=%s", raw)
			}
			if failure && (!strings.Contains(reply.Text, "本轮") || !strings.Contains(reply.Text, "种子仍保留") || !strings.Contains(reply.Text, "未删除")) {
				t.Fatalf("misleading reason: %s", reply.Text)
			}
			var errors, next int
			if err := owner.QueryRowContext(context.Background(), `SELECT total_errors,(SELECT count(*) FROM work_item_history WHERE id='typed-next') FROM download_tasks WHERE id=$1`, p.TaskID).Scan(&errors, &next); err != nil {
				t.Fatal(err)
			}
			if next != 0 || (failure && errors != 1) {
				t.Fatalf("errors=%d successor=%d", errors, next)
			}
		})
	}
}

// The correlation parameter is projection-only: it never feeds the decision
// and the retired stall-since argument is gone from both SQL and Go.
func TestDownloadFinishSyncTypedChannelStatic(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(migrationsDir(t), baselineMigrationFile))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	start := strings.Index(text, "CREATE FUNCTION download_finish_sync(")
	if start < 0 {
		t.Fatal("download_finish_sync is missing from the baseline")
	}
	body := text[start:]
	body = body[strings.Index(body, "AS $_$"):]
	body = body[:strings.Index(body, "$_$;")]
	if strings.Count(body, "p_correlation") != 1 || !strings.Contains(body, "provider_correlation=COALESCE(p_correlation,provider_correlation)") || strings.Contains(body, "p_stall_since") {
		t.Fatal("correlation used outside projection or dead parameter retained")
	}
	goRaw, err := os.ReadFile(filepath.Join(migrationsDir(t), "../../internal/download/application/typed_sync.go"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(goRaw), "stallSince") {
		t.Fatal("dead Go argument remains")
	}
}
