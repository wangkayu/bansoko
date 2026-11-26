package application

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"bansoko/internal/apperrors"
	observabilityport "bansoko/internal/observability/port"
)

type localCleanupTestLogger struct {
	observabilityport.NopRuntimeLogger
	warnings []observabilityport.RuntimeEvent
}

func (l *localCleanupTestLogger) Warn(_ context.Context, event observabilityport.RuntimeEvent) {
	l.warnings = append(l.warnings, event)
}

func TestLocalCleanupRetainedDiagnostic(t *testing.T) {
	const root = "internal-root-id: with-colon"
	const code = "LOCAL_CLEANUP_ACQUISITION_RETAINED"
	for _, tc := range []struct {
		name, detail, reason string
	}{
		{"unresolved effect", "acquisition " + root + ": effect_unresolved", "effect_unresolved"},
		{"dependent rows", "acquisition " + root + ": dependents_present", "dependents_present"},
		{"release pin", "acquisition " + root + ": release_bound", "release_bound"},
		{"future status", "acquisition " + root + ": unexpected-private-status", "acquisition_retained"},
		{"extra text", "acquisition " + root + ": release_bound unexpected-private-status", "acquisition_retained"},
		{"missing id", "acquisition : effect_unresolved", "acquisition_retained"},
		{"malformed", "other message: release_bound", "acquisition_retained"},
		{"empty", "", "acquisition_retained"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logger := &localCleanupTestLogger{}
			s := &Service{logger: logger}
			pgErr := &pgconn.PgError{Code: "23514", Message: code, Detail: tc.detail}
			got := s.mapLocalCleanupDBError(context.Background(), fmt.Errorf("DB: %w", pgErr), "anime-id")
			var business *apperrors.Business
			if !errors.As(got, &business) || business.Status != 409 || business.Code != code || len(business.Details) != 0 {
				t.Fatalf("public error = %#v, want unchanged 409 %s", got, code)
			}
			var leaked *pgconn.PgError
			if errors.As(got, &leaked) || strings.Contains(got.Error(), root) || strings.Contains(got.Error(), "unexpected-private-status") {
				t.Fatalf("public error exposes an internal detail: %v", got)
			}
			want := observabilityport.RuntimeEvent{
				Message: "An acquisition still retains local records.", Component: "anime_ownership",
				Operation: "local_cleanup", Outcome: "failure", HTTPStatus: 409, ReasonCode: tc.reason,
			}
			if !reflect.DeepEqual(logger.warnings, []observabilityport.RuntimeEvent{want}) {
				t.Fatalf("runtime warnings = %#v, want %#v", logger.warnings, want)
			}
		})
	}

	t.Run("other SQLSTATE", func(t *testing.T) {
		logger := &localCleanupTestLogger{}
		s := &Service{logger: logger}
		pgErr := &pgconn.PgError{Code: "23503", Message: code, Detail: "acquisition " + root + ": release_bound"}
		if got := s.mapLocalCleanupDBError(context.Background(), pgErr, "anime-id"); got != pgErr || len(logger.warnings) != 0 {
			t.Fatalf("unrecognized SQLSTATE was mapped: %v, warnings=%v", got, logger.warnings)
		}
	})
}
