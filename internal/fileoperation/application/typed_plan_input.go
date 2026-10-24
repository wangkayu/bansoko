package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"bansoko/internal/namingvars"
	"bansoko/internal/postprocessing/naming"
)

// loadTypedPlanInput copies the acquisition's accepted input. Neither today's
// metadata nor the current library-root resolver participates in this plan.
func loadTypedPlanInput(ctx context.Context, tx *sql.Tx, b *artifactPreparedBatch) error {
	var raw []byte
	if err := tx.QueryRowContext(ctx, `SELECT i.input FROM acquisitions i JOIN postprocessing_plans p ON p.acquisition_id=i.id WHERE p.id=$1`, b.capture.PlanID).Scan(&raw); err != nil {
		return err
	}
	return applyTypedPlanInput(raw, b)
}

func applyTypedPlanInput(raw []byte, b *artifactPreparedBatch) error {
	var in struct {
		Config struct {
			Sections []struct {
				Key     string          `json:"section_key"`
				Version int64           `json:"config_version"`
				Value   json.RawMessage `json:"value"`
			} `json:"sections"`
			Vars json.RawMessage `json:"vars"`
		} `json:"config"`
		Media struct {
			Title         frozenPlanValue `json:"title"`
			TitleOverride frozenPlanValue `json:"title_override"`
			ProviderTitle frozenPlanValue `json:"provider_title"`
			BGM           frozenPlanValue `json:"bgm"`
			TMDB          frozenPlanValue `json:"tmdb"`
			Quality       frozenPlanValue `json:"quality"`
			BGMTitles     frozenPlanValue `json:"bgm_titles"`
			TMDBTitles    frozenPlanValue `json:"tmdb_titles"`
		} `json:"media"`
		Source struct {
			InfohashV1       frozenPlanValue `json:"infohash_v1"`
			InfohashV2       frozenPlanValue `json:"infohash_v2"`
			TorrentSize      frozenPlanValue `json:"torrent_size"`
			TorrentFileCount frozenPlanValue `json:"torrent_file_count"`
		} `json:"source"`
		Delivery struct {
			Intent    string `json:"intent"`
			Collision string `json:"collision"`
			Route     struct {
				Root frozenPlanValue `json:"root"`
			} `json:"route"`
		} `json:"delivery"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return err
	}
	sections := map[string]json.RawMessage{}
	versions := map[string]int64{}
	for _, s := range in.Config.Sections {
		sections[s.Key] = s.Value
		versions[s.Key] = s.Version
	}
	var vars struct {
		Mode    string                `json:"mode"`
		Program namingvars.ProgramRef `json:"program"`
		Output  namingvars.Vars       `json:"output"`
		Status  string                `json:"status"`
		Reason  *string               `json:"reason_code"`
		Known   struct {
			Title frozenPlanValue   `json:"release_title"`
			Files []namingvars.File `json:"files"`
			Facts map[string]any    `json:"facts"`
		} `json:"known_inputs"`
	}
	if err := json.Unmarshal(in.Config.Vars, &vars); err != nil {
		return err
	}
	evaluation := namingvars.FrozenEvaluation{Program: vars.Program, Output: vars.Output, Status: vars.Status}
	if vars.Reason != nil {
		evaluation.Diagnostic = *vars.Reason
	}
	if vars.Known.Title.State == "known" {
		if err := json.Unmarshal(vars.Known.Title.Value, &evaluation.Input.ReleaseTitle); err != nil {
			return err
		}
	}
	evaluation.Input.Files = vars.Known.Files
	evaluation.Input.Facts = vars.Known.Facts
	if vars.Mode == "disabled" {
		evaluation.Status = "ok"
		evaluation.Output = namingvars.Vars{} // disabled wire has no output; engine requires an object
	}
	// Internal engine projection must preserve an empty Vars object; the accepted
	// disabled wire remains mode/runtime_version only. Do not normalize error nil.
	policy, err := typedPlanPolicy(sections, versions, evaluation)
	if err != nil {
		return err
	}
	config, err := effectiveConfigFromFrozenDownloadPolicy(policy)
	if err != nil {
		return err
	}
	// The accepted delivery is the collision authority. Acceptance freezes an
	// overwrite policy whose target was observed absent as an ordinary
	// ensure_present/block delivery (overwrite only authorizes replacing an
	// existing file), so the plan must not ask for a replacement binding.
	if config.effect.CollisionPolicy == fileOperationCollisionOverwrite && in.Delivery.Intent == "ensure_present" && in.Delivery.Collision == "block" {
		config.effect.CollisionPolicy = fileOperationCollisionFail
	}
	b.artifact.acceptedResolution = ""
	if in.Media.Quality.State == "known" {
		var quality struct {
			Resolution struct {
				State string `json:"state"`
				Value string `json:"value"`
			} `json:"resolution"`
		}
		if err := json.Unmarshal(in.Media.Quality.Value, &quality); err != nil {
			return err
		}
		if quality.Resolution.State == "value" {
			b.artifact.acceptedResolution = quality.Resolution.Value
		}
	}
	b.artifact.frozenPolicy = policy
	b.artifact.acceptedFacts = true
	// The accepted delivery route mode says nothing about unknown files. The
	// unclassified_route switch is the instance policy, read by
	// captureArtifactBatch; until then unknown files fail closed to skip.
	config.unclassifiedRoute = "skip"
	b.prep = prepareContext{config: config, replanTargets: true}
	b.artifact.animeTitle = ""
	b.artifact.titleOverride = sql.NullString{}
	b.artifact.providerTitle = ""
	b.artifact.metadataBGM = sql.NullString{}
	b.artifact.tmdbWorkID = sql.NullString{}
	b.artifact.tmdbMediaKind = sql.NullString{}
	b.artifact.tmdbSeason = sql.NullInt64{}
	if in.Media.Title.State == "known" {
		if err := json.Unmarshal(in.Media.Title.Value, &b.artifact.animeTitle); err != nil {
			return err
		}
	}
	if in.Media.TitleOverride.State == "known" {
		b.artifact.titleOverride.Valid = true
		if err := json.Unmarshal(in.Media.TitleOverride.Value, &b.artifact.titleOverride.String); err != nil {
			return err
		}
	}
	if in.Media.ProviderTitle.State == "known" {
		if err := json.Unmarshal(in.Media.ProviderTitle.Value, &b.artifact.providerTitle); err != nil {
			return err
		}
	}
	if in.Media.BGM.State == "known" {
		var value struct {
			Ref string `json:"ref"`
		}
		if err := json.Unmarshal(in.Media.BGM.Value, &value); err != nil {
			return err
		}
		b.artifact.metadataBGM = sql.NullString{String: value.Ref, Valid: true}
	}
	if in.Media.TMDB.State == "known" {
		var value struct {
			Ref    string `json:"work_ref"`
			Kind   string `json:"kind"`
			Season int64  `json:"season"`
		}
		if err := json.Unmarshal(in.Media.TMDB.Value, &value); err != nil {
			return err
		}
		b.artifact.tmdbWorkID = sql.NullString{String: value.Ref, Valid: true}
		b.artifact.tmdbMediaKind = sql.NullString{String: value.Kind, Valid: true}
		b.artifact.tmdbSeason = sql.NullInt64{Int64: value.Season, Valid: true}
	}
	// The native work titles (schema 5) and the torrent metadata (schema 4)
	// are read only from the accepted input; older inputs leave them unknown.
	b.artifact.bindingTitles = frozenBindingTitles{}
	if in.Media.BGMTitles.State == "known" {
		var value struct {
			Title   *string `json:"title"`
			TitleCN *string `json:"title_cn"`
			Year    *int64  `json:"year"`
		}
		if err := json.Unmarshal(in.Media.BGMTitles.Value, &value); err != nil {
			return err
		}
		b.artifact.bindingTitles.bgmTitle, b.artifact.bindingTitles.bgmTitleCN = derefString(value.Title), derefString(value.TitleCN)
		b.artifact.bindingTitles.bgmYear = value.Year
	}
	if in.Media.TMDBTitles.State == "known" {
		var value struct {
			TitleCN       *string `json:"title_cn"`
			OriginalTitle *string `json:"original_title"`
			Year          *int64  `json:"year"`
		}
		if err := json.Unmarshal(in.Media.TMDBTitles.Value, &value); err != nil {
			return err
		}
		b.artifact.bindingTitles.tmdbTitleCN, b.artifact.bindingTitles.tmdbOriginalTitle = derefString(value.TitleCN), derefString(value.OriginalTitle)
		b.artifact.bindingTitles.tmdbYear = value.Year
	}
	b.artifact.torrent = naming.TorrentNamingFacts{}
	for _, item := range []struct {
		value  frozenPlanValue
		target *string
	}{{in.Source.InfohashV1, &b.artifact.torrent.HashV1}, {in.Source.InfohashV2, &b.artifact.torrent.HashV2}} {
		if item.value.State == "known" {
			if err := json.Unmarshal(item.value.Value, item.target); err != nil {
				return err
			}
		}
	}
	for _, item := range []struct {
		value  frozenPlanValue
		target **int64
	}{{in.Source.TorrentSize, &b.artifact.torrent.Size}, {in.Source.TorrentFileCount, &b.artifact.torrent.FileCount}} {
		if item.value.State == "known" {
			var n int64
			if err := json.Unmarshal(item.value.Value, &n); err != nil {
				return err
			}
			*item.target = &n
		}
	}
	if len(in.Config.Sections) != 6 {
		return fmt.Errorf("%w: incomplete accepted sections", ErrFrozenConfigRequired)
	}
	b.input = append(json.RawMessage(nil), raw...)
	return nil
}

func typedPlanPolicy(sections map[string]json.RawMessage, versions map[string]int64, evaluation namingvars.FrozenEvaluation) ([]byte, error) {
	return json.Marshal(map[string]any{"effective_sections": sections, "section_versions": versions, "vars": map[string]any{"program": evaluation.Program, "input": evaluation.Input, "output": evaluation.Output, "status": evaluation.Status, "diagnostic": evaluation.Diagnostic}})
}

type frozenPlanValue struct {
	State string          `json:"state"`
	Value json.RawMessage `json:"value"`
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
