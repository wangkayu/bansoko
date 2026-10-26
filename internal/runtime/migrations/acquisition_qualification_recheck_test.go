package migrations

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"

	protocol "bansoko/internal/acquisition/terminal/domain"
	animedomain "bansoko/internal/anime/domain"
	draft "bansoko/internal/anime/draft/application"
	persistence "bansoko/internal/catalog/adapter/persistence"
	catalogdomain "bansoko/internal/catalog/domain"
	"bansoko/internal/runtime/database"
	"bansoko/internal/testutil/frozenpolicy"
	"bansoko/internal/workitems"
)

// These tests prove that the owner-held qualification recheck locks
// and compares only the fixed QualificationV1 claims, returns the post-lock DB
// verdict time, and never creates rows. Placement/translation/seed proofs that
// SQL cannot represent fail closed instead of being downgraded.

type qualificationFixture struct {
	animeID, subID, srcID, relID     string
	candidateID, revisionDigest      string
	candidateVersion, revision       int64
	businessDigest, revisionBusiness string
	groupID, groupDigest             string
	groupVersion                     int64
	feed                             catalogdomain.ProviderFeedSnapshot
	payloadDigest, projectionDigest  string
	attrDigest, varsDigest           string
}

func seedQualification(t *testing.T, db *sql.DB, tag string) qualificationFixture {
	t.Helper()
	ctx := context.Background()
	seedAcceptableRelease(t, db, tag)
	if _, err := db.ExecContext(ctx, `INSERT INTO anime_metadata(managed_anime_id) VALUES($1) ON CONFLICT DO NOTHING`, "anime-"+tag); err != nil {
		t.Fatal(err)
	}
	f := qualificationFixture{animeID: "anime-" + tag, subID: "sub-" + tag, srcID: "src-" + tag, relID: "rel-" + tag}

	// Real saved revision and instance business through the draft owner; the
	// programs are inactive exact versions, so non-active old content is used.
	b, err := frozenpolicy.SeedBusiness(ctx, db, `vars.actual = {title: input.release_title};`)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := b.Digest()
	if err != nil {
		t.Fatal(err)
	}
	label := "qualification-" + tag
	d := animedomain.AnimeDraft{Origin: animedomain.SubscriptionOriginOneshot, Title: "Qualified Show", InstanceLabel: &label, Subgroups: animedomain.SubgroupsSection{MaxRss: 3, Entries: []animedomain.SubgroupScoreEntry{}}, Business: &b, BusinessDigest: &digest}
	for _, entry := range []struct {
		name string
		dst  *json.RawMessage
	}{{animedomain.SectionParser, &d.Rules.Sections.Parser}, {animedomain.SectionReleaseRules, &d.Rules.Sections.ReleaseFilter}, {animedomain.SectionFileRules, &d.Rules.Sections.FileFilter}, {animedomain.SectionNamingRules, &d.Rules.Sections.Naming}, {animedomain.SectionFileEffect, &d.Rules.Sections.FileEffect}} {
		*entry.dst, _ = json.Marshal(map[string]any{"mode": "replace", "value": b.Sections[entry.name]})
	}
	service, err := draft.NewService(db, draft.Dependencies{WorkItems: &workitems.Store{DB: db, RetentionDays: workitems.DefaultRetentionDays}})
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := service.SaveAnimeConfigCandidate(ctx, draft.CandidateDraftInput{Draft: d})
	if err != nil {
		t.Fatal(err)
	}
	f.candidateID, f.revision = candidate.Id, *candidate.CurrentRevision
	f.attrDigest, f.varsDigest = b.Programs.AttrRules.Digest, b.Programs.Vars.Digest
	if err := db.QueryRowContext(ctx, `SELECT version,current_revision_digest FROM anime_config_candidates WHERE id=$1`, f.candidateID).Scan(&f.candidateVersion, &f.revisionDigest); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT business_digest FROM anime_config_candidate_revisions WHERE candidate_id=$1 AND revision=$2`, f.candidateID, f.revision).Scan(&f.revisionBusiness); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO anime_business_configs(managed_anime_id, candidate_id, revision, business_snapshot, business_digest, business_version)
SELECT $1, candidate_id, revision, business_snapshot, business_digest, 1 FROM anime_config_candidate_revisions WHERE candidate_id=$2 AND revision=$3
ON CONFLICT (managed_anime_id) DO UPDATE SET candidate_id=EXCLUDED.candidate_id, revision=EXCLUDED.revision, business_snapshot=EXCLUDED.business_snapshot, business_digest=EXCLUDED.business_digest, business_version=EXCLUDED.business_version, updated_at=NOW()`, f.animeID, f.candidateID, f.revision); err != nil {
		t.Fatal(err)
	}
	f.businessDigest = f.revisionBusiness

	group := publishGroup(t, db, groupResult())
	f.groupID, f.groupVersion = group.ID, group.Version
	if err := db.QueryRowContext(ctx, `SELECT observation_digest FROM provider_group_snapshots WHERE id=$1`, f.groupID).Scan(&f.groupDigest); err != nil {
		t.Fatal(err)
	}

	payload := []byte("qualified torrent bytes " + tag)
	f.payloadDigest = catalogdomain.DigestBytes(payload)
	size := int64(len(payload))
	file := catalogdomain.TorrentFile{Ordinal: 0, SourcePath: "show/ep01.mkv", Filename: "ep01.mkv", SizeBytes: 1000, MediaKind: "video", Language: "jpn"}
	file.FileRef = catalogdomain.TorrentFileRef(f.payloadDigest, file.SourcePath)
	f.projectionDigest, err = catalogdomain.CanonicalTorrentProjectionDigest(f.payloadDigest, "parser-v1", []catalogdomain.TorrentFile{file})
	if err != nil {
		t.Fatal(err)
	}
	seeds := []catalogdomain.ProviderFeedSnapshotSeed{{StableSeedKey: "seed", Locator: "https://example.test/q.torrent", LocatorDisplay: "example.test/q.torrent", Title: "Qualified", SizeBytes: &size,
		Payload: payload, PayloadDigest: &f.payloadDigest, TorrentState: "available", Manifest: &catalogdomain.TorrentManifest{PayloadDigest: f.payloadDigest, ParserVersion: "parser-v1", TorrentVersion: "v1", TorrentName: "show",
			InfoHashV1: strings.Repeat("a", 40), TotalSizeBytes: 1000, ProjectionDigest: f.projectionDigest, Files: []catalogdomain.TorrentFile{file}}}}
	hash, err := catalogdomain.CanonicalFeedSnapshotContentHash(seeds)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	// Group "10" is the known-available group of groupResult's observation.
	f.feed, err = persistence.NewRepository(db).PersistProviderFeedSnapshot(ctx, catalogdomain.ProviderFeedSnapshotWrite{Provider: "mikan", ProviderEntryRef: "123", ProviderGroupRef: "10", FetchedAt: now, FreshUntil: now.Add(15 * time.Minute),
		ContentHash: hash, HashSchema: catalogdomain.ProviderFeedSnapshotContentHashSchema, ProjectionSchema: catalogdomain.TorrentManifestProjectionSchema, Seeds: seeds})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO anime_business_configs(managed_anime_id, parser_kind, parser_source_default_version, parser_version, admission_mode, quality_profile, retention_source_default_version, retention_version, unclassified_route, file_effect_source_default_version, file_effect_version) VALUES($1,'regex',1,1,'all','{"version":"profile-v1","items":[]}',1,1,'skip',0,1) ON CONFLICT (managed_anime_id) DO UPDATE SET parser_kind=EXCLUDED.parser_kind, parser_source_default_version=EXCLUDED.parser_source_default_version, parser_version=EXCLUDED.parser_version, admission_mode=EXCLUDED.admission_mode, quality_profile=EXCLUDED.quality_profile, retention_source_default_version=EXCLUDED.retention_source_default_version, retention_version=EXCLUDED.retention_version, unclassified_route=EXCLUDED.unclassified_route, file_effect_source_default_version=EXCLUDED.file_effect_source_default_version, file_effect_version=EXCLUDED.file_effect_version`, f.animeID); err != nil {
		t.Fatal(err)
	}

	return f
}

// qualification builds a fresh owner-config QualificationV1 value over the
// fixture; mutate edits the decoded tree before it is encoded.
func (f qualificationFixture) qualification(mutate func(map[string]any)) string {
	scope := func() map[string]any { return map[string]any{"provider": "mikan", "catalog_entry_ref": "123"} }
	q := map[string]any{
		"config": map[string]any{"kind": "owner", "managed_anime_ref": f.animeID, "config_version": 1, "config_digest": f.businessDigest},
		"management": map[string]any{"kind": "instance", "managed_anime_ref": f.animeID, "subscription_ref": f.subID,
			"expected_subscription_version": 1, "expected_lifecycle_version": 1, "expected_metadata_version": 1, "expected_parser_policy_version": 1, "expected_rules_owner_version": 1, "expected_subgroups_owner_version": 1, "subscription_policy": "automatic_active"},
		"source":      map[string]any{"kind": "release", "release_ref": f.relID, "expected_version": 1, "source_ref": map[string]any{"ref": f.srcID, "version": 1}},
		"observation": map[string]any{"scope": scope(), "snapshot_ref": f.groupID, "snapshot_version": f.groupVersion, "observation_digest": f.groupDigest},
		"feed": map[string]any{"snapshot_ref": f.feed.ID, "snapshot_version": f.feed.Version, "scope": scope(), "group_ref": "10",
			"content_hash": f.feed.ContentHash, "hash_schema": "provider-feed-snapshot-v1", "projection_schema": "torrent-manifest-v1",
			"stable_seed_key": "seed", "payload_digest": f.payloadDigest, "manifest_digest": f.projectionDigest},
		"programs":  map[string]any{"attr_fingerprint": f.attrDigest, "vars_digest": f.varsDigest, "translation": nil},
		"placement": map[string]any{"kind": "none"},
	}
	if mutate != nil {
		mutate(q)
	}
	if q["config"].(map[string]any)["kind"] != "owner" {
		q["management"].(map[string]any)["expected_parser_policy_version"] = nil
		q["management"].(map[string]any)["expected_rules_owner_version"] = nil
		q["management"].(map[string]any)["expected_subgroups_owner_version"] = nil
	}
	raw, _ := json.Marshal(q)
	return string(raw)
}

func member(q map[string]any, name string) map[string]any { return q[name].(map[string]any) }

func recheckQualification(db queryRower, qualification, requestHash, configDigest, payloadDigest string) (time.Time, error) {
	var at time.Time
	err := db.QueryRowContext(context.Background(), `SELECT recheck_acquisition_qualification($1::jsonb,$2,$3,$4)`,
		qualification, requestHash, configDigest, payloadDigest).Scan(&at)
	return at, err
}

func expectQualificationError(t *testing.T, err error, message string) {
	t.Helper()
	if sqlState(err) != "23514" || adoptAcceptPGMessage(err) != message {
		t.Fatalf("want %s, got %v", message, err)
	}
}

func setupQualification(t *testing.T) (*sql.DB, *sql.DB) {
	t.Helper()
	owner := installedDB(t)
	ctx := context.Background()
	password := randomTestPassword(t)
	if err := database.ProvisionRuntimeRole(ctx, owner, database.RuntimeRoleConfig{Role: runtimeRoleTestName, Password: password}); err != nil {
		t.Fatal(err)
	}
	runtime := openRuntimeConnection(t, password)
	t.Cleanup(func() { runtime.Close() })
	return owner, runtime
}

func TestAcquisitionQualificationRecheckClaimsAndFailClosedProofs(t *testing.T) {
	owner, runtime := setupQualification(t)
	f := seedQualification(t, owner, "recheck")
	ctx := context.Background()
	request := acqDigest("6")

	var rootsBefore int
	if err := owner.QueryRowContext(ctx, `SELECT count(*) FROM acquisitions`).Scan(&rootsBefore); err != nil {
		t.Fatal(err)
	}
	before := time.Now().Add(-time.Minute)
	for _, tc := range []struct {
		name         string
		mutate       func(map[string]any)
		configDigest string
	}{
		{"owner business", nil, f.businessDigest},
		{"saved current revision", func(q map[string]any) {
			q["config"] = map[string]any{"kind": "saved_revision", "candidate_ref": f.candidateID, "candidate_version": f.candidateVersion, "revision": f.revision, "revision_digest": f.revisionDigest}
			member(q, "management")["expected_metadata_version"] = nil
		}, f.revisionBusiness},
		{"submitted body", func(q map[string]any) {
			q["config"] = map[string]any{"kind": "submitted", "request_hash": request, "config_digest": acqDigest("7")}
			member(q, "management")["expected_metadata_version"] = nil
		}, acqDigest("7")},
		{"no vars claim no observation", func(q map[string]any) {
			member(q, "programs")["vars_digest"] = nil
			q["observation"] = nil
		}, f.businessDigest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := f.qualification(tc.mutate)
			if _, err := protocol.DecodeQualification([]byte(raw)); err != nil {
				t.Fatalf("Go registry rejects the SQL-accepted value: %v", err)
			}
			at, err := recheckQualification(runtime, raw, request, tc.configDigest, f.payloadDigest)
			if err != nil {
				t.Fatal(err)
			}
			if at.Before(before) || at.After(time.Now().Add(time.Minute)) {
				t.Fatalf("verdict time %v is not the DB clock", at)
			}
		})
	}

	for _, tc := range []struct {
		name, message string
		mutate        func(map[string]any)
		configDigest  string
		payload       string
	}{
		{"unknown member", "ACQ_QUALIFICATION_INVALID", func(q map[string]any) { q["skip_guard"] = true }, "", ""},
		{"missing member", "ACQ_QUALIFICATION_INVALID", func(q map[string]any) { delete(q, "feed") }, "", ""},
		{"union leakage", "ACQ_QUALIFICATION_INVALID", func(q map[string]any) { member(q, "config")["revision"] = 1 }, "", ""},
		{"unknown config kind", "ACQ_QUALIFICATION_INVALID", func(q map[string]any) { member(q, "config")["kind"] = "global" }, "", ""},
		{"version as text", "ACQ_QUALIFICATION_INVALID", func(q map[string]any) { member(q, "management")["expected_lifecycle_version"] = "1" }, "", ""},
		{"management none with release", "ACQ_QUALIFICATION_INVALID", func(q map[string]any) {
			q["management"] = map[string]any{"kind": "none"}
			q["config"] = map[string]any{"kind": "submitted", "request_hash": acqDigest("6"), "config_digest": f.businessDigest}
			member(q, "management")["expected_metadata_version"] = nil
		}, "", ""},
		{"existing placement", "ACQ_QUALIFICATION_PROOF_UNSUPPORTED", func(q map[string]any) {
			q["placement"] = map[string]any{"kind": "existing", "placement_ref": "p", "expected_revision": 1, "file_fact_digest": acqDigest("8")}
		}, "", ""},
		{"translation identity", "ACQ_QUALIFICATION_PROOF_UNSUPPORTED", func(q map[string]any) {
			member(q, "programs")["translation"] = map[string]any{"schema_version": "v1", "data_version": "v1", "content_digest": acqDigest("9")}
		}, "", ""},
		{"seed payload identity", "ACQ_QUALIFICATION_PROOF_UNSUPPORTED", func(q map[string]any) {
			q["source"] = map[string]any{"kind": "seed", "seed_identity_digest": acqDigest("a")}
		}, "", ""},
		{"lifecycle version", "ACQ_QUALIFICATION_MANAGEMENT_CHANGED", func(q map[string]any) { member(q, "management")["expected_lifecycle_version"] = 2 }, "", ""},
		{"subscription version", "ACQ_QUALIFICATION_MANAGEMENT_CHANGED", func(q map[string]any) { member(q, "management")["expected_subscription_version"] = 2 }, "", ""},
		{"other owner subscription", "ACQ_QUALIFICATION_MANAGEMENT_CHANGED", func(q map[string]any) { member(q, "management")["subscription_ref"] = "sub-absent" }, "", ""},
		{"owner config version", "ACQ_QUALIFICATION_CONFIG_CHANGED", func(q map[string]any) { member(q, "config")["config_version"] = 2 }, "", ""},
		{"owner body digest", "ACQ_QUALIFICATION_CONFIG_CHANGED", nil, acqDigest("b"), ""},
		{"submitted request", "ACQ_QUALIFICATION_CONFIG_CHANGED", func(q map[string]any) {
			q["config"] = map[string]any{"kind": "submitted", "request_hash": acqDigest("c"), "config_digest": f.businessDigest}
			member(q, "management")["expected_metadata_version"] = nil
		}, "", ""},
		{"saved stale revision", "ACQ_QUALIFICATION_CONFIG_CHANGED", func(q map[string]any) {
			q["config"] = map[string]any{"kind": "saved_revision", "candidate_ref": f.candidateID, "candidate_version": f.candidateVersion, "revision": f.revision + 1, "revision_digest": f.revisionDigest}
			member(q, "management")["expected_metadata_version"] = nil
		}, f.revisionBusiness, ""},
		{"saved stale candidate version", "ACQ_QUALIFICATION_CONFIG_CHANGED", func(q map[string]any) {
			q["config"] = map[string]any{"kind": "saved_revision", "candidate_ref": f.candidateID, "candidate_version": f.candidateVersion + 1, "revision": f.revision, "revision_digest": f.revisionDigest}
			member(q, "management")["expected_metadata_version"] = nil
		}, f.revisionBusiness, ""},
		{"attr program", "ACQ_QUALIFICATION_PROGRAM_CHANGED", func(q map[string]any) { member(q, "programs")["attr_fingerprint"] = acqDigest("d") }, "", ""},
		{"vars program", "ACQ_QUALIFICATION_PROGRAM_CHANGED", func(q map[string]any) { member(q, "programs")["vars_digest"] = acqDigest("d") }, "", ""},
		{"source version", "ACQ_QUALIFICATION_SOURCE_CHANGED", func(q map[string]any) { member(member(q, "source"), "source_ref")["version"] = 2 }, "", ""},
		{"release version", "ACQ_QUALIFICATION_SOURCE_CHANGED", func(q map[string]any) { member(q, "source")["expected_version"] = 2 }, "", ""},
		{"release of other source", "ACQ_QUALIFICATION_SOURCE_CHANGED", func(q map[string]any) { member(q, "source")["release_ref"] = "rel-absent" }, "", ""},
		{"missing group snapshot", "ACQ_QUALIFICATION_OBSERVATION_CHANGED", func(q map[string]any) { member(q, "observation")["snapshot_ref"] = "group-absent" }, "", ""},
		{"group digest", "ACQ_QUALIFICATION_OBSERVATION_CHANGED", func(q map[string]any) { member(q, "observation")["observation_digest"] = acqDigest("e") }, "", ""},
		{"group snapshot version", "ACQ_QUALIFICATION_OBSERVATION_CHANGED", func(q map[string]any) { member(q, "observation")["snapshot_version"] = f.groupVersion + 1 }, "", ""},
		{"feed content", "ACQ_QUALIFICATION_FEED_CHANGED", func(q map[string]any) { member(q, "feed")["content_hash"] = acqDigest("f") }, "", ""},
		{"feed group", "ACQ_QUALIFICATION_FEED_CHANGED", func(q map[string]any) { member(q, "feed")["group_ref"] = "20" }, "", ""},
		{"missing seed", "ACQ_QUALIFICATION_FEED_CHANGED", func(q map[string]any) { member(q, "feed")["stable_seed_key"] = "absent" }, "", ""},
		{"seed manifest", "ACQ_QUALIFICATION_FEED_CHANGED", func(q map[string]any) { member(q, "feed")["manifest_digest"] = acqDigest("1") }, "", ""},
		{"captured payload", "ACQ_QUALIFICATION_FEED_CHANGED", nil, "", acqDigest("2")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			configDigest, payload := tc.configDigest, tc.payload
			if configDigest == "" {
				configDigest = f.businessDigest
			}
			if payload == "" {
				payload = f.payloadDigest
			}
			_, err := recheckQualification(runtime, f.qualification(tc.mutate), request, configDigest, payload)
			expectQualificationError(t, err, tc.message)
		})
	}

	var rootsAfter int
	if err := owner.QueryRowContext(ctx, `SELECT count(*) FROM acquisitions`).Scan(&rootsAfter); err != nil || rootsAfter != rootsBefore {
		t.Fatalf("recheck wrote roots: %d -> %d %v", rootsBefore, rootsAfter, err)
	}
	var granted, public bool
	if err := owner.QueryRowContext(ctx, `SELECT has_function_privilege('bansoko_runtime',p.oid,'EXECUTE'),has_function_privilege('public',p.oid,'EXECUTE')
FROM pg_proc p WHERE p.proname='recheck_acquisition_qualification'`).Scan(&granted, &public); err != nil || !granted || public {
		t.Fatalf("EXECUTE runtime=%t public=%t %v", granted, public, err)
	}
}

func TestAcquisitionQualificationRejectsChangedOwnerState(t *testing.T) {
	owner, runtime := setupQualification(t)
	ctx := context.Background()
	for i, tc := range []struct {
		name, statement, message string
		arg                      func(f qualificationFixture) any
		mutate                   func(map[string]any)
	}{
		{"stopped subscription", `UPDATE subscriptions SET state='stopped' WHERE id=$1`, "ACQ_QUALIFICATION_MANAGEMENT_CHANGED",
			func(f qualificationFixture) any { return f.subID }, nil},
		{"retired instance", `UPDATE managed_anime SET lifecycle_state='retired',fence_epoch=fence_epoch+1,retired_at=now(),retired_subscription_version=1,lifecycle_version=lifecycle_version+1 WHERE id=$1`,
			"ACQ_QUALIFICATION_MANAGEMENT_CHANGED", func(f qualificationFixture) any { return f.animeID },
			func(q map[string]any) { member(q, "management")["expected_lifecycle_version"] = 2 }},
		{"expired feed", `UPDATE provider_feed_snapshots SET fresh_until=fetched_at WHERE id=$1`, "ACQ_QUALIFICATION_FEED_EXPIRED",
			func(f qualificationFixture) any { return f.feed.ID }, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := seedQualification(t, owner, "state"+string(rune('a'+i)))
			if _, err := owner.ExecContext(ctx, tc.statement, tc.arg(f)); err != nil {
				t.Fatal(err)
			}
			_, err := recheckQualification(runtime, f.qualification(tc.mutate), acqDigest("6"), f.businessDigest, f.payloadDigest)
			expectQualificationError(t, err, tc.message)
		})
	}
}

// waitBlocked returns once the backend pid waits on another lock holder.
func waitBlocked(ctx context.Context, t *testing.T, db *sql.DB, pid int, done <-chan error) {
	t.Helper()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var blocked bool
		if err := db.QueryRowContext(ctx, `SELECT cardinality(pg_blocking_pids($1))>0`, pid).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			return
		}
		select {
		case err := <-done:
			t.Fatalf("statement bypassed the lock: %v", err)
		case <-ctx.Done():
			t.Fatal("statement never reached the lock")
		case <-ticker.C:
		}
	}
}

// A feed that expires while the recheck waits for its row lock is judged by
// the post-lock DB clock, not by the freshness seen before waiting.
func TestAcquisitionFeedExpiryAfterLockWaitIsRejected(t *testing.T) {
	owner, runtime := setupQualification(t)
	ctx := context.Background()
	f := seedQualification(t, owner, "wait")
	holder, err := owner.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Rollback()
	if _, err := holder.ExecContext(ctx, `UPDATE provider_feed_snapshots SET fresh_until=fetched_at WHERE id=$1`, f.feed.ID); err != nil {
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
	callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := recheckQualification(conn, f.qualification(nil), acqDigest("6"), f.businessDigest, f.payloadDigest)
		done <- err
	}()
	waitBlocked(callCtx, t, owner, pid, done)
	if err := holder.Commit(); err != nil {
		t.Fatal(err)
	}
	expectQualificationError(t, <-done, "ACQ_QUALIFICATION_FEED_EXPIRED")
}

// The recheck's parent FOR SHARE locks keep the exact feed/group/program rows
// stable for the rest of the accepting transaction: GC deletes and active
// program publication wait until it ends.
func TestAcquisitionRecheckLocksBlockGCAndPublication(t *testing.T) {
	owner, runtime := setupQualification(t)
	ctx := context.Background()
	for i, tc := range []struct {
		name, statement string
		arg             func(f qualificationFixture) any
	}{
		{"feed GC", `DELETE FROM provider_feed_snapshots WHERE id=$1`, func(f qualificationFixture) any { return f.feed.ID }},
		{"group GC", `DELETE FROM provider_group_snapshots WHERE id=$1`, func(f qualificationFixture) any { return f.groupID }},
		{"attr publication", `UPDATE attr_rule_versions SET active=active WHERE fingerprint=$1`, func(f qualificationFixture) any { return f.attrDigest }},
		{"vars publication", `UPDATE naming_vars_versions SET active=active WHERE fingerprint=$1`, func(f qualificationFixture) any { return f.varsDigest }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := seedQualification(t, owner, "lock"+string(rune('a'+i)))
			tx, err := runtime.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if _, err := recheckQualification(tx, f.qualification(nil), acqDigest("6"), f.businessDigest, f.payloadDigest); err != nil {
				t.Fatal(err)
			}
			conn, err := owner.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			var pid int
			if err := conn.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
				t.Fatal(err)
			}
			callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, err := conn.ExecContext(callCtx, tc.statement, tc.arg(f))
				done <- err
			}()
			waitBlocked(callCtx, t, owner, pid, done)
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}
