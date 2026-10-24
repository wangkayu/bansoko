package application

import (
	"bansoko/internal/apperrors"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"os"
	"strings"
	"sync/atomic"
	"testing"
)

// Intercept only the production capture query; all transaction and typed-stage
// writes run on the real segregated runtime connection, without owner grants.
type captureFailureConnector struct {
	driver.Connector
	cause error
	calls atomic.Int32
}
type captureFailureConn struct {
	driver.Conn
	connector *captureFailureConnector
}

func (c *captureFailureConnector) Connect(ctx context.Context) (driver.Conn, error) {
	v, e := c.Connector.Connect(ctx)
	if e != nil {
		return nil, e
	}
	return &captureFailureConn{v, c}, nil
}
func (c *captureFailureConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	return c.Conn.(driver.ConnBeginTx).BeginTx(ctx, opts)
}
func (c *captureFailureConn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	if strings.Contains(q, "t.frozen_input, t.frozen_policy") {
		c.connector.calls.Add(1)
		return nil, c.connector.cause
	}
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, q, args)
}
func TestTypedPlanHandlerClassifiesPreparationFailure(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		name := "transient"
		if terminal {
			name = "deterministic"
		}
		t.Run(name, func(t *testing.T) {
			s, c := preparedPlanPGFixture(t)
			cause := error(errors.New("fixture capture observation unavailable"))
			if terminal {
				cause = &apperrors.Business{Code: "FIXTURE_PLAN_INVALID", Message: "fixture deterministic capture failure"}
			}
			dsn := os.Getenv("BANSOKO_TEST_RUNTIME_DATABASE_URL")
			if dsn == "" {
				dsn = os.Getenv("BANSOKO_TEST_DATABASE_URL")
			}
			config, err := pgx.ParseConfig(dsn)
			if err != nil {
				t.Fatal(err)
			}
			config.RuntimeParams["role"] = "bansoko_runtime"
			connector := &captureFailureConnector{Connector: stdlib.GetConnector(*config), cause: cause}
			db := sql.OpenDB(connector)
			defer db.Close()
			s.db = db
			s.workItems = &workitems.Store{DB: db, RetentionDays: workitems.DefaultRetentionDays}
			var role string
			if err := db.QueryRow(`SELECT current_user`).Scan(&role); err != nil {
				t.Fatal(err)
			}
			if role != "bansoko_runtime" {
				t.Fatalf("classification role=%s", role)
			}
			out, err := s.PostProcessingPlanHandler().Handle(context.Background(), c)
			if err != nil {
				t.Fatal(err)
			}
			if connector.calls.Load() != 1 {
				t.Fatalf("capture fault calls=%d", connector.calls.Load())
			}
			if out.Kind != worker.OutcomeCommitted || out.WriteOwner != nil || out.PostTransition != nil {
				t.Fatalf("non-typed classification: %+v", out)
			}
			want := workitems.StateAvailable
			if terminal {
				want = workitems.StateFailed
			}
			var state string
			var attempts int
			if err := db.QueryRow(`SELECT state,attempt FROM work_item_history WHERE id=$1`, c.ID).Scan(&state, &attempts); err != nil {
				t.Fatal(err)
			}
			if state != want || attempts != c.Attempt {
				t.Fatalf("classification state=%s attempts=%d claim=%d", state, attempts, c.Attempt)
			}
			var count int
			if err := db.QueryRow(`SELECT count(*) FROM file_operations WHERE postprocessing_plan_id=$1`, c.Owner.ID).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatalf("preparation failure published %d Operations", count)
			}
		})
	}
}
