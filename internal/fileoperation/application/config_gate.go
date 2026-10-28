package application

import (
	"context"
	"encoding/json"
	"sort"
	"time"

	"bansoko/internal/filekinds"
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/postprocessing/naming"
	runtimeworker "bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
)

// The import plan's configuration gate. Before a subscription plan spends an
// attempt it renders the files it places with the anime's current
// configuration; when that configuration cannot render an import path (a
// missing BGM or TMDB binding, a template variable the render does not
// define) the plan waits in waiting_config instead of failing. Waiting spends
// no attempt, creates no file operation and touches no file: the downloaded
// files stay where they are. A configuration write wakes the waiting plans of
// its anime in its own transaction, the periodic sweep wakes the rest, and the
// next claim checks again. The plan still executes its frozen TargetSet once
// the configuration is complete.

const (
	planWaitingConfigCode = "PLAN_WAITING_CONFIG"
	// planConfigPark is how long a waiting plan's work item stays parked
	// when nothing wakes it; the sweep runs well within it.
	planConfigPark = time.Hour
	// planConfigHoldItems is the hold's item bound (the migration 097 shape).
	planConfigHoldItems = 16
)

// CurrentConfigGate reports what the anime's current configuration lacks to
// render the given import files: each missing item with the number of files
// it blocks, and the number of files any item blocks. The composition root
// wires the file maintenance renderer, whose current-configuration render the
// gate predicts.
type CurrentConfigGate interface {
	CurrentConfigHold(ctx context.Context, animeID string, files []naming.NamingContext) (map[string]int, int, error)
}

// SetCurrentConfigGate wires the configuration gate. Without one no plan
// waits.
func (s *Service) SetCurrentConfigGate(gate CurrentConfigGate) {
	if s != nil {
		s.configGate = gate
	}
}

// configHoldItem is one missing item of a waiting plan's hold.
type configHoldItem struct {
	Item  string `json:"item"`
	Files int    `json:"files"`
}

// configHold orders the gate's items by name — the BGM binding, the TMDB
// binding, then the template items — within the hold's bounds.
func configHold(counts map[string]int) []configHoldItem {
	hold := make([]configHoldItem, 0, len(counts))
	for item, files := range counts {
		if files > 0 {
			hold = append(hold, configHoldItem{Item: item, Files: min(files, 999999)})
		}
	}
	sort.Slice(hold, func(i, j int) bool { return hold[i].Item < hold[j].Item })
	if len(hold) > planConfigHoldItems {
		hold = hold[:planConfigHoldItems]
	}
	return hold
}

// configurationGate runs the gate for one plan claim. A plan that waits (or
// was cancelled instead) returns its committed outcome; nil lets the claim
// plan, after a waiting plan whose configuration is complete again — or
// cannot be checked — has gone back to queued. One-shot plans never wait.
func (s *Service) configurationGate(ctx context.Context, claim workitems.Claim, plan *postProcessingPlanRow) (*runtimeworker.Outcome, error) {
	if plan.oneShotID.Valid {
		return nil, nil
	}
	hold, blocked, err := s.currentConfigHold(ctx, plan)
	if err != nil {
		return nil, err
	}
	if len(hold) == 0 {
		if plan.state == "waiting_config" {
			return nil, s.resumeConfigWait(ctx, claim)
		}
		return nil, nil
	}
	out, err := s.waitForConfiguration(ctx, claim, hold, blocked)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// currentConfigHold checks the plan's files against the current
// configuration. A plan whose capture cannot be read here holds nothing: the
// claim's own capture reports it.
func (s *Service) currentConfigHold(ctx context.Context, plan *postProcessingPlanRow) ([]configHoldItem, int, error) {
	if s.configGate == nil {
		return nil, 0, nil
	}
	batch, err := s.captureArtifactBatch(ctx, plan)
	if err != nil {
		return nil, 0, nil
	}
	files := configGateFiles(batch)
	if len(files) == 0 {
		return nil, 0, nil
	}
	counts, blocked, err := s.configGate.CurrentConfigHold(ctx, batch.artifact.managedAnimeID, files)
	if err != nil {
		return nil, 0, err
	}
	return configHold(counts), min(blocked, 999999), nil
}

// configGateFiles are the naming contexts of the videos the plan renders an
// episode or movie target for, selected as prepare selects them: eligible
// videos the requirements admit and the file rules keep, not covered or
// dropped by the frozen batch plan, with a frozen episode (a movie: its main
// video). Each context carries the frozen release and file facts.
// Subtitles, unknown files and the subtitle suffix are not checked.
func configGateFiles(b *artifactPreparedBatch) []naming.NamingContext {
	artifact, prep, frozen := b.artifact, b.prep, b.frozen
	artifact.frozenReleaseResolution = frozen.releaseResolution
	applyFrozenBinding(&artifact, frozen)
	selection := newFileSelection(artifact.frozenInput, frozen.fileKindsVersion, frozen.plan.Selection())
	videos := make([]artifactFileContext, 0, len(artifact.files))
	for _, file := range artifact.files {
		kind, ignored := selection.classify(file)
		if ignored == "" && kind == filekinds.KindVideo && prep.admitsFile(file.fileRef) && fileAllowedByFilter(file, prep.config.fileFilter) {
			videos = append(videos, file)
		}
	}
	sort.SliceStable(videos, func(i, j int) bool { return videos[i].relativePath < videos[j].relativePath })
	movie := artifact.tmdbMediaKind.Valid && artifact.tmdbMediaKind.String == "movie"
	extras := movieExtras(artifact, videos)
	drift := planDrift(frozen.plan, selection, videos, frozen.coverage, movie)
	files := make([]naming.NamingContext, 0, len(videos))
	for _, video := range videos {
		episode := frozen.episodes[video.fileRef]
		if extras[video.fileRef] || (!movie && episode == nil) || planExclusion(frozen.plan, selection, video, drift) != "" {
			continue
		}
		facts, err := namingContextFacts(artifact, video, episode, prep.config.vars)
		if err != nil {
			// The plan fails this file itself.
			continue
		}
		files = append(files, facts)
	}
	return files
}

// waitForConfiguration records the hold and the files it blocks and returns
// the work item, parked, with this claim's attempt refunded.
func (s *Service) waitForConfiguration(ctx context.Context, c workitems.Claim, hold []configHoldItem, blocked int) (runtimeworker.Outcome, error) {
	payload, err := json.Marshal(hold)
	if err != nil {
		return runtimeworker.Outcome{}, err
	}
	var result workitems.OwnerResult
	err = s.workItems.WithTypedStageTransaction(ctx, c, func(scope txscope.Tx) (workitems.EffectDisposition, error) {
		var raw []byte
		if err := scope.SQLTx().QueryRowContext(ctx, `SELECT public.postprocessing_plan_wait_config($1,$2,$3,$4::jsonb,$5,$6)`, c.Owner.ID, c.ID, c.FencingToken, string(payload), blocked, planConfigPark.Seconds()).Scan(&raw); err != nil {
			return "", err
		}
		var reply struct {
			State string `json:"state"`
			Code  string `json:"reason_code"`
		}
		if err := json.Unmarshal(raw, &reply); err != nil {
			return "", err
		}
		result = workitems.OwnerResult{State: reply.State, ReasonCode: reply.Code}
		return workitems.DispositionNoExternalEffect, nil
	})
	if err != nil {
		return runtimeworker.Outcome{}, err
	}
	out := runtimeworker.Committed(c, result)
	out.Disposition = workitems.DispositionNoExternalEffect
	return out, nil
}

// resumeConfigWait puts a waiting plan back to queued within the claim.
func (s *Service) resumeConfigWait(ctx context.Context, c workitems.Claim) error {
	return s.workItems.WithTypedStageTransaction(ctx, c, func(scope txscope.Tx) (workitems.EffectDisposition, error) {
		var raw []byte
		if err := scope.SQLTx().QueryRowContext(ctx, `SELECT public.postprocessing_plan_config_resume($1,$2,$3)`, c.Owner.ID, c.ID, c.FencingToken).Scan(&raw); err != nil {
			return workitems.DispositionAmbiguous, err
		}
		// The claim goes on to plan; nothing is settled here.
		return workitems.DispositionAmbiguous, nil
	})
}
