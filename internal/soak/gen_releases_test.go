//go:build soakscale

package soak

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"bansoko/internal/parser/reltitletags"
)

// titleTagsFact is the SQL expression of source_facts.title_tags as RSS refresh
// freezes it on every release (the pinned parser's reading of the title). A real
// release always carries it, so reads never run the title parser; without it the
// dataset would charge every list read one reltitle parse per release. The tag set
// is the parser's own output for the generated title shape; only the title and
// group values vary per owner.
func titleTagsFact() (string, error) {
	sample := reltitletags.Extract("[Group0] Soak Anime 0 - 01 [WebRip 1080p HEVC AAC][CHS]")
	if sample.Diagnostic != "" {
		return "", fmt.Errorf("title tag sample did not parse: %s", sample.Diagnostic)
	}
	tags, err := json.Marshal(sample.Tags)
	if err != nil {
		return "", err
	}
	quote := func(v string) string { return "'" + strings.ReplaceAll(v, "'", "''") + "'" }
	return fmt.Sprintf(`jsonb_build_object('schema', %s, 'implementation', %s, 'config_digest', %s, 'tags', %s::jsonb,
                          'value_tags', jsonb_build_array('title:Soak Anime ' || owner_n, 'group:Group' || src_idx))`,
		quote(sample.Schema), quote(sample.Implementation), quote(sample.ConfigDigest), quote(string(tags))), nil
}

// generateReleases writes the releases of every owner plus the per-release rows
// the workspace and RSS reads join: media fact snapshots and files, acquisition
// targets and their observations.
func (g *generator) generateReleases(ctx context.Context) {
	p := g.p
	chosenTarget := p.Acquisitions - p.OneShotOwners
	if chosenTarget < 0 {
		chosenTarget = 0
	}
	g.execf("plan: releases", `
CREATE TABLE soak_ext.rel AS
SELECT q.*, (q.oneshot OR q.pick_rank <= %[2]d) AS chosen
FROM (
  SELECT row_number() OVER (ORDER BY o.n, k) AS gn,
         o.n AS owner_n, o.aid, o.sid, k, o.rels, o.heavy, o.oneshot, o.state AS sub_state,
         'rl' || lpad(o.n::text, 5, '0') || '-' || k AS id,
         s.id AS source_id, s.idx AS src_idx,
         o.created_at + (%[1]s - o.created_at) * ((k::double precision - 0.5) / o.rels) AS discovered_at,
         soak_ext.rnd('verdict', o.n::bigint * 100000 + k) AS vr,
         row_number() OVER (PARTITION BY o.oneshot ORDER BY soak_ext.rnd('pick', o.n::bigint * 100000 + k), o.n, k) AS pick_rank,
         '[Group' || s.idx || '] Soak Anime ' || o.n || ' - ' || soak_ext.ep(k) || ' [WebRip 1080p HEVC AAC][CHS]' AS title,
         (300000000 + soak_ext.rnd('size', o.n::bigint * 100000 + k) * 1100000000)::bigint AS size_bytes
  FROM soak_ext.owner o
  CROSS JOIN LATERAL generate_series(1, o.rels) k
  JOIN soak_ext.src s ON s.n = o.n AND s.idx = (CASE WHEN o.two_sources THEN k %% 2 ELSE 0 END)
) q`, g.baseSQL, chosenTarget)
	g.exec("plan: rel pkey", `ALTER TABLE soak_ext.rel ADD PRIMARY KEY (id)`)
	g.exec("plan: rel index", `CREATE INDEX ON soak_ext.rel (chosen, gn)`)
	g.exec("plan: rel verdict", `ALTER TABLE soak_ext.rel ADD COLUMN verdict text, ADD COLUMN reason text`)
	g.exec("plan: rel verdict set", `
UPDATE soak_ext.rel SET
  verdict = CASE WHEN chosen THEN 'submitted'
                 WHEN vr < 0.55 THEN 'filtered'
                 WHEN vr < 0.60 THEN 'discovered'
                 WHEN vr < 0.63 THEN 'waiting_metadata'
                 WHEN vr < 0.67 THEN 'parse_failed'
                 WHEN vr < 0.75 THEN 'adopted_existing'
                 WHEN vr < 0.78 THEN 'subscription_stopped'
                 ELSE 'acquisition_failed' END`)
	g.exec("plan: rel reason set", `
UPDATE soak_ext.rel SET reason = CASE verdict
  WHEN 'submitted' THEN 'accepted' WHEN 'filtered' THEN 'release_filter_rejected' WHEN 'discovered' THEN 'discovered'
  WHEN 'waiting_metadata' THEN 'metadata_pending' WHEN 'parse_failed' THEN 'parse_failed'
  WHEN 'adopted_existing' THEN 'target_exists' WHEN 'subscription_stopped' THEN 'subscription_stopped'
  ELSE 'acquisition_failed' END`)

	titleTags, err := titleTagsFact()
	if err != nil {
		g.t.Fatal(err)
	}
	g.execf("releases", `
INSERT INTO releases(id, rss_source_id, stable_item_key, raw_facts, source_facts, parsed_facts, internal_facts, verdict, reason_code,
                     reason_detail, evaluation_version, download_task_id, version, discovered_at, updated_at, evaluated_rules_version,
                     discovery_notice_due)
SELECT id, source_id, 'guid:' || id,
       jsonb_build_object('title', title, 'published_at', to_char(discovered_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
                          'enclosure_type', 'application/x-bittorrent', 'enclosure_length', size_bytes),
       jsonb_build_object('group_name', 'Group' || src_idx, 'identity_kind', 'guid', 'identity_value', id, 'title_tags', %[1]s),
       CASE WHEN verdict = 'parse_failed' THEN NULL
            ELSE jsonb_build_object('episode', k, 'group_name', 'Group' || src_idx, 'languages', jsonb_build_array('chs'),
                                    'quality', '1080p', 'special', false, 'coverage', NULL) END,
       jsonb_build_object('guid', id, 'title', title, 'link', 'https://feeds.soak.invalid/item/' || id,
                          'description', soak_ext.pad(gn, 180), 'enclosure', 'https://feeds.soak.invalid/dl/' || id || '.torrent',
                          'enclosure_type', 'application/x-bittorrent', 'enclosure_length', size_bytes,
                          'torrent_url', 'https://feeds.soak.invalid/dl/' || id || '.torrent',
                          'download_url', 'https://feeds.soak.invalid/dl/' || id || '.torrent',
                          'published', to_char(discovered_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
                          'release_files', jsonb_build_array(jsonb_build_object('source_path', title || '.mkv', 'filename', title || '.mkv',
                                                                               'size_bytes', size_bytes, 'media_kind', 'video'))),
       verdict, reason, NULL,
       CASE WHEN verdict = 'discovered' THEN NULL ELSE 'eval-v1' END,
       CASE WHEN chosen THEN 'dt-' || id END,
       1, discovered_at, discovered_at, CASE WHEN verdict = 'discovered' THEN NULL ELSE 1 END,
       (soak_ext.rnd('notice', gn) < 0.05)
FROM soak_ext.rel`, titleTags)

	g.exec("media_fact_snapshots", `
INSERT INTO media_fact_snapshots(id, release_id, schema_version, resolver_version, parser_kind, parser_config_fingerprint, input_digest,
                                 release_identity, release_facts, created_at, parser_config)
SELECT 'mf-' || id, id, 'media-facts-v1', 'resolver-v1', 'regex', NULL, soak_ext.dg('soak-input', id),
       jsonb_build_object('State', 'resolved', 'Kind', 'tv', 'Shape', 'single', 'RawEpisode', k::text, 'Episode', k::text,
                          'CoverageKey', 'tv:s1:e' || k, 'ResolverVersion', 'resolver-v1', 'InputDigest', soak_ext.dg('soak-input', id)),
       jsonb_build_object('title', title, 'group', 'Group' || src_idx, 'resolution', '1080p', 'languages', jsonb_build_array('chs')),
       discovered_at + interval '1 second', NULL
FROM soak_ext.rel`)
	g.exec("media_fact_files", `
INSERT INTO media_fact_files(snapshot_id, file_ref, media_kind, size_bytes, source_fingerprint, file_evidence, effective_facts, created_at)
SELECT 'mf-' || id, 'file-0', 'video', size_bytes, NULL,
       jsonb_build_object('name', title || '.mkv', 'media_kind', 'video', 'size_bytes', size_bytes, 'group_name', 'Group' || src_idx,
                          'resolution', '1080p', 'languages', jsonb_build_array('chs')),
       jsonb_build_object('identity', jsonb_build_object('State', 'resolved', 'Kind', 'tv', 'Shape', 'single', 'RawEpisode', k::text,
                                                         'Episode', k::text, 'CoverageKey', 'tv:s1:e' || k),
                          'quality', jsonb_build_object('size_bytes', size_bytes, 'resolution', '1080p'),
                          'mapping_version', 'facts-map-v1'),
       discovered_at + interval '1 second'
FROM soak_ext.rel`)

	// Targets: one video target per release, a second (subtitle) one for ~9%.
	g.execf("acquisition_targets", `
INSERT INTO acquisition_targets(id, release_id, target_ref, source_path, media_kind, target_path, expected_size_bytes, intent,
                                collision_policy, render_snapshot, config_snapshot, spec_digest, version, created_at, updated_at)
SELECT 'tg-' || r.id || '-' || t.i, r.id, 'file-' || t.i,
       r.title || CASE t.i WHEN 0 THEN '.mkv' ELSE '.chs.ass' END,
       CASE t.i WHEN 0 THEN 'video' ELSE 'subtitle' END,
       '%[1]s/Soak Anime ' || (r.owner_n %% %[2]d) || '/Season 01/Soak Anime ' || r.owner_n || ' - S01E' || soak_ext.ep(r.k) ||
         CASE t.i WHEN 0 THEN '.mkv' ELSE '.chs.ass' END,
       CASE t.i WHEN 0 THEN r.size_bytes ELSE 80000 END, 'ensure_present', 'block',
       jsonb_build_object('renderer_version', 'fileoperation-render-v1', 'title', 'Soak Anime ' || r.owner_n, 'season', 1, 'episode', r.k),
       '{}'::jsonb, soak_ext.dg('soak-target', r.id || t.i::text), 1, r.discovered_at, r.discovered_at
FROM soak_ext.rel r CROSS JOIN LATERAL generate_series(0, CASE WHEN soak_ext.rnd('subt', r.gn) < 0.09 THEN 1 ELSE 0 END) t(i)`,
		g.library, maxInt(p.PlacementDirs, 1))
	g.exec("acquisition_targets", `
UPDATE acquisition_targets t
SET observation_state = CASE WHEN r.chosen THEN 'present_matched' ELSE 'missing' END,
    observed_size_bytes = CASE WHEN r.chosen THEN t.expected_size_bytes END,
    observed_at = r.discovered_at + interval '1 minute'
FROM soak_ext.rel r WHERE r.id = t.release_id`)
}
