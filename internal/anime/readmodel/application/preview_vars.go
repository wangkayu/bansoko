package application

import (
	acquisition "bansoko/internal/acquisition/domain"
	"bansoko/internal/attrrules"
	catalog "bansoko/internal/catalog/domain"
	"bansoko/internal/namingvars"
	"bansoko/internal/namingvars/adapter/persistence"
	"context"
	"fmt"
	"path"
)

// loadPreviewVarsProgram reads the exact pinned Vars program, through the cache.
// The program is immutable and safe to reuse concurrently, so one load serves
// every release of every preview that pins the same digest.
func (s *Service) loadPreviewVarsProgram(ctx context.Context, ref namingvars.ProgramRef) (namingvars.Program, error) {
	load := s.loadVarsProgramSource
	if load == nil {
		load = func(ctx context.Context, ref namingvars.ProgramRef) (namingvars.Program, error) {
			if s == nil || s.db == nil {
				return namingvars.Program{}, fmt.Errorf("exact preview vars program database unavailable")
			}
			return namingvars.NewStore(persistence.NewRepository(s.db)).LoadProgram(ctx, ref)
		}
	}
	return s.previewMaterials.programFor(ctx, ref, load)
}

// freezePreviewVars loads the pinned Vars program (cached) and runs it over one release.
func (s *Service) freezePreviewVars(ctx context.Context, ref namingvars.ProgramRef, item acquisition.FeedItem, manifest *catalog.TorrentManifest) (namingvars.FrozenEvaluation, error) {
	program, err := s.loadPreviewVarsProgram(ctx, ref)
	if err != nil {
		return namingvars.FrozenEvaluation{}, err
	}
	evidence, err := s.previewVarsEvidence(ctx, namingvars.AnimeEvidence{}, "", item, nil)
	if err != nil {
		return namingvars.FrozenEvaluation{}, err
	}
	return freezeVarsWithProgram(program, evidence, manifest), nil
}

// varsQuerier is the database the preview evidence loads from; nil when the
// service has none (unit-test previews read only the item).
func (s *Service) varsQuerier() persistence.Querier {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db
}

// previewVarsEvidence assembles one release's Vars evidence the way the
// admission paths do (persistence.ReleaseItemEvidence), loading the
// persisted release's tags and hashes when releaseID names one.
func (s *Service) previewVarsEvidence(ctx context.Context, anime namingvars.AnimeEvidence, releaseID string, item acquisition.FeedItem, episode *namingvars.EpisodeEvidence) (namingvars.ReleaseEvidence, error) {
	return persistence.ReleaseItemEvidence(ctx, s.varsQuerier(), anime, releaseID, item, episode)
}

// freezeVarsWithProgram runs an already-loaded Vars program over one release:
// the shared release evidence plus the exact manifest files.
func freezeVarsWithProgram(program namingvars.Program, evidence namingvars.ReleaseEvidence, manifest *catalog.TorrentManifest) namingvars.FrozenEvaluation {
	evidence.Files = []namingvars.File{}
	if manifest != nil {
		for _, file := range manifest.Files {
			evidence.Files = append(evidence.Files, namingvars.File{Name: path.Base(file.SourcePath), Path: file.SourcePath, Size: file.SizeBytes})
		}
	}
	return program.FreezeEvaluation(namingvars.BuildInput(evidence))
}

// loadPreviewAttrRules resolves the same immutable business source as Vars;
// active publication is neither initialization nor preview authority.
// It is read through the cache and verified against its digest on load.
func (s *Service) loadPreviewAttrRules(ctx context.Context, ref namingvars.ProgramRef) (attrrules.Rules, error) {
	load := s.loadAttrRulesSource
	if load == nil {
		load = s.readPreviewAttrRules
	}
	return s.previewMaterials.attrRulesFor(ctx, ref, load)
}

func (s *Service) readPreviewAttrRules(ctx context.Context, ref namingvars.ProgramRef) (attrrules.Rules, error) {
	if s == nil || s.db == nil {
		return attrrules.Rules{}, fmt.Errorf("exact preview attrrules database unavailable")
	}
	return attrrules.LoadExact(ctx, ref.Version, ref.Digest, func(ctx context.Context, fingerprint string) ([]byte, error) {
		var raw []byte
		err := s.db.QueryRowContext(ctx, `SELECT content FROM attr_rule_versions WHERE fingerprint=$1`, fingerprint).Scan(&raw)
		return raw, err
	})
}
