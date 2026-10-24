package application

import (
	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/attrrules"
	"bansoko/internal/namingvars"
	"context"
	"database/sql"
	"encoding/json"
)

func verifyBusinessProgramsTx(ctx context.Context, tx *sql.Tx, b animedomain.BusinessSnapshot) error {
	var varsRaw, attrRaw []byte
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
	if err := tx.QueryRowContext(ctx, `SELECT content FROM attr_rule_versions WHERE fingerprint=$1`, b.Programs.AttrRules.Digest).Scan(&attrRaw); err != nil {
		return businessInputError("exact attrrules program is not persisted")
	}
	var rules attrrules.Rules
	if err := json.Unmarshal(attrRaw, &rules); err != nil {
		return err
	}
	digest, err := attrrules.Fingerprint(rules)
	if err != nil {
		return err
	}
	if digest != b.Programs.AttrRules.Digest {
		return businessInputError("persisted attrrules digest mismatch")
	}
	return nil
}
