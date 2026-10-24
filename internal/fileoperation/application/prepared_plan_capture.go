package application

import (
	"bansoko/internal/persistence/txscope"
	"context"
	"database/sql"
	"encoding/json"
)

type preparedCaptureQuery interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// capturePreparedPlan reads only persisted inputs. Its single statement sees
// one MVCC snapshot without holding any transaction across naming/filesystem I/O.
// Keep complete frozen policy (including Vars reference/input/output/status/diag)
// and typed per-file/binding/target facts; never project through active globals.
func capturePreparedPlan(ctx context.Context, q preparedCaptureQuery, planID string) (PreparedPlanCapture, error) {
	var c PreparedPlanCapture
	err := q.QueryRowContext(ctx, `SELECT p.id,p.version,p.input_digest,p.artifact_id,p.artifact_version,
 dt.frozen_policy,
 jsonb_build_object('artifact',to_jsonb(a),'download',to_jsonb(dt),'release',to_jsonb(r),'source',to_jsonb(src),'subscription',to_jsonb(sub),'anime',to_jsonb(anime),'metadata',to_jsonb(m),
 'files',COALESCE((SELECT jsonb_agg(to_jsonb(f) ORDER BY f.id) FROM artifact_files f WHERE f.artifact_id=a.id),'[]'::jsonb),
 'operations',COALESCE((SELECT jsonb_agg(to_jsonb(op) ORDER BY op.id) FROM file_operations op WHERE op.artifact_id=a.id),'[]'::jsonb),
 'media_facts',COALESCE((SELECT jsonb_agg(to_jsonb(mf) ORDER BY mf.id) FROM media_fact_snapshots mf WHERE mf.release_id=r.id),'[]'::jsonb),
 'media_files',COALESCE((SELECT jsonb_agg(to_jsonb(ff) ORDER BY ff.snapshot_id,ff.file_ref) FROM media_fact_files ff JOIN media_fact_snapshots mf ON mf.id=ff.snapshot_id WHERE mf.release_id=r.id),'[]'::jsonb),
 'targets',COALESCE((SELECT jsonb_agg(jsonb_build_object('target',to_jsonb(t),'observation',CASE WHEN t.observation_state IS NULL THEN NULL ELSE jsonb_build_object('state',t.observation_state,'observed_size_bytes',t.observed_size_bytes,'reason_code',t.observation_reason_code,'observed_at',t.observed_at,'version',t.observation_version) END) ORDER BY t.id) FROM acquisition_targets t WHERE t.release_id=r.id),'[]'::jsonb))
 FROM postprocessing_plans p JOIN artifacts a ON a.id=p.artifact_id JOIN download_tasks dt ON dt.id=a.download_task_id JOIN releases r ON r.id=dt.release_id JOIN rss_sources src ON src.id=r.rss_source_id JOIN subscriptions sub ON sub.id=src.subscription_id JOIN managed_anime anime ON anime.id=sub.managed_anime_id JOIN anime_metadata m ON m.managed_anime_id=anime.id WHERE p.id=$1`, planID).Scan(&c.PlanID, &c.PlanVersion, &c.PlanInputDigest, &c.ArtifactID, &c.ArtifactVersion, &c.Config, &c.Facts)
	return c, err
}
func (s *Service) CapturePreparedPlan(ctx context.Context, planID string) (PreparedPlanCapture, error) {
	return capturePreparedPlan(ctx, s.db, planID)
}

// reloadPreparedPlanCaptureTx locks all mutable input rows before re-reading
// the exact persisted input snapshot. NOWAIT avoids retaining claim/plan locks
// while waiting on a writer that acquired the owner graph in another order.
func reloadPreparedPlanCaptureTx(ctx context.Context, scope txscope.Tx, old PreparedPlanCapture) (PreparedPlanCapture, error) {
	tx := scope.SQLTx()
	if _, err := tx.ExecContext(ctx, `SELECT a.id FROM artifacts a JOIN download_tasks dt ON dt.id=a.download_task_id JOIN releases r ON r.id=dt.release_id JOIN rss_sources src ON src.id=r.rss_source_id JOIN subscriptions sub ON sub.id=src.subscription_id JOIN managed_anime anime ON anime.id=sub.managed_anime_id JOIN anime_metadata m ON m.managed_anime_id=anime.id WHERE a.id=$1 FOR SHARE OF dt,r,src,sub,anime,m NOWAIT`, old.ArtifactID); err != nil {
		return PreparedPlanCapture{}, err
	}
	for _, query := range []string{
		`SELECT id FROM artifact_files WHERE artifact_id=$1 ORDER BY id FOR SHARE NOWAIT`,
		`SELECT id FROM file_operations WHERE artifact_id=$1 ORDER BY id FOR UPDATE NOWAIT`,
		`SELECT mf.id FROM media_fact_snapshots mf JOIN download_tasks dt ON dt.release_id=mf.release_id JOIN artifacts a ON a.download_task_id=dt.id WHERE a.id=$1 ORDER BY mf.id FOR SHARE OF mf NOWAIT`,
		`SELECT ff.file_ref FROM media_fact_files ff JOIN media_fact_snapshots mf ON mf.id=ff.snapshot_id JOIN download_tasks dt ON dt.release_id=mf.release_id JOIN artifacts a ON a.download_task_id=dt.id WHERE a.id=$1 ORDER BY ff.snapshot_id,ff.file_ref FOR SHARE OF ff NOWAIT`,
		`SELECT p.managed_anime_id FROM anime_business_configs p JOIN subscriptions sub ON sub.managed_anime_id=p.managed_anime_id JOIN rss_sources src ON src.subscription_id=sub.id JOIN releases r ON r.rss_source_id=src.id JOIN download_tasks dt ON dt.release_id=r.id JOIN artifacts a ON a.download_task_id=dt.id WHERE a.id=$1 FOR SHARE OF p NOWAIT`,
		`SELECT public.target_lock_artifact_snapshot($1)`,
	} {
		if _, err := tx.ExecContext(ctx, query, old.ArtifactID); err != nil {
			return PreparedPlanCapture{}, err
		}
	}
	// This query has no Artifact parameter; do not send the loop's $1 argument.
	if _, err := tx.ExecContext(ctx, `SELECT section_name FROM global_config_sections WHERE section_name='file_effect_policy' FOR SHARE NOWAIT`); err != nil {
		return PreparedPlanCapture{}, err
	}
	return capturePreparedPlan(ctx, tx, old.PlanID)
}

// preparedPlanConfig is intentionally lossless: the full frozen policy passes
// to the existing effectiveConfig decoder, including failed Vars diagnostics.
func preparedPlanConfig(c PreparedPlanCapture) (effectiveConfig, error) {
	return effectiveConfigFromFrozenDownloadPolicy(json.RawMessage(c.Config))
}
