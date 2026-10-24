package application

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// Invalid edits are rejected by the real sealed graph gate. This is constraint
// evidence, not evidence that an invalid graph reached the finish or recursion.
func TestSealedGraphRejectsInvalidEdits(t *testing.T) {
	s, c := preparedPlanPGFixture(t)
	ctx := context.Background()
	if _, err := s.PostProcessingPlanHandler().Handle(ctx, c); err != nil {
		t.Fatal(err)
	}
	owner := ownerTestDB(t)
	for _, tc := range []struct{ name, query string }{
		{"sealed_plan", `UPDATE public.postprocessing_plans SET sealed_at=sealed_at-interval '1 minute' WHERE id=$1`},
		{"retain_without_evidence", `UPDATE public.acquisition_files SET plan_kind='retain' WHERE acquisition_id=(SELECT acquisition_id FROM public.postprocessing_plans WHERE id=$1) AND plan_kind='execute'`},
		{"dropped_diagnostic", `UPDATE public.acquisition_files SET plan_diagnostic=NULL WHERE acquisition_id=(SELECT acquisition_id FROM public.postprocessing_plans WHERE id=$1) AND plan_kind='execute'`},
		{"excluded_required", `UPDATE public.acquisition_files SET requirement='excluded' WHERE acquisition_id=(SELECT acquisition_id FROM public.postprocessing_plans WHERE id=$1) AND plan_kind='execute'`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := planGraphSnapshot(t, owner)
			_, err := owner.ExecContext(ctx, tc.query, c.Owner.ID)
			var pg *pgconn.PgError
			if !errors.As(err, &pg) || pg.Code != "23514" {
				t.Fatalf("sealed graph rejection=%v", err)
			}
			if planGraphSnapshot(t, owner) != before {
				t.Fatal("invalid graph edit changed graph")
			}
		})
	}
}
