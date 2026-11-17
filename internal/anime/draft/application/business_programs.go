package application

import (
	"context"
	"database/sql"
	"encoding/json"

	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/namingvars"
)

func verifyBusinessProgramsTx(ctx context.Context, tx *sql.Tx, b animedomain.BusinessSnapshot) error {
	var varsRaw []byte
	if err := tx.QueryRowContext(ctx, `SELECT content FROM naming_vars_versions WHERE fingerprint=$1`, b.Programs.Vars.Digest).Scan(&varsRaw); err != nil {
		return businessInputError("exact Vars program is not persisted")
	}
	var source string
	if err := json.Unmarshal(varsRaw, &source); err != nil {
		return err
	}
	if _, err := namingvars.NewProgram(b.Programs.Vars, source); err != nil {
		return businessInputError(err.Error())
	}
	return nil
}
