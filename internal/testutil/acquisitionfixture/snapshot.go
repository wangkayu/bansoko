package acquisitionfixture

import (
	"context"
	"database/sql"
	"testing"
)

// Snapshot captures every relation using an owner-only read. PostgreSQL
// formats catalog identifiers with %I; the Go SQL statement is fixed.
func Snapshot(t *testing.T, db *sql.DB) string {
	t.Helper()
	var snapshot string
	err := db.QueryRowContext(context.Background(), `SELECT COALESCE(jsonb_object_agg(tablename, contents ORDER BY tablename),'{}'::jsonb)::text FROM (SELECT tablename, query_to_xml(format('SELECT to_jsonb(r)::text AS row FROM public.%I r ORDER BY to_jsonb(r)::text',tablename),true,false,'')::text AS contents FROM pg_tables WHERE schemaname='public') audit`).Scan(&snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}
