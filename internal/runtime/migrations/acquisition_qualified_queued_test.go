package migrations

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	protocol "bansoko/internal/acquisition/terminal/domain"
	anime "bansoko/internal/anime/domain"
	"bansoko/internal/attrrules"
	"bansoko/internal/namingvars"
	"bansoko/internal/testutil/frozenpolicy"
)

// The queued fixtures freeze a genuine submitted business value
// with exact retained programs; no active fallback or invented empty claim.
func queuedBusiness(t *testing.T) anime.BusinessSnapshot {
	t.Helper()
	b := anime.BusinessSnapshot{SchemaVersion: "business-snapshot-v1", Sections: map[string]json.RawMessage{}, SectionVersions: map[string]int64{}, SectionSchemas: map[string]string{}, Parser: anime.ParserPolicyEnvelope{SchemaVersion: anime.ParserPolicyEnvelopeSchemaVersion, Kind: "regex", SourceOwner: "global", Provenance: "global", PolicyVersion: 1, ProfileVersion: 1, RulesOwnerVersion: 1}, RetentionVersion: 1, FileEffectVersion: 1, UnclassifiedRoute: "skip"}
	for _, name := range anime.EffectiveSectionOrder {
		value, err := anime.DefaultSectionValue(name)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		b.Sections[name] = raw
		b.SectionVersions[name] = 1
		b.SectionSchemas[name] = anime.SectionSchemaVersions[name]
	}
	b.Retention = anime.RetentionValue{AdmissionMode: "all", QualityProfile: json.RawMessage(`{"version":"profile-v1","items":[{"key":"subgroup.score","ranks":[]},{"key":"release.lang","ranks":[["chs"],["sc"],["cht"],["tc"],["jpn"],["eng"]]},{"key":"release.resolution","ranks":[["2160p"],["1440p"],["1080p"],["720p"],["480p"]]},{"key":"release.revision","ranks":[]}]}`)}
	rules := attrrules.Rules{Version: 1, Resolution: []string{"1080p"}, Quality: []string{"web"}, Language: []string{"jpn"}, SpecialMarkers: []string{"special"}, GroupBrackets: []string{"[", "]"}}
	attr, err := attrrules.Fingerprint(rules)
	if err != nil {
		t.Fatal(err)
	}
	vars, err := namingvars.SourceFingerprint(`vars.actual = {title: input.release_title};`)
	if err != nil {
		t.Fatal(err)
	}
	b.Programs = anime.ProgramReferences{AttrRules: namingvars.ProgramRef{Digest: attr, Version: attrrules.ProfileVersion(attr)}, Vars: namingvars.ProgramRef{Digest: vars, Version: namingvars.ProfileVersion(vars)}}
	return b
}
func seedQueuedPrograms(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := frozenpolicy.SeedBusiness(context.Background(), db, `vars.actual = {title: input.release_title};`); err != nil {
		t.Fatal(err)
	}
}
func submittedQualification(t *testing.T, tag string, b anime.BusinessSnapshot) string {
	t.Helper()
	digest, err := b.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return adoptJSON(t, map[string]any{
		"config":      map[string]any{"kind": "submitted", "request_hash": acqDigest("6"), "config_digest": digest},
		"management":  map[string]any{"kind": "instance", "managed_anime_ref": "anime-" + tag, "subscription_ref": "sub-" + tag, "expected_subscription_version": 1, "expected_lifecycle_version": 1, "expected_metadata_version": nil, "expected_parser_policy_version": nil, "expected_rules_owner_version": nil, "expected_subgroups_owner_version": nil, "subscription_policy": "manual_active_or_stopped"},
		"source":      map[string]any{"kind": "release", "release_ref": "rel-" + tag, "expected_version": 1, "source_ref": map[string]any{"ref": "src-" + tag, "version": 1}},
		"observation": nil, "feed": nil, "programs": map[string]any{"attr_fingerprint": b.Programs.AttrRules.Digest, "vars_digest": b.Programs.Vars.Digest, "translation": nil}, "placement": map[string]any{"kind": "none"},
	})
}
func mutateQualification(t *testing.T, p *queuedParams, change func(map[string]any)) {
	t.Helper()
	var q map[string]any
	if err := json.Unmarshal([]byte(p.Qualification), &q); err != nil {
		t.Fatal(err)
	}
	change(q)
	p.Qualification = adoptJSON(t, q)
}
func qualifiedCatalog(t *testing.T, db *sql.DB, id, key, tag string) queuedParams {
	t.Helper()
	f := seedQualification(t, db, tag)
	p := validQueued(t, id, key, tag)
	p.Payload = f.feed.Seeds[0].Payload
	var input map[string]any
	if err := json.Unmarshal([]byte(p.InputCanonical), &input); err != nil {
		t.Fatal(err)
	}
	input["source"].(map[string]any)["payload_digest"] = f.payloadDigest
	input["source"].(map[string]any)["kind"] = "automatic_source"
	p.InputCanonical = canonicalJSON(t, input)
	p.AdmissionDigest = acqDomainDigest("acquisition-input:v1", []byte(p.InputCanonical))
	var q map[string]any
	if err := json.Unmarshal([]byte(f.qualification(nil)), &q); err != nil {
		t.Fatal(err)
	}
	var b anime.BusinessSnapshot
	if err := db.QueryRowContext(context.Background(), `SELECT business_snapshot FROM anime_business_configs WHERE managed_anime_id=$1`, f.animeID).Scan(jsonRawScan{dst: &b}); err != nil {
		t.Fatal(err)
	}
	p.BusinessSnapshot = adoptJSON(t, b)
	input["config"].(map[string]any)["effective_digest"] = f.businessDigest
	config := input["config"].(map[string]any)
	for _, entry := range config["sections"].([]any) {
		section := entry.(map[string]any)
		key := section["section_key"].(string)
		var value any
		if err := json.Unmarshal(projectDefaultSection(key, b.Sections[key]), &value); err != nil {
			t.Fatal(err)
		}
		section["value"] = value
		section["config_version"] = b.SectionVersions[key]
		section["schema_version"] = b.SectionSchemas[key]
	}
	fact := map[string]any{"state": "known", "value": 1, "reason_code": nil}
	config["rules_version"] = fact
	parser := config["parser"].(map[string]any)
	selection := parser["selection"].(map[string]any)["value"]
	body := map[string]any{"selection": selection, "offset_source": "instance_default", "rules_owner_version": fact}
	for _, key := range []string{"offset_override", "source_version", "policy_version", "profile_version", "input_digest", "result_digest", "media_kind", "season", "season_bound", "parse_status", "parse_shape"} {
		body[key] = map[string]any{"state": "absent", "value": nil, "reason_code": nil}
	}
	parser["provenance"] = map[string]any{"state": "known", "value": body, "reason_code": nil}
	p.InputCanonical = canonicalJSON(t, input)
	p.AdmissionDigest = acqDomainDigest("acquisition-input:v1", []byte(p.InputCanonical))
	p.Qualification = adoptJSON(t, q)
	var observation json.RawMessage
	if err := db.QueryRowContext(context.Background(), `SELECT observation FROM provider_group_snapshots WHERE id=$1`, f.groupID).Scan(&observation); err != nil {
		t.Fatal(err)
	}
	var o map[string]any
	if err := json.Unmarshal(observation, &o); err != nil {
		t.Fatal(err)
	}
	var policy map[string]any
	if err := json.Unmarshal(b.Sections[anime.SectionSubgroups], &policy); err != nil {
		t.Fatal(err)
	}
	policy["name_keywords"] = nil
	policy["provider_defaults"] = nil
	absent := map[string]any{"state": "absent", "value": nil, "reason_code": nil}
	decisions := []any{}
	selected := []string{}
	rank := 0
	// groupResult fixture has default-ranked groups. The A4 production builder is
	// exercised independently by terminal/application's SQL end-to-end fixture.
	for _, raw := range o["groups"].([]any) {
		g := raw.(map[string]any)
		available := g["available"].(map[string]any)["value"].(bool)
		var r any
		reason, outcome := "provider_group_not_observed", "unavailable"
		if available {
			rank++
			r = rank
			reason, outcome = "ranked_within_max_rss", "selected"
			selected = append(selected, g["group_ref"].(string))
		}
		decisions = append(decisions, map[string]any{"group_ref": g["group_ref"], "display_name": map[string]any{"state": "known", "value": g["display_name"], "reason_code": nil}, "available": available, "canonical_key": absent, "mode": "ranked", "score": 50, "count_in_limit": true, "policy_origin": map[string]any{"layer": "provider_default", "config_version": 1}, "rank": r, "outcome": outcome, "reason_code": reason})
	}
	witness := map[string]any{"schema_version": 1, "kind": "provider_selection_v1", "evaluator_version": "evaluator-v1", "request_hash": p.RequestHash, "input_digest": p.AdmissionDigest, "config_digest": f.businessDigest, "observation": o, "observation_digest": f.groupDigest, "policy": policy, "policy_digest": protocol.Digest("acquisition-group-policy:v1", []byte(canonicalJSON(t, policy))), "translation": nil, "decisions": decisions, "selected_refs": selected}
	p.PreparationMode = "provider_selection_v1"
	p.PreparationWitness = canonicalJSON(t, witness)
	p.PreparationDigest = protocol.Digest("acquisition-preparation-witness:v1", []byte(p.PreparationWitness.(string)))
	p.WitnessMaxBytes, p.WitnessMaxRows = 200000, 50 // fixture capacities only
	return p
}

type jsonRawScan struct{ dst *anime.BusinessSnapshot }

func (s jsonRawScan) Scan(value any) error {
	switch v := value.(type) {
	case []byte:
		return json.Unmarshal(v, s.dst)
	case string:
		return json.Unmarshal([]byte(v), s.dst)
	}
	return sql.ErrNoRows
}
func canonicalJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	v, err := protocol.ParseStrict(raw)
	if err != nil {
		t.Fatal(err)
	}
	return string(protocol.Canonical(v))
}

func TestAcquisitionQualifiedQueuedRejectsChangedClaimsAndWitness(t *testing.T) {
	owner, runtime := setupQualification(t)
	p := qualifiedCatalog(t, owner, "acq-qualified", "key-qualified", "qualified")
	for _, tc := range []struct {
		name    string
		change  func(*queuedParams)
		message string
	}{
		{"subscription", func(p *queuedParams) {
			mutateQualification(t, p, func(q map[string]any) { member(q, "management")["expected_subscription_version"] = 99 })
		}, "ACQ_QUALIFICATION_MANAGEMENT_CHANGED"},
		{"lifecycle", func(p *queuedParams) {
			mutateQualification(t, p, func(q map[string]any) { member(q, "management")["expected_lifecycle_version"] = 99 })
		}, "ACQ_QUALIFICATION_MANAGEMENT_CHANGED"},
		{"config", func(p *queuedParams) {
			mutateQualification(t, p, func(q map[string]any) { member(q, "config")["config_version"] = 99 })
		}, "ACQ_QUALIFICATION_CONFIG_CHANGED"},
		{"source", func(p *queuedParams) {
			mutateQualification(t, p, func(q map[string]any) { member(member(q, "source"), "source_ref")["version"] = 99 })
		}, "ACQ_QUALIFICATION_SOURCE_CHANGED"},
		{"release", func(p *queuedParams) {
			mutateQualification(t, p, func(q map[string]any) { member(q, "source")["expected_version"] = 99 })
		}, "ACQ_QUALIFICATION_SOURCE_CHANGED"},
		{"observation", func(p *queuedParams) {
			mutateQualification(t, p, func(q map[string]any) { member(q, "observation")["snapshot_version"] = 99 })
		}, "ACQ_QUALIFICATION_OBSERVATION_CHANGED"},
		{"feed", func(p *queuedParams) {
			mutateQualification(t, p, func(q map[string]any) { member(q, "feed")["snapshot_version"] = 99 })
		}, "ACQ_QUALIFICATION_FEED_CHANGED"},
		{"program", func(p *queuedParams) {
			mutateQualification(t, p, func(q map[string]any) { member(q, "programs")["attr_fingerprint"] = acqDigest("f") })
		}, "ACQ_QUALIFICATION_PROGRAM_CHANGED"},
		{"placement", func(p *queuedParams) {
			mutateQualification(t, p, func(q map[string]any) {
				q["placement"] = map[string]any{"kind": "existing", "placement_ref": "p", "expected_revision": 1, "file_fact_digest": acqDigest("f")}
			})
		}, "ACQ_QUALIFICATION_PROOF_UNSUPPORTED"},
		{"witness rank", func(p *queuedParams) {
			tamperWitness(t, p, func(w map[string]any) { w["decisions"].([]any)[0].(map[string]any)["rank"] = 99 })
		}, "ACQ_ACCEPT_PREPARATION_MISMATCH"},
		{"witness policy", func(p *queuedParams) {
			tamperWitness(t, p, func(w map[string]any) { w["policy"].(map[string]any)["max_rss"] = 0 })
		}, "ACQ_ACCEPT_PREPARATION_MISMATCH"},
		{"witness selection", func(p *queuedParams) {
			tamperWitness(t, p, func(w map[string]any) { w["selected_refs"] = []string{} })
		}, "ACQ_ACCEPT_PREPARATION_MISMATCH"},
		{"witness capsule", func(p *queuedParams) {
			tamperWitness(t, p, func(w map[string]any) { w["observation"].(map[string]any)["snapshot_version"] = 99 })
		}, "ACQ_ACCEPT_PREPARATION_MISMATCH"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := p
			bad.AcquisitionID = "bad-qualified"
			tc.change(&bad)
			_, err := callQueuedAccept(t, runtime, bad)
			expectQualificationError(t, err, tc.message)
			var n int
			if err := owner.QueryRowContext(context.Background(), `SELECT count(*) FROM acquisitions`).Scan(&n); err != nil || n != 0 {
				t.Fatalf("rejection left root: %d %v", n, err)
			}
		})
	}
	first, err := callQueuedAccept(t, runtime, p)
	if err != nil {
		t.Fatal(err)
	}
	if first["status"] != "accepted" {
		t.Fatal(first)
	}
	replay := p
	replay.Qualification = "{}"
	replay.BusinessSnapshot = "{}"
	replay.PreparationWitness = "{}"
	replay.PreparationDigest = acqDigest("f")
	again, err := callQueuedAccept(t, runtime, replay)
	if err != nil || again["status"] != "replayed" {
		t.Fatalf("replay rechecked: %v %v", again, err)
	}
	var overloads int
	if err := owner.QueryRowContext(context.Background(), `SELECT count(*) FROM pg_proc WHERE pronamespace='public'::regnamespace AND proname='accept_acquisition_queued_with_vars'`).Scan(&overloads); err != nil || overloads != 1 {
		t.Fatalf("unexpected accept overloads: %d %v", overloads, err)
	}
	var bypass int
	if err := owner.QueryRowContext(context.Background(), `SELECT count(*) FROM pg_proc WHERE pronamespace='public'::regnamespace AND (proname='accept_acquisition_queued' OR (proname='accept_acquisition_queued_without_vars' AND has_function_privilege('bansoko_runtime',oid,'EXECUTE')))`).Scan(&bypass); err != nil || bypass != 0 {
		t.Fatalf("queued writer bypass: %d %v", bypass, err)
	}
}
func tamperWitness(t *testing.T, p *queuedParams, change func(map[string]any)) {
	t.Helper()
	var w map[string]any
	if err := json.Unmarshal([]byte(p.PreparationWitness.(string)), &w); err != nil {
		t.Fatal(err)
	}
	change(w)
	p.PreparationWitness = canonicalJSON(t, w)
	p.PreparationDigest = protocol.Digest("acquisition-preparation-witness:v1", []byte(p.PreparationWitness.(string)))
}

func TestAcquisitionQualifiedQueuedWaitsThenRejectsExpiredFeed(t *testing.T) {
	owner, runtime := setupQualification(t)
	p := qualifiedCatalog(t, owner, "acq-expiry", "key-expiry", "expiry")
	var q map[string]any
	if err := json.Unmarshal([]byte(p.Qualification), &q); err != nil {
		t.Fatal(err)
	}
	feed := member(q, "feed")["snapshot_ref"].(string)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	blocker, err := owner.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback()
	if _, err := blocker.ExecContext(ctx, `UPDATE provider_feed_snapshots SET fresh_until=clock_timestamp()+interval '0.3 second',fetched_at=clock_timestamp()-interval '1 second' WHERE id=$1`, feed); err != nil {
		t.Fatal(err)
	}
	conn, err := runtime.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var pid int
	if err := conn.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		var raw []byte
		call, args := queuedCallSQL(), queuedCallArgs(p.args())
		err := conn.QueryRowContext(ctx, call, args...).Scan(&raw)
		done <- err
	}()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var blocked bool
		if err := owner.QueryRowContext(ctx, `SELECT cardinality(pg_blocking_pids($1))>0`, pid).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("no real lock wait: %v", err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-ticker.C:
		}
	}
	for {
		var expired bool
		if err := blocker.QueryRowContext(ctx, `SELECT fresh_until<=clock_timestamp() FROM provider_feed_snapshots WHERE id=$1`, feed).Scan(&expired); err != nil {
			t.Fatal(err)
		}
		if expired {
			break
		}
		<-ticker.C
	}
	if err := blocker.Commit(); err != nil {
		t.Fatal(err)
	}
	expectQualificationError(t, <-done, "ACQ_QUALIFICATION_FEED_EXPIRED")
	var n int
	if err := owner.QueryRowContext(ctx, `SELECT count(*) FROM acquisitions`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("expired created root: %d %v", n, err)
	}
}
