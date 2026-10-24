package migrations

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

type tableACL struct {
	Table         string   `json:"table"`
	Privileges    []string `json:"privileges"`
	Reason        string   `json:"reason"`
	DirectAccess  []string `json:"direct_access"`
	ClosureSlice  string   `json:"closure_slice"`
	ExitCondition string   `json:"exit_condition"`
	// ColumnPrivileges are granted per column instead of per table; the
	// ExcludedColumnPrefixes name the proof columns those grants must cover
	// (PRUNE-046 S6: the submission/publication slot is SECURITY DEFINER-only).
	ColumnPrivileges       []string `json:"column_privileges"`
	ExcludedColumnPrefixes []string `json:"excluded_column_prefixes"`
}

type aclQuerier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func auditRuntimeTableACL(ctx context.Context, db aclQuerier, role string, inventory []tableACL) ([]string, error) {
	registered := map[string]map[string]bool{}
	for _, e := range inventory {
		if e.Table == "" || e.Reason == "" || len(e.DirectAccess) == 0 || e.ClosureSlice == "" || e.ExitCondition == "" {
			return nil, fmt.Errorf("incomplete ACL registration: %s", e.Table)
		}
		if _, exists := registered[e.Table]; exists {
			return nil, fmt.Errorf("duplicate ACL registration: %s", e.Table)
		}
		registered[e.Table] = map[string]bool{}
		for _, p := range e.Privileges {
			registered[e.Table][p] = true
		}
	}
	rows, err := db.QueryContext(ctx, `SELECT c.relname,p.priv,has_table_privilege($1,c.oid,p.priv) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace CROSS JOIN (VALUES ('INSERT'),('UPDATE'),('DELETE'),('TRUNCATE')) p(priv) WHERE n.nspname='public' AND c.relkind IN ('r','p') ORDER BY c.relname,p.priv`, role)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var less []string
	for rows.Next() {
		var table, p string
		var actual bool
		if err := rows.Scan(&table, &p, &actual); err != nil {
			return nil, err
		}
		if actual && !registered[table][p] {
			return nil, fmt.Errorf("unregistered runtime write privilege: %s %s", table, p)
		}
		if !actual && registered[table][p] {
			less = append(less, table+" "+p)
		}
		if registered[table] != nil {
			delete(registered[table], p)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for table, permissions := range registered {
		for p := range permissions {
			less = append(less, table+" "+p+" (table absent)")
		}
	}
	// Column-scoped grants: every listed privilege must be denied on the excluded
	// prefixes (the proof columns) and allowed on every other column.
	for _, entry := range inventory {
		if len(entry.ColumnPrivileges) == 0 && len(entry.ExcludedColumnPrefixes) == 0 {
			continue
		}
		if len(entry.ColumnPrivileges) == 0 || len(entry.ExcludedColumnPrefixes) == 0 {
			return nil, fmt.Errorf("half-registered column privileges: %s", entry.Table)
		}
		for _, priv := range entry.ColumnPrivileges {
			rows, err := db.QueryContext(ctx, `SELECT column_name,has_column_privilege($1,$2,column_name,$3) FROM information_schema.columns WHERE table_schema='public' AND table_name=$2 ORDER BY column_name`, role, entry.Table, priv)
			if err != nil {
				return nil, err
			}
			for rows.Next() {
				var column string
				var allowed bool
				if err := rows.Scan(&column, &allowed); err != nil {
					rows.Close()
					return nil, err
				}
				excluded := false
				for _, prefix := range entry.ExcludedColumnPrefixes {
					if strings.HasPrefix(column, prefix) {
						excluded = true
					}
				}
				if excluded && allowed {
					rows.Close()
					return nil, fmt.Errorf("runtime holds %s on the proof column %s.%s", priv, entry.Table, column)
				}
				if !excluded && !allowed {
					less = append(less, entry.Table+"."+column+" "+priv)
				}
			}
			if err := rows.Err(); err != nil {
				rows.Close()
				return nil, err
			}
			rows.Close()
		}
	}
	sort.Strings(less)
	return less, nil
}

// TestRuntimeProofColumnWritesAreRefused pins the S6 ACL split: the runtime may
// still write the ordinary owner columns directly, but a direct write of any
// submission/publication proof column is refused with 42501.
func TestRuntimeProofColumnWritesAreRefused(t *testing.T) {
	_, runtimeDB, _ := progressProbe(t, "acl-proof-columns", nil, nil)
	for _, probe := range []struct{ SQL string }{
		{`UPDATE file_operations SET publication_closed_proof='{"kind":"forged"}'::jsonb WHERE false`},
		{`UPDATE file_operations SET publication_dispatch_state='closed' WHERE false`},
		{`UPDATE file_operations SET publication_evidence_version=publication_evidence_version+1 WHERE false`},
		{`UPDATE download_tasks SET submit_closed_proof='{"kind":"forged"}'::jsonb WHERE false`},
		{`UPDATE download_tasks SET submit_permit_id='forged' WHERE false`},
		{`INSERT INTO file_operations(publication_permit_id) VALUES('forged')`},
		{`INSERT INTO download_tasks(submit_permit_id) VALUES('forged')`},
	} {
		if _, err := runtimeDB.Exec(probe.SQL); sqlState(err) != "42501" {
			t.Fatalf("want 42501 for %q, got %v", probe.SQL, err)
		}
	}
	// The ordinary owner columns keep their pre-fold direct write privilege.
	for _, probe := range []string{
		`UPDATE file_operations SET state='pending' WHERE false`,
		`UPDATE download_tasks SET state='queued' WHERE false`,
	} {
		if _, err := runtimeDB.Exec(probe); err != nil && sqlState(err) == "42501" {
			t.Fatalf("ordinary column write lost its privilege: %q: %v", probe, err)
		}
	}
}

func TestRuntimeTableACLInventory(t *testing.T) {
	owner, runtimeDB, _ := progressProbe(t, "acl-inventory", nil, nil)
	raw, err := os.ReadFile(filepath.Join("runtime_table_acl_inventory.json"))
	if err != nil {
		t.Fatal(err)
	}
	var inventory []tableACL
	if err := json.Unmarshal(raw, &inventory); err != nil {
		t.Fatal(err)
	}
	// Every registered table must still exist in the schema the runner accepts.
	for _, entry := range inventory {
		if _, ok := knownRelations[entry.Table]; !ok {
			t.Errorf("ACL inventory lists %s, which is not a known relation", entry.Table)
		}
	}
	ctx := context.Background()
	var role string
	if err := runtimeDB.QueryRow(`SELECT current_user`).Scan(&role); err != nil {
		t.Fatal(err)
	}
	less, err := auditRuntimeTableACL(ctx, runtimeDB, role, inventory)
	if err != nil {
		t.Fatal(err)
	}
	// A table registered with an explicit column allow-list must keep that list
	// complete: a new column added without a grant would silently become
	// unwritable by the runtime, so it fails the gate instead of logging.
	columnTables := map[string]bool{}
	for _, entry := range inventory {
		if len(entry.ColumnPrivileges) > 0 {
			columnTables[entry.Table] = true
		}
	}
	var missingColumns []string
	for _, entry := range less {
		if dot := strings.Index(entry, "."); dot > 0 && columnTables[entry[:dot]] {
			missingColumns = append(missingColumns, entry)
		}
	}
	if len(missingColumns) > 0 {
		t.Fatalf("runtime is missing registered column privileges: %v", missingColumns)
	}
	if len(less) > 0 {
		t.Logf("update ACL inventory: actual fewer grants: %v", less)
	}
	for _, kind := range []string{"default-new-table", "extra-grant"} {
		t.Run(kind, func(t *testing.T) {
			tx, err := owner.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if kind == "default-new-table" {
				_, err = tx.Exec(`CREATE TABLE public.unregistered_default_table(id text)`)
			} else {
				_, err = tx.Exec(`GRANT TRUNCATE ON public.download_tasks TO bansoko_runtime`)
			}
			if err != nil {
				t.Fatal(err)
			}
			// This transaction sees its own temporary grant/DDL. Audit the effective
			// runtime role (including inheritance), never owner privileges.
			if _, err := auditRuntimeTableACL(ctx, tx, role, inventory); err == nil {
				t.Fatal("ACL guard accepted privilege growth")
			}
		})
	}
}
