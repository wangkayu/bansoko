package persistence

import (
	"bansoko/internal/namingvars"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

// LoadInstanceProgram loads only the frozen reference and its exact retained
// source. It never reads active, evaluates a sample or updates publication.
func LoadInstanceProgram(ctx context.Context, db *sql.DB, animeID string) (namingvars.Program, error) {
	if db == nil {
		return namingvars.Program{}, fmt.Errorf("instance Vars reference unavailable: database is not wired")
	}
	var raw []byte
	if err := db.QueryRowContext(ctx, `SELECT business_snapshot->'programs'->'vars' FROM anime_business_configs WHERE managed_anime_id=$1 AND business_snapshot IS NOT NULL`, animeID).Scan(&raw); err != nil {
		return namingvars.Program{}, fmt.Errorf("instance Vars reference unavailable: %w", err)
	}
	var ref namingvars.ProgramRef
	if err := json.Unmarshal(raw, &ref); err != nil {
		return namingvars.Program{}, err
	}
	return namingvars.NewStore(NewRepository(db)).LoadProgram(ctx, ref)
}
