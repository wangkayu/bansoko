package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	protocol "bansoko/internal/acquisition/terminal/domain"
	"bansoko/internal/postprocessing/naming"
	"bansoko/internal/templatefacts"
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
			Year          json.RawMessage `json:"year"`
			Season        json.RawMessage `json:"season"`
			BGM           frozenPlanValue `json:"bgm"`
			TMDB          frozenPlanValue `json:"tmdb"`
			BGMTitles     frozenPlanValue `json:"bgm_titles"`
			TMDBTitles    frozenPlanValue `json:"tmdb_titles"`
		} `json:"media"`
		Source struct {
			Title            frozenPlanValue `json:"title"`
			Group            frozenPlanValue `json:"group"`
			TagEvidence      json.RawMessage `json:"tag_evidence"`
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
	policy, err := json.Marshal(map[string]any{"effective_sections": sections, "section_versions": versions})
	if err != nil {
		return err
	}
	config, err := effectiveConfigFromFrozenDownloadPolicy(policy)
	if err != nil {
		return err
	}
	// Older admissions could freeze block despite an overwrite file-effect
	// setting. Preserve that accepted delivery; new admissions freeze overwrite.
	if config.effect.CollisionPolicy == fileOperationCollisionOverwrite && in.Delivery.Intent == "ensure_present" && in.Delivery.Collision == "block" {
		config.effect.CollisionPolicy = fileOperationCollisionFail
	}
	b.artifact.frozenPolicy = policy
	// Current release/source rows only locate owners. Their changing values
	// must not supply evidence to an already accepted operation.
	b.artifact.sourceFacts = nil
	if len(in.Source.TagEvidence) > 0 {
		evidence, err := protocol.ParseStrict(in.Source.TagEvidence)
		if err != nil {
			return fmt.Errorf("accepted source tags: %w", err)
		}
		if err := protocol.Validate("SourceTagsV1", evidence); err != nil {
			return fmt.Errorf("accepted source tags: %w", err)
		}
		b.artifact.sourceFacts = protocol.Canonical(evidence)
	}
	b.artifact.groupName = ""
	if in.Source.Group.State == "known" {
		if err := json.Unmarshal(in.Source.Group.Value, &b.artifact.groupName); err != nil {
			return err
		}
	}
	var releaseTitle string
	if in.Source.Title.State == "known" {
		if err := json.Unmarshal(in.Source.Title.Value, &releaseTitle); err != nil {
			return err
		}
	}
	b.artifact.rawFacts, err = json.Marshal(map[string]string{"title": releaseTitle})
	if err != nil {
		return err
	}
	// The accepted delivery route mode says nothing about unknown files. The
	// unclassified_route switch is the instance policy, read by
	// captureArtifactBatch; until then unknown files fail closed to skip.
	config.unclassifiedRoute = "skip"
	b.prep = prepareContext{config: config, replanTargets: true, titleTagCache: newFileTitleTagCache()}
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
	if b.artifact.ownerYear, err = decodeFrozenOwnerNumber(in.Media.Year); err != nil {
		return fmt.Errorf("accepted media.year: %w", err)
	}
	if b.artifact.ownerSeason, err = decodeFrozenOwnerNumber(in.Media.Season); err != nil {
		return fmt.Errorf("accepted media.season: %w", err)
	}
	// Only a missing member predates the new fields. An explicitly absent
	// owner season never inherits the TMDB binding.
	b.artifact.ownerSeasonPresent = in.Media.Season != nil
	if in.Media.Year == nil || in.Media.Season == nil {
		legacy := legacyVarsKnownAnime(in.Config.Vars)
		if in.Media.Year == nil {
			var text string
			if json.Unmarshal(legacy["year"], &text) == nil {
				if year, err := strconv.Atoi(strings.TrimSpace(text)); err == nil && year >= templatefacts.MinYear && year <= templatefacts.MaxYear {
					b.artifact.ownerYear = &year
				}
			}
		}
		if in.Media.Season == nil {
			raw, present := legacy["season"]
			b.artifact.ownerSeasonPresent = present
			var season *int
			if present && json.Unmarshal(raw, &season) == nil && season != nil && *season >= 0 {
				b.artifact.ownerSeason = season
			}
		}
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

// legacyVarsKnownAnime reads only the two built-in owner facts from pre-0.3
// accepted inputs. No program, output or evaluation record is needed to plan.
func legacyVarsKnownAnime(raw json.RawMessage) map[string]json.RawMessage {
	var old struct {
		Known struct {
			Facts struct {
				Anime map[string]json.RawMessage `json:"anime"`
			} `json:"facts"`
		} `json:"known_inputs"`
	}
	if json.Unmarshal(raw, &old) != nil {
		return nil
	}
	return old.Known.Facts.Anime
}

type frozenPlanValue struct {
	State string          `json:"state"`
	Value json.RawMessage `json:"value"`
}

func decodeFrozenOwnerNumber(raw json.RawMessage) (*int, error) {
	if raw == nil {
		return nil, nil
	}
	var value frozenPlanValue
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	switch value.State {
	case "absent", "unavailable":
		return nil, nil
	case "known":
		var number *int
		if err := json.Unmarshal(value.Value, &number); err != nil {
			return nil, err
		}
		if number != nil {
			return number, nil
		}
		return nil, fmt.Errorf("known value has no number")
	default:
		return nil, fmt.Errorf("invalid frozen value state %q", value.State)
	}
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
