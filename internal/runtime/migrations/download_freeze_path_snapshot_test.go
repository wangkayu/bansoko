package migrations

import (
	"strings"
	"testing"
)

func TestDownloadFreezePathSnapshot(t *testing.T) {
	owner, runtimeDB, p := progressProbe(t, "freeze-path", nil, nil)
	if _, err := owner.Exec(`UPDATE work_items SET state='leased',fencing_token=1,lease_until=clock_timestamp()+interval '1 hour' WHERE id=$1`, p.SubmitWorkID); err != nil {
		t.Fatal(err)
	}
	var input string
	var version int64
	if err := owner.QueryRow(`SELECT (frozen_input || '{"path_snapshot":{"remote_root":"/downloads","application_root":"/downloads","path_mappings":[]}}')::text,version FROM download_tasks WHERE id=$1`, p.TaskID).Scan(&input, &version); err != nil {
		t.Fatal(err)
	}
	freeze := func(token, v int64, s string) (int64, error) {
		var n int64
		err := runtimeDB.QueryRow(`SELECT download_freeze_path_snapshot($1,$2,$3,$4,$5)`, p.TaskID, p.SubmitWorkID, token, v, s).Scan(&n)
		return n, err
	}
	n, err := freeze(1, version, input)
	if err != nil || n != version+1 {
		t.Fatalf("freeze version=%d err=%v", n, err)
	}
	repeat, err := freeze(1, n, input)
	if err != nil || repeat != n {
		t.Fatalf("idempotent=%d err=%v", repeat, err)
	}
	for _, bad := range []struct {
		name           string
		token, version int64
		input          string
	}{{"token", 2, n, input}, {"version", 1, n - 1, input}, {"different", 1, n, strings.Replace(input, "/downloads", "/changed", -1)}} {
		t.Run(bad.name, func(t *testing.T) {
			_, err := freeze(bad.token, bad.version, bad.input)
			requireSQLState(t, err, "23514")
		})
	}
	if _, err := owner.Exec(`UPDATE work_items SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, p.SubmitWorkID); err != nil {
		t.Fatal(err)
	}
	_, err = freeze(1, n, input)
	requireSQLState(t, err, "23514")
	if _, err := owner.Exec(`UPDATE work_items SET lease_until=clock_timestamp()+interval '1 hour' WHERE id=$1`, p.SubmitWorkID); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err := runtimeDB.QueryRow(`SELECT download_start_submit_attempt($1,$2,1,'freeze-session')`, p.TaskID, p.SubmitWorkID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err := runtimeDB.QueryRow(`SELECT download_start_dispatch($1,$2,1,'freeze-session','freeze-permit',$3,'connection',$3,'{"key":"freeze"}','{"root":"/downloads"}','{"hash":"a"}')`, p.TaskID, p.SubmitWorkID, acqDigest("a")).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	_, err = freeze(1, n, input)
	requireSQLState(t, err, "23514")
}

func requireSQLState(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected SQLSTATE", want)
	}
	state, ok := err.(interface{ SQLState() string })
	if !ok || state.SQLState() != want {
		t.Fatalf("expected %s: %v", want, err)
	}
}
