package application

import (
	acquisition "bansoko/internal/acquisition/domain"
	domain "bansoko/internal/anime/domain"
	"bansoko/internal/namingvars"
	varspersistence "bansoko/internal/namingvars/adapter/persistence"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path"
)

// freezeBusinessVarsTx freezes Vars from the exact business program reference,
// never active source/sample output. The input is the shared release-level
// evidence (namingvars.BuildInput): the anime and binding rows this command
// wrote in tx, the release id it assigned, the frozen episode, the item's tag
// sets and its exact inventory.
func freezeBusinessVarsTx(ctx context.Context, tx *sql.Tx, business domain.BusinessSnapshot, item acquisition.FeedItem, animeID, releaseID string, episode *namingvars.EpisodeEvidence) (namingvars.FrozenEvaluation, error) {
	var content []byte
	if err := tx.QueryRowContext(ctx, `SELECT content FROM naming_vars_versions WHERE fingerprint=$1`, business.Programs.Vars.Digest).Scan(&content); err != nil {
		return namingvars.FrozenEvaluation{}, fmt.Errorf("exact historical Vars program unavailable: %w", err)
	}
	var source string
	if err := json.Unmarshal(content, &source); err != nil {
		return namingvars.FrozenEvaluation{}, err
	}
	program, err := namingvars.NewProgram(business.Programs.Vars, source)
	if err != nil {
		return namingvars.FrozenEvaluation{}, err
	}
	evidence, err := varspersistence.ItemEvidence(ctx, tx, animeID, releaseID, item, episode)
	if err != nil {
		return namingvars.FrozenEvaluation{}, err
	}
	for _, file := range item.ReleaseFiles {
		if file.SizeBytes == nil {
			return namingvars.FrozenEvaluation{}, fmt.Errorf("Vars inventory size missing for %s", file.SourcePath)
		}
		evidence.Files = append(evidence.Files, namingvars.File{Name: path.Base(file.SourcePath), Path: file.SourcePath, Size: *file.SizeBytes})
	}
	frozen, err := program.Freeze(namingvars.BuildInput(evidence))
	if err != nil {
		return frozen, err
	}
	if frozen.Status != "ok" {
		return frozen, fmt.Errorf("Vars evaluation failed: %s", frozen.Diagnostic)
	}
	return frozen, nil
}
