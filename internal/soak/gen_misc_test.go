//go:build soakscale

package soak

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	deliverypayload "bansoko/internal/plugintarget/payload"
)

// generateMisc writes everything that is not the owner/release/download graph:
// the work item history, the notification deliveries, the observability tables,
// provider snapshots and the file manager / reconciliation tables.
func (g *generator) generateMisc(ctx context.Context) {
	p := g.p
	pendingDeliveries := p.PluginDeliveries * 3 / 10
	if pendingDeliveries > 6000 {
		pendingDeliveries = 6000
	}

	// ---- plugin targets and deliveries ---------------------------------------------
	g.exec("secret_values + plugin targets", `
INSERT INTO secret_values(ref, purpose, nonce, ciphertext)
VALUES ('secret-plugin-1', 'plugin_target', decode('000102030405060708090a0b', 'hex'), decode('00112233445566778899aabbccddeeff', 'hex'));
INSERT INTO plugin_target_configs(id, name, plugin_kind, adapter_kind, enabled, trigger_kinds, config_schema_version, config, template_set,
                                  secret_ref, version, signing_secret_set)
VALUES ('t1', 'Soak webhook', 'notification', 'webhook', true,
        '["download_submitted","file_operation_completed","file_operation_failed","release_discovered","download_failed","subscription_changed","unknown_files_imported","replacement_deletion_failed"]'::jsonb,
        1, '{"adapter_kind":"webhook","auth_kind":"none"}'::jsonb, '{}'::jsonb, 'secret-plugin-1', 1, false);
`)

	g.execf("plan: deliveries", `
CREATE TABLE soak_ext.dl AS
SELECT q.*, r.aid, r.id AS rel_id, r.title
FROM (
  SELECT n AS rn, 'pd-' || lpad(n::text, 6, '0') AS id,
         CASE WHEN n <= %[2]d THEN 'pending'
              WHEN soak_ext.rnd('dls', n) < 0.015 THEN 'failed' WHEN soak_ext.rnd('dls', n) < 0.020 THEN 'unknown'
              WHEN soak_ext.rnd('dls', n) < 0.030 THEN 'waiting_retry' ELSE 'delivered' END AS state,
         CASE WHEN n <= %[2]d THEN %[1]s - make_interval(mins => (soak_ext.rnd('dlt', n) * 120)::int)
              WHEN soak_ext.rnd('dlx', n) < 0.03 THEN %[1]s - make_interval(mins => 31 * 1440 + (soak_ext.rnd('dlt', n) * 14 * 1440)::int)
              ELSE %[1]s - make_interval(mins => 130 + (soak_ext.rnd('dlt', n) * (30 * 1440 - 130))::int) END AS created_at
  FROM generate_series(1, %[3]d) n
) q JOIN soak_ext.rel r ON r.gn = soak_ext.pick('dlrel', q.rn, %[4]d)`, g.baseSQL, pendingDeliveries, p.PluginDeliveries, g.scalarInt(`SELECT count(*) FROM soak_ext.rel`))
	g.exec("plan: dl pkey", `ALTER TABLE soak_ext.dl ADD PRIMARY KEY (rn)`)
	g.execf("plugin_deliveries", `
INSERT INTO plugin_deliveries(id, plugin_target_config_id, plugin_target_config_version, adapter_kind, adapter_config_snapshot, secret_ref,
                              delivery_kind, trigger_kind, source_owner_type, source_owner_id, source_owner_version, source_result_key,
                              managed_anime_id, state, version, idempotency_key, payload_schema_version, payload, payload_digest,
                              template_language_version, template_catalog_version, title_template_snapshot, body_template_snapshot,
                              rendered_title, rendered_body, rendered_message_digest, target_display, attempt_count, last_error_code,
                              last_error, delivered_at, created_at, updated_at, retention_until, next_attempt_at)
SELECT d.id, 't1', 1, 'webhook', '{"adapter_kind":"webhook","auth_kind":"none"}'::jsonb, 'secret-plugin-1',
       'business',
       (ARRAY['download_submitted','file_operation_completed','file_operation_failed','release_discovered','download_failed','subscription_changed','unknown_files_imported','replacement_deletion_failed'])[1 + (d.rn %% 8)],
       'release', d.rel_id, 1, 'r' || d.rn, d.aid, d.state, 1, 'pdk-' || d.id, 1,
       jsonb_build_object('schema_version', 1, 'release_title', d.title, 'pad', soak_ext.pad(d.rn, 300)),
       soak_ext.dg('soak-payload', d.id), 'bansoko-template-1', 'notification-catalog-v1', '{{title}}', '{{body}}',
       CASE WHEN d.state <> 'pending' THEN d.title END, CASE WHEN d.state <> 'pending' THEN 'new release for ' || d.title END,
       CASE WHEN d.state <> 'pending' THEN soak_ext.dg('soak-rendered', d.id) END, 'Soak webhook',
       CASE d.state WHEN 'pending' THEN 0 WHEN 'delivered' THEN 1 ELSE 3 END,
       CASE WHEN d.state IN ('failed', 'unknown', 'waiting_retry') THEN 'webhook_failed' END,
       CASE WHEN d.state IN ('failed', 'unknown', 'waiting_retry') THEN 'connection refused' END,
       CASE WHEN d.state = 'delivered' THEN d.created_at + interval '30 seconds' END,
       d.created_at, d.created_at + interval '30 seconds', d.created_at + interval '30 days',
       CASE WHEN d.state = 'waiting_retry' THEN %[1]s + interval '10 minutes' END
FROM soak_ext.dl d`, g.baseSQL)
	// The frozen payload is the canonical delivery payload (the plugin payload codec owns the shape and the digest).
	var payloadCases, digestCases strings.Builder
	for _, tk := range []string{"download_submitted", "file_operation_completed", "file_operation_failed", "release_discovered", "download_failed", "subscription_changed", "unknown_files_imported", "replacement_deletion_failed"} {
		raw, err := deliverypayload.Encode(tk, map[string]any{"release_title": "Soak Anime", "source": "soak"})
		if err != nil {
			g.t.Fatalf("encode delivery payload: %v", err)
		}
		sum := sha256.Sum256(raw)
		fmt.Fprintf(&payloadCases, " WHEN '%s' THEN '%s'::jsonb", tk, raw)
		fmt.Fprintf(&digestCases, " WHEN '%s' THEN 'sha256:%s'", tk, hex.EncodeToString(sum[:]))
	}
	g.exec("plugin_deliveries: canonical payloads", `UPDATE plugin_deliveries SET payload = CASE trigger_kind`+payloadCases.String()+` END, payload_digest = CASE trigger_kind`+digestCases.String()+` END`)
	g.execf("plugin_delivery_attempts", `
INSERT INTO plugin_delivery_attempts(id, plugin_delivery_id, attempt_number, started_at, request_started_at, response_received_at, duration_ms,
                                     target_display, payload_digest, request_summary, response_status, response_preview, error_code, error,
                                     retention_until)
SELECT 'pa-' || x.rn || '-' || x.an, x.id, x.an, x.st, x.st, x.st + interval '300 milliseconds', 300, 'Soak webhook', soak_ext.dg('soak-payload', x.id),
       '{"method":"POST"}'::jsonb, CASE WHEN x.state = 'delivered' AND x.an = 1 THEN 200 ELSE 502 END, 'ok',
       CASE WHEN x.state <> 'delivered' OR x.an > 1 THEN 'webhook_failed' END, CASE WHEN x.state <> 'delivered' OR x.an > 1 THEN 'bad gateway' END,
       x.st + interval '14 days'
FROM (
  SELECT d.rn, d.id, d.state, a.an,
         CASE WHEN soak_ext.rnd('atx', d.rn * 4 + a.an) < 0.2 THEN %[1]s - make_interval(mins => 14 * 1440 + 60 + (soak_ext.rnd('att', d.rn * 4 + a.an) * 14 * 1440)::int)
              ELSE %[1]s - make_interval(mins => (soak_ext.rnd('att', d.rn * 4 + a.an) * 14 * 1440)::int) END AS st
  FROM soak_ext.dl d CROSS JOIN LATERAL generate_series(1, CASE WHEN soak_ext.rnd('anum', d.rn) < 0.8 THEN 2 ELSE 1 END) a(an)
  WHERE d.state <> 'pending'
) x
LIMIT %[2]d`, g.baseSQL, p.PluginAttempts)

	// ---- work items beyond the live rss_refresh rows --------------------------------
	g.exec("plan: src_rn", `CREATE TABLE soak_ext.src_rn AS SELECT row_number() OVER (ORDER BY id) AS rn, id, aid FROM soak_ext.src WHERE NOT oneshot`)
	g.exec("plan: acq_rn", `CREATE TABLE soak_ext.acq_rn AS SELECT row_number() OVER (ORDER BY id) AS rn, id, rel_id, aid, cls FROM soak_ext.acq WHERE NOT oneshot`)
	g.exec("plan: pk", `ALTER TABLE soak_ext.src_rn ADD PRIMARY KEY (rn); ALTER TABLE soak_ext.acq_rn ADD PRIMARY KEY (rn)`)
	nSrc := g.scalarInt(`SELECT count(*) FROM soak_ext.src_rn`)
	nAcq := g.scalarInt(`SELECT count(*) FROM soak_ext.acq_rn`)
	nRel := g.scalarInt(`SELECT count(*) FROM soak_ext.rel`)
	// delivered deliveries addressable by rank
	g.exec("plan: dl_rn", `CREATE TABLE soak_ext.dl_rn AS SELECT row_number() OVER (ORDER BY rn) AS rn, id, aid FROM soak_ext.dl WHERE state <> 'pending'`)
	g.exec("plan: dl_rn pkey", `ALTER TABLE soak_ext.dl_rn ADD PRIMARY KEY (rn)`)
	// A notification delivery outlives its work item (30 d against 7 d): terminal deliver items may only point at deliveries
	// created in their own window, or the janitor's delivery purge would hit the work item foreign key.
	g.execf("plan: dl_rn_recent", `CREATE TABLE soak_ext.dl_rn_recent AS SELECT row_number() OVER (ORDER BY rn) AS rn, id, aid FROM soak_ext.dl WHERE state <> 'pending' AND created_at >= %[1]s - interval '7 days'`, g.baseSQL)
	g.execf("plan: dl_rn_old", `CREATE TABLE soak_ext.dl_rn_old AS SELECT row_number() OVER (ORDER BY rn) AS rn, id, aid FROM soak_ext.dl WHERE state <> 'pending' AND created_at < %[1]s - interval '7 days' AND created_at >= %[1]s - interval '37 days'`, g.baseSQL)
	g.exec("plan: dl_rn windows pkey", `ALTER TABLE soak_ext.dl_rn_recent ADD PRIMARY KEY (rn); ALTER TABLE soak_ext.dl_rn_old ADD PRIMARY KEY (rn)`)
	nDlRecent := g.scalarInt(`SELECT count(*) FROM soak_ext.dl_rn_recent`)
	nDlOld := g.scalarInt(`SELECT count(*) FROM soak_ext.dl_rn_old`)

	// available (live) items: release_evaluate backlog, pending notification deliveries, qB sync of running downloads.
	runningTasks := g.scalarInt(`SELECT count(*) FROM soak_ext.acq WHERE cls = 'running' AND NOT oneshot`)
	reEval := int64(p.WorkAvailable) - int64(p.Sources) - pendingDeliveries64(pendingDeliveries) - runningTasks
	if reEval < 0 {
		reEval = 0
	}
	g.execf("work_items: release_evaluate (live)", `
INSERT INTO work_items(id, owner_type, owner_id, kind, state, available_at, fencing_token, idempotency_key, attempt, max_attempts,
                       input_snapshot, expected_owner_version, retention_days, created_at, updated_at, managed_anime_id, owner_lifecycle_epoch)
SELECT 'wi-re-' || r.id, 'release', r.id, 'release_evaluate', 'available',
       CASE WHEN soak_ext.rnd('reav', r.gn) < 0.7 THEN %[1]s - make_interval(mins => (soak_ext.rnd('reav2', r.gn) * 120)::int)
            ELSE %[1]s + make_interval(mins => (soak_ext.rnd('reav2', r.gn) * 60)::int) END,
       0, 'release_evaluate:' || r.id, 0, 5, '{"trigger":"refresh"}'::jsonb, 1, 7, %[1]s - interval '3 hours', %[1]s - interval '3 hours', r.aid, 1
FROM (SELECT * FROM soak_ext.rel WHERE NOT oneshot ORDER BY discovered_at DESC, id LIMIT %[2]d) r`, g.baseSQL, reEval)
	g.execf("work_items: plugin_deliver (live)", `
INSERT INTO work_items(id, owner_type, owner_id, kind, state, available_at, fencing_token, idempotency_key, attempt, max_attempts,
                       input_snapshot, expected_owner_version, retention_days, created_at, updated_at, managed_anime_id, owner_lifecycle_epoch)
SELECT 'wi-pd-' || d.id, 'plugin_delivery', d.id, 'plugin_deliver', 'available', d.created_at, 0, 'plugin_deliver:' || d.id, 0, 5,
       '{}'::jsonb, 1, 7, d.created_at, d.created_at, d.aid, 1
FROM soak_ext.dl d WHERE d.state = 'pending'`)
	g.exec("work_items: qb_sync (live)", `
INSERT INTO work_items(id, owner_type, owner_id, kind, state, available_at, fencing_token, idempotency_key, attempt, max_attempts,
                       input_snapshot, expected_owner_version, retention_days, created_at, updated_at, acquisition_id, managed_anime_id, owner_lifecycle_epoch)
SELECT 'wi-qs-' || a.rel_id, 'download_task', 'dt-' || a.rel_id, 'qb_sync', 'available', a.accepted_at + interval '1 minute', 0,
       'dt-key:' || a.rel_id, 0, 5, '{}'::jsonb, 1, 7, a.accepted_at, a.accepted_at, a.id, a.aid, 1
FROM soak_ext.acq a WHERE a.cls = 'running' AND NOT a.oneshot`)

	// terminal history. kinds follow the production mix, rss_refresh dominating.
	termSQL := func(count int, expired bool) string {
		window := "make_interval(mins => (soak_ext.rnd('wtt', w) * 7 * 1440)::int)"
		offset := "0"
		if expired {
			window = "make_interval(mins => 60 + (soak_ext.rnd('wtt', w) * 30 * 1440)::int)"
			offset = "7 * 1440"
		}
		_ = offset
		tag := "wt"
		dlTable, dlCount := "dl_rn_recent", nDlRecent
		if expired {
			tag = "wx"
			dlTable, dlCount = "dl_rn_old", nDlOld
		}
		base := "%[1]s"
		termAt := base + " - " + window
		if expired {
			termAt = base + " - interval '7 days' - " + window
		}
		return `
INSERT INTO work_item_outcomes(id, owner_type, owner_id, acquisition_id, kind, state,
                       available_at, fencing_token, idempotency_key, attempt, max_attempts, input_snapshot, expected_owner_version,
                       last_error_code, last_error_reason, terminal_at, retention_days, retention_until, created_at, updated_at,
                       managed_anime_id, owner_lifecycle_epoch)
SELECT '` + tag + `-' || x.w,
       CASE x.kind WHEN 'rss_refresh' THEN 'rss_source_runtime' WHEN 'release_evaluate' THEN 'release' WHEN 'plugin_deliver' THEN 'plugin_delivery' ELSE 'download_task' END,
       CASE x.kind WHEN 'rss_refresh' THEN s.id WHEN 'release_evaluate' THEN r.id WHEN 'plugin_deliver' THEN d.id ELSE 'dt-' || a.rel_id END,
       CASE WHEN x.kind = 'qb_sync' THEN a.id END, x.kind, x.state,
       x.term_at - interval '1 minute', 1,
       CASE x.kind WHEN 'rss_refresh' THEN 'rss_refresh:' || s.id || ':' || x.w WHEN 'release_evaluate' THEN 'release_evaluate:' || r.id || ':' || x.w
                   WHEN 'plugin_deliver' THEN 'plugin_deliver:' || d.id || ':' || x.w ELSE 'dt-key:' || a.rel_id END,
       1, 5, '{"trigger":"scheduled"}'::jsonb, 1,
       CASE WHEN x.state = 'failed' THEN 'handler_failed' END, CASE WHEN x.state = 'failed' THEN 'the handler reported a failure' END,
       x.term_at, 7, x.term_at + (7::double precision * interval '1 day'), x.term_at - interval '2 minutes', x.term_at,
       COALESCE(s.aid, r.aid, d.aid, a.aid), 1
FROM (
  SELECT w,
         CASE WHEN soak_ext.rnd('wk', w) < 0.74 THEN 'rss_refresh' WHEN soak_ext.rnd('wk', w) < 0.88 THEN 'release_evaluate'
              WHEN soak_ext.rnd('wk', w) < 0.96 THEN 'plugin_deliver' ELSE 'qb_sync' END AS kind,
         CASE WHEN soak_ext.rnd('wf', w) < 0.03 THEN 'failed' ELSE 'succeeded' END AS state,
         ` + termAt + ` AS term_at
  FROM generate_series(1, ` + itoa(count) + `) w
) x
LEFT JOIN soak_ext.src_rn s ON x.kind = 'rss_refresh' AND s.rn = soak_ext.pick('wsrc', x.w, ` + itoa64(nSrc) + `)
LEFT JOIN soak_ext.rel r ON x.kind = 'release_evaluate' AND r.gn = soak_ext.pick('wrel', x.w, ` + itoa64(nRel) + `)
LEFT JOIN soak_ext.` + dlTable + ` d ON x.kind = 'plugin_deliver' AND d.rn = soak_ext.pick('wdl', x.w, ` + itoa64(max64(dlCount, 1)) + `)
LEFT JOIN soak_ext.acq_rn a ON x.kind = 'qb_sync' AND a.rn = soak_ext.pick('wacq', x.w, ` + itoa64(max64(nAcq, 1)) + `)
WHERE (x.kind <> 'qb_sync' OR a.rn IS NOT NULL) AND (x.kind <> 'plugin_deliver' OR d.rn IS NOT NULL)`
	}
	graphItems := int(g.scalarInt(`SELECT count(*) FROM work_item_outcomes WHERE state = 'succeeded' AND acquisition_id LIKE 'acq-%'`))
	retained := p.WorkTerminal - graphItems
	if retained < 0 {
		retained = 0
	}
	g.execf("work_items: terminal (retained)", termSQL(retained, false), g.baseSQL)
	g.execf("work_items: terminal (expired)", termSQL(p.WorkExpired, true), g.baseSQL)

	// ---- subscription log entries ---------------------------------------------------
	g.execf("subscription_log_entries", `
INSERT INTO subscription_log_entries(id, subscription_id, managed_anime_id, kind, outcome, occurred_at, title, summary, rss_source_id, release_id,
                                     details, dedupe_key, retention_until, created_at, reason_code, subject_type, subject_id, subject_version)
SELECT 'lg-' || x.w, o.sid, o.aid, x.kind, CASE WHEN x.kind = 'rss_refresh_failed' THEN 'failure' WHEN x.kind IN ('release_processed', 'download_changed') THEN 'success' ELSE 'info' END,
       x.occ, 'Soak Anime ' || o.n, 'event ' || x.kind || ' for owner ' || o.n,
       NULL, NULL, jsonb_build_object('pad', soak_ext.pad(x.w, 160), 'kind', x.kind), 'lg:' || x.w, x.occ + interval '30 days', x.occ, x.kind,
       'subscription', o.sid, 1
FROM (
  SELECT w,
         (ARRAY['rss_new_content','rss_refresh_failed','release_processed','download_changed','artifact_verified','placement_changed','acquisition_settled'])[soak_ext.pick('lgk', w, 7)] AS kind,
         soak_ext.pick('lgo', w, %[2]d) AS owner_n,
         CASE WHEN w <= %[3]d THEN %[1]s - make_interval(mins => 31 * 1440 + (soak_ext.rnd('lgt', w) * 30 * 1440)::int)
              ELSE %[1]s - make_interval(mins => 30 + (soak_ext.rnd('lgt', w) * (30 * 1440 - 60))::int) END AS occ
  FROM generate_series(1, %[4]d) w
) x
JOIN soak_ext.owner o ON o.n = LEAST(x.owner_n, %[2]d)`, g.baseSQL, p.Owners, p.LogExpired, p.LogEntries)

	// ---- other observability and operational tables ----------------------------------
	g.execf("automation_dispatch_runs", `
INSERT INTO automation_dispatch_runs(id, kind, trigger, state, target_count, enqueued_count, merged_count, skipped_count, dispatch_error_count,
                                     started_at, finished_at, retention_until, created_at, updated_at)
SELECT 'dr-' || w, (ARRAY['rss_refresh','rss_refresh','rss_refresh','rss_refresh'])[1 + (w %% 4)], 'scheduled', 'completed', %[3]d, %[3]d, 0, 0, 0,
       t, t + interval '2 seconds', t + interval '2 seconds' + interval '30 days', t, t + interval '2 seconds'
FROM (SELECT w, CASE WHEN w <= %[2]d / 8 THEN %[1]s - make_interval(mins => 31 * 1440 + (soak_ext.rnd('dr', w) * 10 * 1440)::int)
                     ELSE %[1]s - make_interval(mins => 30 + (soak_ext.rnd('dr', w) * 29 * 1440)::int) END AS t
      FROM generate_series(1, %[2]d) w) q`, g.baseSQL, p.DispatchRuns, p.Sources)
	g.execf("routine_activities", `
INSERT INTO routine_activities(id, kind, scope_kind, scope_id, managed_anime_id, outcome, last_executed_at, last_result_code, summary, details, updated_at)
SELECT 'ra-' || w, (ARRAY['catalog_refresh','provider_refresh','rss_refresh','metadata_match','automation_dispatch','file_manager_operation'])[1 + (w %% 6)],
       'component', 'c-' || w, NULL, CASE WHEN soak_ext.rnd('ro', w) < 0.9 THEN 'success' ELSE 'failure' END,
       %[1]s - make_interval(mins => (soak_ext.rnd('rt', w) * 90 * 1440)::int), 'completed', 'routine activity ' || w,
       jsonb_build_object('pad', soak_ext.pad(w, 120)), %[1]s
FROM generate_series(1, %[2]d) w`, g.baseSQL, p.Routines)
	g.execf("diagnostic_episodes", `
INSERT INTO diagnostic_episodes(id, severity, component, operation, error_code, scope_kind, scope_id, managed_anime_id, first_failed_at, last_failed_at,
                                occurrence_count, last_error, details, succeeded_after_at, retention_until)
SELECT 'de-' || w, CASE WHEN w %% 4 = 0 THEN 'error' ELSE 'warning' END, 'worker', 'rss_refresh', 'feed_unreachable', 'rss_source', 'src-' || w, NULL,
       lf - interval '1 hour', lf, 1 + (w %% 20), 'feed unreachable',
       NULL, CASE WHEN w %% 3 <> 0 THEN lf + interval '10 minutes' END,
       CASE WHEN w %% 3 <> 0 THEN lf + interval '10 minutes' + CASE WHEN w %% 7 = 0 THEN interval '-1 day' ELSE interval '14 days' END
            ELSE lf + interval '14 days' END
FROM (SELECT w, %[1]s - make_interval(mins => 60 + (soak_ext.rnd('de', w) * 25 * 1440)::int) AS lf FROM generate_series(1, %[2]d) w) q`, g.baseSQL, p.Episodes)

	// ---- provider snapshots -----------------------------------------------------------
	seasons := `(ARRAY['2025-autumn','2026-winter','2026-spring','2026-summer'])`
	g.execf("provider_catalog_snapshots", `
INSERT INTO provider_catalog_snapshots(id, provider, season, status, observed_at, source_version, contract_version, health_state, health_checked_at,
                                       version, retention_until)
SELECT 'cs-' || pr || '-' || s || '-' || v, pr, %[2]s[s], 'fresh', t, 'v' || v, 'catalog-v1', 'ready', t, v,
       CASE WHEN v <= %[3]d THEN %[1]s - make_interval(hours => 1 + (v %% 20)) ELSE t + interval '30 days' END
FROM (SELECT pr, s, v, %[1]s - make_interval(days => 30 - v, mins => (soak_ext.rnd('cs', v * 100 + s) * 600)::int) AS t
      FROM unnest(ARRAY['anibt','mikan']) pr, generate_series(1, 4) s, generate_series(1, %[4]d) v) q`,
		g.baseSQL, seasons, expiredPerGroup(p), maxInt(p.CatalogSnapshots/8, 1))
	g.execf("provider_catalog_entries", `
INSERT INTO provider_catalog_entries(id, snapshot_id, provider, provider_entry_ref, bgm_id, title, original_title, source_hint, state, observed_at)
SELECT s.id || '-' || e, s.id, s.provider, (1000 + e)::text, (300000 + e)::text, 'Catalog Title ' || e, 'Original Title ' || e,
       jsonb_build_object('episodes', 12 + (e %% 2) * 12, 'cover', 'https://img.soak.invalid/' || e || '.jpg', 'broadcast_day', (ARRAY['mon','tue','wed','thu','fri','sat','sun'])[1 + (e %% 7)]),
       CASE WHEN soak_ext.rnd('ce', e) < 0.97 THEN 'present' ELSE 'withdrawn' END, s.observed_at
FROM provider_catalog_snapshots s CROSS JOIN generate_series(0, %d - 1) e`, p.CatalogEntriesPer)

	g.execf("provider_group_snapshots", `
INSERT INTO provider_group_snapshots(id, provider, catalog_entry_ref, observed_at, source_version, schema_version, group_count, version, created_at, retention_until, entries)
SELECT 'gs-' || w, (ARRAY['anibt','mikan'])[1 + (w %% 2)], (1000 + (w %% 400))::text, t, 'v' || (w / 400), 'group-obs-v1', 4, 1 + (w / 400), t,
       t + interval '30 days',
       (SELECT COALESCE(jsonb_agg(jsonb_build_object('group_ref', 'grp-' || g, 'display_name', 'Group ' || g,
            'feed_locator', jsonb_build_object('url', 'https://feeds.soak.invalid/gs-' || w || '/' || g || '.xml'),
            'observed_at', t) ORDER BY g), '[]'::jsonb) FROM generate_series(0, 3) g)
FROM (SELECT w, CASE WHEN soak_ext.rnd('gsx', w) < 0.06 THEN %[1]s - make_interval(mins => 31 * 1440 + (soak_ext.rnd('gs', w) * 9 * 1440)::int)
                     ELSE %[1]s - make_interval(mins => (soak_ext.rnd('gs', w) * 29 * 1440)::int) END AS t FROM generate_series(0, %[2]d - 1) w) q`, g.baseSQL, p.GroupSnapshots)
	g.execf("provider_feed_snapshots", `
INSERT INTO provider_feed_snapshots(id, provider, provider_entry_ref, provider_group_ref, version, fetched_at, content_hash, fresh_until, retention_until,
                                    seed_count, created_at, projection_schema)
SELECT 'fs-' || w, (ARRAY['anibt','mikan'])[1 + (w %% 2)], (1000 + (w %% 300))::text, 'grp-' || (w %% 4), 1 + (w / 300), t, soak_ext.dg('soak-feed', w::text), t + interval '15 minutes',
       t + interval '30 days', 6, t, 'torrent-manifest-v1'
FROM (SELECT w, CASE WHEN soak_ext.rnd('fsx', w) < 0.06 THEN %[1]s - make_interval(mins => 31 * 1440 + (soak_ext.rnd('fs', w) * 9 * 1440)::int)
                     ELSE %[1]s - make_interval(mins => (soak_ext.rnd('fs', w) * 29 * 1440)::int) END AS t FROM generate_series(0, %[2]d - 1) w) q`, g.baseSQL, p.FeedSnapshots)
	g.exec("provider_feed_snapshot_seeds", `
INSERT INTO provider_feed_snapshot_seeds(id, snapshot_id, stable_seed_key, locator, locator_display, title, published_at, size_bytes, created_at)
SELECT s.id || '-' || e, s.id, 'seed-' || e, 'https://feeds.soak.invalid/dl/' || s.id || '-' || e || '.torrent', 'https://feeds.soak.invalid/dl/' || e || '.torrent',
       'Seed Title ' || e, s.fetched_at, 400000000 + e * 1000, s.fetched_at
FROM provider_feed_snapshots s CROSS JOIN generate_series(0, 5) e`)

	// ---- file manager, reconciliation ----------------------------------------------------
	g.execf("file_manager_operations", `
INSERT INTO file_manager_operations(id, idempotency_key, request_digest, actor, operation, root_key, root_path, target_root_key, target_root_path,
                                    source_path, target_path, expected_size_bytes, collision, state, failure_code, version, created_at, updated_at, completed_at)
SELECT 'fm-' || w, 'fmk-' || w, soak_ext.hexn('fm', w, 64), 'owner', 'move', 'library', '%[3]s', 'library', '%[3]s',
       '%[3]s/Unsorted/file-' || w || '.mkv', '%[3]s/Soak Anime ' || (w %% 400) || '/moved-' || w || '.mkv', 500000000, 'block',
       st, CASE WHEN st IN ('blocked', 'failed') THEN 'target_exists' END, 1, t, t, CASE WHEN st IN ('completed', 'blocked', 'failed') THEN t + interval '1 second' END
FROM (SELECT w, %[1]s - make_interval(mins => (soak_ext.rnd('fm', w) * 90 * 1440)::int) AS t,
             CASE WHEN w > %[2]d - 30 THEN (CASE WHEN w %% 2 = 0 THEN 'ready' ELSE 'running' END)
                  WHEN soak_ext.rnd('fms', w) < 0.93 THEN 'completed' WHEN soak_ext.rnd('fms', w) < 0.97 THEN 'blocked' ELSE 'failed' END AS st
      FROM generate_series(1, %[2]d) w) q`, g.baseSQL, p.FileManagerOps, g.library)
	g.execf("reconciliation_files", `
INSERT INTO reconciliation_files(managed_anime_id, generation, batch_index, file_path, fingerprint, size_bytes, facts, observed_at)
SELECT o.aid, 1, (w / 500)::int, '%[3]s/Soak Anime ' || (o.n %% 400) || '/Season 01/recon-' || w || '.mkv', soak_ext.hexn('rc', w, 64), 600000000,
       jsonb_build_object('pad', soak_ext.pad(w, 200)), %[1]s - make_interval(mins => (soak_ext.rnd('rco', w) * 20 * 1440)::int)
FROM generate_series(1, %[2]d) w JOIN soak_ext.owner o ON o.n = soak_ext.pick('rcn', w, %[4]d)`, g.baseSQL, p.ReconObs, g.library, p.Owners)

	// ---- one-shot markers ----------------------------------------------------------------------
	g.execf("one_shot_acquisitions", `
INSERT INTO one_shot_acquisitions(id, managed_anime_id, subscription_id, rss_source_id, release_id, download_task_id, correlation_id,
                                  request_idempotency_key, request_hash, result_snapshot, state, terminal_outcome, terminal_reason_code,
                                  terminal_reason, terminal_at, retention_until, version, created_at, updated_at)
SELECT 'os-' || o.n, o.aid, o.sid, s.id, r.id, 'dt-' || r.id, 'corr-os-' || o.n, 'os-key-' || o.n, soak_ext.dg('soak-os', o.n::text), '{}'::jsonb,
       CASE WHEN a.cls = 'succeeded' THEN 'reclaim_pending' ELSE 'running' END,
       CASE WHEN a.cls = 'succeeded' THEN 'succeeded' END, CASE WHEN a.cls = 'succeeded' THEN 'completed' END,
       CASE WHEN a.cls = 'succeeded' THEN 'one-shot completed' END, a.terminal_at, CASE WHEN a.cls = 'succeeded' THEN a.terminal_at + interval '3 days' END,
       1, a.accepted_at, a.accepted_at
FROM soak_ext.owner o
JOIN soak_ext.src s ON s.n = o.n AND s.idx = 0
JOIN soak_ext.rel r ON r.owner_n = o.n
JOIN soak_ext.acq a ON a.rel_id = r.id
WHERE o.oneshot`)
}

func expiredPerGroup(p Profile) int {
	perGroup := maxInt(p.CatalogSnapshots/8, 1)
	e := p.CatalogExpired / 8
	if e < 1 {
		e = 1
	}
	if e >= perGroup {
		e = perGroup - 1
	}
	return e
}

func pendingDeliveries64(n int) int64 { return int64(n) }

func itoa(n int) string     { return strconv.Itoa(n) }
func itoa64(n int64) string { return strconv.FormatInt(n, 10) }
func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
