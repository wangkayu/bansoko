//go:build soakscale

package soak

import (
	"context"
)

// generateOwners writes the owner plan, owners, sources, source runtimes and the
// live rss_refresh work item of every source. All owners other than the template
// are clones of the rows the real subscribe path wrote.
func (g *generator) generateOwners(ctx context.Context) {
	p := g.p
	t := g.t
	if err := g.conn.QueryRowContext(ctx, `
SELECT m.id, s.id, r.id FROM managed_anime m
JOIN subscriptions s ON s.managed_anime_id = m.id
JOIN rss_sources r ON r.subscription_id = s.id
ORDER BY m.created_at, m.id LIMIT 1`).Scan(&g.template.AnimeID, &g.template.SubscriptionID, &g.template.SourceID); err != nil {
		t.Fatalf("template owner: %v", err)
	}
	total := p.Owners + p.OneShotOwners
	extraSources := p.Sources - total
	if extraSources < 0 {
		extraSources = 0
	}
	heavyEvery := p.Owners / p.HeavyOwners
	if heavyEvery < 1 {
		heavyEvery = 1
	}

	g.exec("drop plan tables", `DROP TABLE IF EXISTS soak_ext.owner, soak_ext.src, soak_ext.rel CASCADE`)
	// The owner plan: one row per owner (subscribed first, one-shot after).
	g.execf("plan: owners", `
CREATE TABLE soak_ext.owner AS
SELECT n,
       'a' || lpad(n::text, 5, '0') AS aid,
       's' || lpad(n::text, 5, '0') AS sid,
       (n > %[2]d) AS oneshot,
       (n <= %[2]d AND (n - 1) %% %[3]d = 0 AND (n - 1) / %[3]d < %[4]d) AS heavy,
       CASE WHEN n > %[2]d THEN 1
            WHEN (n <= %[2]d AND (n - 1) %% %[3]d = 0 AND (n - 1) / %[3]d < %[4]d) THEN %[5]d
            ELSE GREATEST(1, (%[6]d / 3 + floor(soak_ext.rnd('rels', n) * (%[6]d * 4 / 3)))::int) END AS rels,
       %[1]s - make_interval(secs => 86400.0 * 90 *
            CASE WHEN n > %[2]d THEN 0.02 * soak_ext.rnd('created', n)
                 WHEN (n <= %[2]d AND (n - 1) %% %[3]d = 0 AND (n - 1) / %[3]d < %[4]d) THEN 0.93 + 0.07 * soak_ext.rnd('created', n)
                 ELSE 0.05 + 0.9 * soak_ext.rnd('created', n) END) AS created_at,
       CASE WHEN n > %[2]d THEN 'oneshot'
            WHEN soak_ext.rnd('origin', n) < 0.55 THEN 'custom'
            WHEN soak_ext.rnd('origin', n) < 0.80 THEN 'mikan' ELSE 'anibt' END AS origin,
       CASE WHEN n <= %[2]d AND soak_ext.rnd('state', n) < 0.08 THEN 'stopped' ELSE 'active' END AS state,
       (n <= %[7]d) AS two_sources
FROM generate_series(1, %[8]d) n`, g.baseSQL, p.Owners, heavyEvery, p.HeavyOwners, p.HeavyReleases, p.TypicalReleases, extraSources, total)
	g.exec("plan: owner pkey", `ALTER TABLE soak_ext.owner ADD PRIMARY KEY (n)`)

	g.exec("managed_anime", `
INSERT INTO managed_anime(id, title, version, created_at, updated_at)
SELECT aid, 'Soak Anime ' || n, 1, created_at, created_at FROM soak_ext.owner`)
	g.exec("anime_metadata", `
INSERT INTO anime_metadata(managed_anime_id, bgm_work_id, tmdb_work_id, tmdb_media_kind, tmdb_season, version, created_at, updated_at, total_episodes)
SELECT aid, (100000 + n)::text, (200000 + n)::text, 'tv', 1, 1, created_at, created_at,
       CASE WHEN heavy THEN NULL WHEN soak_ext.rnd('eps', n) < 0.7 THEN 12 ELSE 24 END
FROM soak_ext.owner WHERE NOT oneshot`)
	g.execf("anime_business_configs", `
INSERT INTO anime_business_configs(managed_anime_id, rules_version, subgroups_version,
    parser_kind, parser_source_default_version, parser_version,
    admission_mode, quality_profile, retention_source_default_version, retention_version,
    unclassified_route, file_effect_source_default_version, file_effect_version,
    candidate_id, revision, business_snapshot, business_digest, business_version, updated_at)
SELECT o.aid, t.rules_version, t.subgroups_version,
       t.parser_kind, t.parser_source_default_version, t.parser_version,
       t.admission_mode, t.quality_profile, t.t.retention_source_default_version, t.retention_version,
       t.unclassified_route, t.file_effect_source_default_version, t.file_effect_version,
       t.candidate_id, t.revision, t.business_snapshot, t.business_digest, t.business_version, o.created_at
FROM soak_ext.owner o CROSS JOIN anime_business_configs t WHERE t.managed_anime_id = '%s'`, g.template.AnimeID)
	g.execf("anime_config_overrides", `
INSERT INTO anime_config_overrides(managed_anime_id, owner_type, section_name, schema_version, value, created_at, updated_at)
SELECT o.aid, t.owner_type, t.section_name, t.schema_version, t.value, o.created_at, o.created_at
FROM soak_ext.owner o CROSS JOIN anime_config_overrides t WHERE t.managed_anime_id = '%s'`, g.template.AnimeID)
	g.execf("subscriptions", `
INSERT INTO subscriptions(id, managed_anime_id, origin, origin_entry_ref, instance_label, state, version, created_at, updated_at)
SELECT sid, aid, origin,
       CASE WHEN origin IN ('mikan', 'anibt') THEN (1000 + n %% %d)::text END,
       CASE WHEN origin IN ('custom', 'oneshot') THEN 'Soak ' || n END,
       state, 1, created_at, created_at
FROM soak_ext.owner`, maxInt(p.CatalogEntriesPer, 1))

	// Sources: one per owner, a second one for the first `extraSources` owners.
	g.exec("plan: sources", `
CREATE TABLE soak_ext.src AS
SELECT o.n, o.aid, o.sid, i AS idx, 'r' || lpad(o.n::text, 5, '0') || '-' || i AS id,
       o.created_at, o.heavy, o.oneshot, o.state AS sub_state
FROM soak_ext.owner o CROSS JOIN LATERAL generate_series(0, CASE WHEN o.two_sources THEN 1 ELSE 0 END) i`)
	g.exec("plan: src pkey", `ALTER TABLE soak_ext.src ADD PRIMARY KEY (id)`)
	g.exec("rss_sources", `
INSERT INTO rss_sources(id, subscription_id, source_kind, locator, locator_display, group_name, title, episode_offset, version, created_at, updated_at)
SELECT id, sid, 'custom_rss', 'https://feeds.soak.invalid/' || id || '.xml', 'https://feeds.soak.invalid/' || id || '.xml',
       'Group' || idx, 'Soak Anime ' || n, 0, 1, created_at, created_at
FROM soak_ext.src`)
	// ~15% of sources are degraded or failed (PRD).
	g.execf("rss_source_runtime", `
INSERT INTO rss_source_runtime(rss_source_id, cursor, last_good_facts, last_refresh_at, last_success_at, health, reason_code, reason_detail,
                               version, created_at, updated_at, etag, last_modified, consecutive_failures)
SELECT id, NULL, NULL, lr, ls, h,
       CASE h WHEN 'degraded' THEN 'feed_slow' WHEN 'failed' THEN 'feed_unreachable' END,
       CASE h WHEN 'degraded' THEN 'recent refreshes were slow' WHEN 'failed' THEN 'connection refused' END,
       1, created_at, lr,
       CASE WHEN h IN ('ready', 'degraded') THEN 'W/"' || soak_ext.hexn('etag', n::bigint, 12) || '"' END,
       CASE WHEN h IN ('ready', 'degraded') THEN 'Wed, 14 Oct 2026 08:00:00 GMT' END,
       CASE h WHEN 'failed' THEN 3 + (soak_ext.rnd('cf', n) * 5)::int WHEN 'degraded' THEN 1 ELSE 0 END
FROM (
  SELECT s.id, s.n, s.created_at,
         CASE WHEN r < 0.08 THEN 'degraded' WHEN r < 0.15 THEN 'failed' WHEN r < 0.20 THEN 'unknown' ELSE 'ready' END AS h,
         %[1]s - make_interval(mins => (soak_ext.rnd('lr', s.n * 2 + s.idx) * 15)::int) AS lr,
         CASE WHEN r < 0.15 THEN %[1]s - make_interval(hours => 6 + (soak_ext.rnd('ls', s.n)*40)::int)
              ELSE %[1]s - make_interval(mins => (soak_ext.rnd('lr', s.n * 2 + s.idx) * 15)::int) END AS ls
  FROM (SELECT x.*, soak_ext.rnd('health', x.n * 2 + x.idx) AS r FROM soak_ext.src x) s
) q`, g.baseSQL)
	// rss_refresh queue entries: every source has its one live item, spread over the
	// 15 minute schedule so about a quarter are due.
	g.execf("work_items: rss_refresh (live)", `
INSERT INTO work_items(id, owner_type, owner_id, kind, state, available_at, fencing_token, idempotency_key,
                       attempt, max_attempts, input_snapshot, expected_owner_version, retention_days, created_at, updated_at,
                       managed_anime_id, owner_lifecycle_epoch)
SELECT 'wi-rss-' || id, 'rss_source_runtime', id, 'rss_refresh', 'available',
       %[1]s + make_interval(mins => -5 + (soak_ext.rnd('rssdue', n * 2 + idx) * 20)::int), 0, 'rss_refresh:' || id,
       0, 5, '{"trigger":"scheduled"}'::jsonb, 1, 7, %[1]s - interval '20 minutes', %[1]s - interval '20 minutes', aid, 1
FROM soak_ext.src WHERE NOT oneshot`, g.baseSQL)
	// one-shot sources have a runtime but no live refresh item.
	_ = t
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
