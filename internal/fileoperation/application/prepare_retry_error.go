package application

import (
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5/pgconn"
)

var ErrPrepareAdmissionBusy = errors.New("post-processing admission busy; retry frozen plan")

func prepareAdmissionError(err error) error {
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "55P03" {
		return fmt.Errorf("%w: %v", ErrPrepareAdmissionBusy, err)
	}
	return err
}
