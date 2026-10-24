package application

import (
	"context"
	"database/sql"
	_ "github.com/jackc/pgx/v5/stdlib"
	"testing"
)

func TestCommittedPlanObservationDatabaseErrorIsNotConfirmation(t *testing.T) {
	db, err := sql.Open("pgx", "postgres://localhost/unused")
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	s := &Service{db: db}
	out, err := s.ObserveCommittedPlan(context.Background(), CommittedPlanObservationRequest{PlanID: "p", ArtifactID: "a", AcquisitionID: "r"})
	if err == nil || out.Status != "observation_error" {
		t.Fatalf("%+v %v", out, err)
	}
}
