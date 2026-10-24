package application

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5/pgconn"
	"testing"
)

func TestTypedPlanProofRejectsChangedCommittedOperation(t *testing.T) {
	for _, tc := range []struct{ name, mutation string }{
		{"budget", `file_max_attempts=file_max_attempts+1`},
		{"timeout", `file_attempt_timeout_seconds=file_attempt_timeout_seconds+1`},
		{"wall", `file_wall_until=file_wall_until+interval '1 second'`},
		{"path", `target_path=target_path||'.drift'`},
		{"config", `frozen_config_snapshot=frozen_config_snapshot||'{"extra":true}'::jsonb`},
		{"source", `source_path=source_path||'.drift'`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, c := preparedPlanPGFixture(t)
			ctx := context.Background()
			if _, err := s.PostProcessingPlanHandler().Handle(ctx, c); err != nil {
				t.Fatal(err)
			}
			if _, ok, err := s.confirmTypedPlanCommit(ctx, c); err != nil || !ok {
				t.Fatalf("normal proof=%v %v", ok, err)
			}
			owner := ownerTestDB(t)
			original := planGraphSnapshot(t, owner)
			_, mutationErr := owner.Exec(`UPDATE file_operations SET `+tc.mutation+` WHERE postprocessing_plan_id=$1`, c.Owner.ID)
			if tc.name == "source" {
				var pg *pgconn.PgError
				if !errors.As(mutationErr, &pg) || pg.Code != "23503" {
					t.Fatalf("source identity guard=%v", mutationErr)
				}
				if planGraphSnapshot(t, owner) != original {
					t.Fatal("source rejection changed graph")
				}
				return
			}
			// R2 ruling: the 163 budget guard freezes file_max_attempts,
			// file_attempt_timeout_seconds and file_wall_until at INSERT, so a
			// committed operation's budget can no longer drift. The DB guard is
			// the stronger protection; assert it refuses the tamper with zero
			// writes instead of reaching the proof layer.
			if tc.name == "budget" || tc.name == "timeout" || tc.name == "wall" {
				var pg *pgconn.PgError
				if !errors.As(mutationErr, &pg) || pg.Code != "23514" || pg.Message != "FILE_ATTEMPT_BUDGET_IMMUTABLE" {
					t.Fatalf("%s budget guard=%v", tc.name, mutationErr)
				}
				if planGraphSnapshot(t, owner) != original {
					t.Fatalf("%s budget rejection changed graph", tc.name)
				}
				return
			}
			if mutationErr != nil {
				t.Fatal(mutationErr)
			}
			before := planGraphSnapshot(t, owner)
			useRuntimePlanObserver(t, s)
			if _, ok, err := s.confirmTypedPlanCommit(ctx, c); err != nil || ok {
				t.Fatalf("corrupt %s proof=%v %v", tc.name, ok, err)
			}
			if after := planGraphSnapshot(t, owner); after != before {
				t.Fatal("proof repaired graph")
			}
		})
	}
}
