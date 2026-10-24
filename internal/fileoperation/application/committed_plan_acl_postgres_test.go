package application

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5/pgconn"
	"strings"
	"testing"
)

func TestCommittedPlanObserverHasNoRepairPrivileges(t *testing.T) {
	s, c := preparedPlanPGFixture(t)
	useRuntimePlanObserver(t, s)
	if _, err := s.PostProcessingPlanHandler().Handle(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	useRuntimePlanObserver(t, s)
	// FileOperation's independently inventoried retained maintenance DML is not
	// a Plan permission; closing it requires its own writer migration.
	for _, table := range []string{"work_items", "postprocessing_plans", "acquisition_files"} {
		var allowed bool
		if err := s.db.QueryRow(`SELECT has_table_privilege(current_user,$1,'INSERT,UPDATE,DELETE')`, "public."+table).Scan(&allowed); err != nil {
			t.Fatal(err)
		}
		if allowed {
			t.Fatalf("runtime repair DML on %s", table)
		}
	}
	for _, statement := range []string{
		`INSERT INTO postprocessing_plans(id) VALUES('acl-forbidden')`,
		`UPDATE postprocessing_plans SET version=version WHERE id=$1`,
		`DELETE FROM postprocessing_plans WHERE id=$1`,
		`SELECT id FROM postprocessing_plans WHERE id=$1 FOR UPDATE NOWAIT`,
	} {
		var err error
		if strings.Contains(statement, "$1") {
			_, err = s.db.Exec(statement, c.Owner.ID)
		} else {
			_, err = s.db.Exec(statement)
		}
		var pg *pgconn.PgError
		if !errors.As(err, &pg) || pg.Code != "42501" {
			t.Fatalf("Plan ACL statement %s: %v", statement, err)
		}
	}
	var public bool
	if err := s.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM pg_proc p CROSS JOIN LATERAL aclexplode(p.proacl) a WHERE p.proname='postprocessing_plan_child_diagnostic' AND a.grantee=0 AND a.privilege_type='EXECUTE')`).Scan(&public); err != nil {
		t.Fatal(err)
	}
	if public {
		t.Fatal("PUBLIC graph validator")
	}
}
