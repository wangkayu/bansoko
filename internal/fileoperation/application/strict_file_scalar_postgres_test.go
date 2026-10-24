package application

import (
	"context"
	"database/sql"
	"testing"
)

func verifyStrictFileScalars(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	owner := ownerTestDB(t)
	before := planGraphSnapshot(t, db)
	cases := []struct {
		fn, input string
		valid     bool
	}{
		{"time", `"2026-10-09T01:02:03.123456Z"`, true}, {"time", `"2026-10-09T01:02:03Z"`, true},
		{"time", `null`, false}, {"time", `1`, false}, {"time", `"2026-02-30T01:02:03Z"`, false}, {"time", `"2026-10-09T01:02:03+00:00"`, false}, {"time", `"2026-10-09T01:02:03.120Z"`, false}, {"time", `"2026-10-09T01:02:03.1234567Z"`, false}, {"time", `"infinity"`, false},
		{"id", `"合法nonce"`, true}, {"id", `""`, false}, {"id", `null`, false}, {"id", `1`, false}, {"id", `" nonce"`, false}, {"id", `"nonce "`, false}, {"id", `"a\nb"`, false},
		{"u64", `"18446744073709551615"`, true}, {"u64", `"0"`, true}, {"u64", `"18446744073709551616"`, false}, {"u64", `"01"`, false}, {"u64", `"-1"`, false}, {"u64", `"1e2"`, false}, {"u64", `null`, false}, {"u64", `1`, false},
		{"fv", `{"state":"known","value":1,"reason_code":null}`, true}, {"fv", `{"state":"absent","value":null,"reason_code":null}`, true}, {"fv", `{"state":"unavailable","value":null,"reason_code":"UNAVAILABLE"}`, true},
		{"fv", `{"state":"known","value":null,"reason_code":null}`, false}, {"fv", `{"state":"absent","value":1,"reason_code":null}`, false}, {"fv", `{"state":"unavailable","value":null,"reason_code":null}`, false}, {"fv", `{"state":"foreign","value":null,"reason_code":null}`, false}, {"fv", `null`, false}, {"fv", `{"state":"absent","value":null,"reason_code":null,"extra":true}`, false},
		{"handle", `{"mount_id":-1,"handle_type":0,"opaque":"","encoding":"base64"}`, true}, {"handle", `{"mount_id":0,"handle_type":-1,"opaque":"not-base64","encoding":"base64"}`, true},
		{"handle", `{"mount_id":null,"handle_type":0,"opaque":"","encoding":"base64"}`, false}, {"handle", `{"mount_id":0,"handle_type":0,"opaque":null,"encoding":"base64"}`, false}, {"handle", `{"mount_id":0,"handle_type":0,"opaque":"","encoding":"foreign"}`, false}, {"handle", `{"mount_id":0,"handle_type":0,"opaque":"","encoding":"base64","extra":true}`, false},
	}
	for _, tc := range cases {
		var got bool
		if err := owner.QueryRowContext(ctx, `SELECT file_wire_`+tc.fn+`_valid($1::jsonb)`, tc.input).Scan(&got); err != nil || got != tc.valid {
			t.Fatalf("scalar %s %s=%v err=%v", tc.fn, tc.input, got, err)
		}
		if planGraphSnapshot(t, db) != before {
			t.Fatal("scalar validator wrote graph")
		}
	}
}
