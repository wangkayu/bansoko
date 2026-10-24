// Package acquisitionfixture seeds acquisition roots for PostgreSQL tests
// through the owner-held entries (observed download manifest, plan commit).
package acquisitionfixture

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	protocol "bansoko/internal/acquisition/terminal/domain"
	"bansoko/internal/acquisition/terminal/varsevidence"
	anime "bansoko/internal/anime/domain"
	"bansoko/internal/namingvars"
	"bansoko/internal/testutil/frozenpolicy"
	"bansoko/internal/testutil/nativeinput"
)

// DownloadDB is an owner connection or owner transaction. Accepted-graph
// arrangement is fixture owner work; runtime handles are refused up front.
type DownloadDB interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// SeedDownload accepts a queued download through the owner entry. Callers may
// arrange observed task state afterwards; frozen budgets are never bypassed.
func SeedDownload(t *testing.T, db DownloadDB, root, task, release, animeID, subscription, source, key string, payload []byte, hash, frozen string) {
	seedDownloadManifest(t, db, root, task, release, animeID, subscription, source, key, payload, hash, frozen, `[{"file_ref":"file-a","ordinal":0,"relative_path":"file-a.mkv","exact_size":1,"media_kind":"video","identity_evidence":{}}]`)
}

// SeedDownloadManifest arranges the exact accepted manifest for downstream inspection tests.
func SeedDownloadManifest(t *testing.T, db DownloadDB, root, task, release, animeID, subscription, source, key string, payload []byte, hash, frozen, manifest string) {
	seedDownloadManifest(t, db, root, task, release, animeID, subscription, source, key, payload, hash, frozen, manifest)
}

func seedDownloadManifest(t *testing.T, db DownloadDB, root, task, release, animeID, subscription, source, key string, payload []byte, hash, frozen, manifest string) {
	t.Helper()
	ctx := context.Background()
	var owner bool
	if err := db.QueryRowContext(ctx, `SELECT has_table_privilege(current_user,'public.work_items','UPDATE')`).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if !owner {
		t.Fatal("acquisitionfixture requires an owner connection; runtime handles cannot arrange accepted work")
	}
	business, err := frozenpolicy.SeedBusiness(ctx, db, `vars.actual = {title: input.release_title};`)
	if err != nil {
		t.Fatal(err)
	}
	businessDigest, err := business.Digest()
	if err != nil {
		t.Fatal(err)
	}
	digest := func(b []byte) string { sum := sha256.Sum256(b); return "sha256:" + hex.EncodeToString(sum[:]) }
	input := nativeinput.Canonical(t, digest(payload), businessDigest)
	var body map[string]any
	if err := json.Unmarshal([]byte(input), &body); err != nil {
		t.Fatal(err)
	}
	body["source"].(map[string]any)["kind"] = "subscription_direct"
	body["source"].(map[string]any)["manifest_digest"] = digest([]byte(root))
	// Golden DTO templates demonstrate wire values, not executable JS. Downstream
	// application fixtures must freeze the actual policy's naming sections.
	var frozenPolicy struct {
		// RouteRoot is the delivery route root the accepted input freezes: the
		// work's tree top, which is also what the notify projectors relativize
		// an unrecognized file's path against.
		RouteRoot   string          `json:"route_root"`
		Sections    map[string]any  `json:"effective_sections"`
		Vars        json.RawMessage `json:"vars"`
		MediaValues map[string]any  `json:"fixture_media_values"`
		RouteValues map[string]any  `json:"fixture_route_values"`
		VarsSource  string          `json:"fixture_vars_source"`
		// AnimeYear is the owner-edited year (anime_metadata.year) the frozen
		// Vars input carries as facts.anime.year; nil leaves it unset.
		AnimeYear *int64 `json:"fixture_anime_year"`
		// AnimeSeason is the owner-edited season (anime_metadata.season) the
		// frozen Vars input carries as facts.anime.season; nil leaves it unset.
		AnimeSeason *int64 `json:"fixture_anime_season"`
		// ReplacementTarget is the accepted ReplacementV1 target for an
		// overwrite delivery. persistOperationPlan refuses an overwrite
		// operation unless the accepted input carries the binding naming the
		// very path the planner renders, so a caller that wants the overwrite
		// chain must supply it here: the binding has to exist before the accept
		// function computes admission_digest and the vars evidence, because
		// those bind the canonical bytes and cannot be rewritten afterwards.
		ReplacementTarget string `json:"fixture_replacement_target"`
		// ReplacementBinding is the FULL accepted ReplacementV1 frozen value
		// (state/value/reason_code). When present it is injected verbatim,
		// replacing the thin location stub: the admission compares the binding's
		// expected_object location/size/identity/content.sha256 against the
		// frozen prepare observation of the incumbent, so only the complete
		// observed object passes FILE_PREPARE_OVERWRITE_PROOF_REQUIRED.
		ReplacementBinding json.RawMessage `json:"fixture_replacement_binding"`
	}
	if err := json.Unmarshal([]byte(frozen), &frozenPolicy); err != nil {
		t.Fatal(err)
	}
	if frozenPolicy.RouteRoot != "" {
		body["delivery"].(map[string]any)["route"].(map[string]any)["root"] = map[string]any{"state": "known", "value": frozenPolicy.RouteRoot, "reason_code": nil}
	}
	if len(frozenPolicy.Sections) > 0 {
		for _, entry := range body["config"].(map[string]any)["sections"].([]any) {
			section := entry.(map[string]any)
			if value, ok := frozenPolicy.Sections[section["section_key"].(string)]; ok {
				section["value"] = value
			}
		}
		if rules, ok := frozenPolicy.Sections["naming_rules"]; ok {
			body["delivery"].(map[string]any)["naming"].(map[string]any)["templates"] = rules
		}
		if effect, ok := frozenPolicy.Sections["file_effect"].(map[string]any); ok {
			for _, key := range []string{"operation", "collision_policy"} {
				if v, ok := effect[key]; ok {
					if key == "collision_policy" {
						v = map[string]string{"fail": "block", "versioned": "version", "overwrite": "overwrite"}[fmt.Sprint(v)]
					}
					body["delivery"].(map[string]any)[map[string]string{"operation": "operation", "collision_policy": "collision"}[key]] = v
				}
			}
		}
	}
	if len(frozenPolicy.Vars) > 0 {
		var v struct {
			Program map[string]any `json:"program"`
			Input   struct {
				ReleaseTitle string `json:"release_title"`
				Files        []any  `json:"files"`
			} `json:"input"`
			Output     map[string]any `json:"output"`
			Status     string         `json:"status"`
			Diagnostic string         `json:"diagnostic"`
		}
		if err := json.Unmarshal(frozenPolicy.Vars, &v); err != nil {
			t.Fatal(err)
		}
		if v.Status == "ok" {
			files := v.Input.Files
			if files == nil {
				files = []any{}
			}
			output := v.Output
			if output == nil {
				output = map[string]any{}
			}
			var sourceJSON []byte
			if err := db.QueryRowContext(ctx, `SELECT content FROM naming_vars_versions WHERE fingerprint=$1`, fmt.Sprint(v.Program["digest"])).Scan(&sourceJSON); err != nil {
				// The complete policy fixture owns its source, not a published active program.
				fixtureSource := `vars.fixture = {title: input.release_title};`
				if frozenPolicy.VarsSource != "" {
					fixtureSource = frozenPolicy.VarsSource
				}
				fp, _ := namingvars.SourceFingerprint(fixtureSource)
				if fmt.Sprint(v.Program["digest"]) != fp {
					t.Fatalf("exact fixture Vars source unavailable: %v", err)
				}
				if _, err := frozenpolicy.SeedBusiness(ctx, db, fixtureSource); err != nil {
					t.Fatal(err)
				}
				sourceJSON, _ = json.Marshal(fixtureSource)
			}
			var programSource string
			if err := json.Unmarshal(sourceJSON, &programSource); err != nil {
				t.Fatal(err)
			}
			runtime, _ := namingvars.RuntimeOf(namingvars.ProgramRef{Digest: fmt.Sprint(v.Program["digest"]), Version: fmt.Sprint(v.Program["version"])})
			v.Program = map[string]any{"schema_version": 1, "source": programSource, "digest": v.Program["digest"], "version": v.Program["version"], "runtime_version": runtime, "timeout_ms": 50, "max_input_bytes": 262144, "max_output_bytes": 8192, "max_collection_len": 256, "max_string_bytes": 8192}
			body["delivery"].(map[string]any)["naming"].(map[string]any)["vars_choice"] = "frozen"
			// VarsInputV1 freezes the fact object the program read, so the fixture
			// builds it from the same release evidence the real evaluation uses.
			fileBytes, err := json.Marshal(files)
			if err != nil {
				t.Fatal(err)
			}
			var namingFiles []namingvars.File
			if err := json.Unmarshal(fileBytes, &namingFiles); err != nil {
				t.Fatal(err)
			}
			facts := namingvars.BuildInput(namingvars.ReleaseEvidence{AnimeEvidence: namingvars.AnimeEvidence{AnimeYear: frozenPolicy.AnimeYear, AnimeSeason: frozenPolicy.AnimeSeason}, ReleaseTitle: v.Input.ReleaseTitle, Files: namingFiles}).Facts
			known := map[string]any{"release_title": map[string]any{"state": "known", "value": v.Input.ReleaseTitle, "reason_code": nil}, "files": files, "facts": facts}
			body["config"].(map[string]any)["vars"] = map[string]any{"mode": "frozen", "program": v.Program, "known_inputs": known, "output": output, "status": v.Status, "reason_code": nil}
		}
	}
	// Owner-arranged test inputs must be frozen before acceptance, never patched later.
	for key, value := range frozenPolicy.MediaValues {
		body["media"].(map[string]any)[key] = value
	}
	for key, value := range frozenPolicy.RouteValues {
		body["delivery"].(map[string]any)["route"].(map[string]any)[key] = value
	}
	if frozenPolicy.ReplacementTarget != "" {
		if effect, ok := frozenPolicy.Sections["file_effect"].(map[string]any); ok {
			if fmt.Sprint(effect["collision_policy"]) == "overwrite" {
				// The accepted replacement binding for an overwrite delivery.
				// intent/replacement live on delivery, and the path must equal
				// what the planner renders for this file. A caller that froze a
				// full observed binding (fixture_replacement_binding) injects it
				// verbatim; the thin canonical_path stub remains the historical
				// shape for callers that only need the plan-side binding check.
				body["delivery"].(map[string]any)["intent"] = "replace"
				var replacement any = map[string]any{
					"expected_object": map[string]any{
						"location": map[string]any{"canonical_path": frozenPolicy.ReplacementTarget},
					},
				}
				if len(frozenPolicy.ReplacementBinding) > 0 {
					// Inline the frozen binding bytes verbatim: a map[string]any
					// roundtrip would coerce the identity nanosecond integers to
					// float64 and silently corrupt them beyond float64 precision,
					// which the admission's exact identity equality rejects. The fixture
					// carries the frozen value (state/value/reason_code); the
					// delivery replacements list holds the bare value.
					var binding struct {
						Value json.RawMessage `json:"value"`
					}
					if err := json.Unmarshal(frozenPolicy.ReplacementBinding, &binding); err == nil && len(binding.Value) > 0 {
						replacement = binding.Value
					} else {
						replacement = json.RawMessage(frozenPolicy.ReplacementBinding)
					}
				}
				body["delivery"].(map[string]any)["replacements"] = []any{replacement}
			}
		}
	}
	// The accept freezes the torrent totals from exactly this manifest and
	// refuses a mismatch, so the fixture derives them from the same rows.
	var manifestRows []struct {
		ExactSize int64 `json:"exact_size"`
	}
	if err := json.Unmarshal([]byte(manifest), &manifestRows); err != nil {
		t.Fatal(err)
	}
	var torrentSize int64
	for _, row := range manifestRows {
		torrentSize += row.ExactSize
	}
	body["source"].(map[string]any)["torrent_size"] = map[string]any{"state": "known", "value": torrentSize, "reason_code": nil}
	body["source"].(map[string]any)["torrent_file_count"] = map[string]any{"state": "known", "value": len(manifestRows), "reason_code": nil}
	bodyRaw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	value, err := protocol.ParseStrict(bodyRaw)
	if err != nil {
		t.Fatal(err)
	}
	canonical := protocol.Canonical(value)
	encode := func(v any) string {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	d := digest([]byte(root))
	var sourceVersion, releaseVersion, subscriptionVersion, lifecycleVersion int64
	if err := db.QueryRowContext(ctx, `SELECT s.version,r.version,sub.version,l.lifecycle_version FROM rss_sources s JOIN releases r ON r.rss_source_id=s.id JOIN subscriptions sub ON sub.id=$3 JOIN managed_anime l ON l.id=$4 WHERE s.id=$1 AND r.id=$2`, source, release, subscription, animeID).Scan(&sourceVersion, &releaseVersion, &subscriptionVersion, &lifecycleVersion); err != nil {
		t.Fatal(err)
	}
	qualification := encode(map[string]any{
		"config":      map[string]any{"kind": "submitted", "request_hash": d, "config_digest": businessDigest},
		"management":  map[string]any{"kind": "instance", "managed_anime_ref": animeID, "subscription_ref": subscription, "expected_subscription_version": subscriptionVersion, "expected_lifecycle_version": lifecycleVersion, "expected_metadata_version": nil, "expected_parser_policy_version": nil, "expected_rules_owner_version": nil, "expected_subgroups_owner_version": nil, "subscription_policy": "manual_active_or_stopped"},
		"source":      map[string]any{"kind": "release", "release_ref": release, "expected_version": releaseVersion, "source_ref": map[string]any{"ref": source, "version": sourceVersion}},
		"observation": nil, "feed": nil, "programs": map[string]any{"attr_fingerprint": business.Programs.AttrRules.Digest, "vars_digest": business.Programs.Vars.Digest, "translation": nil}, "placement": map[string]any{"kind": "none"},
	})
	defaults := map[string]json.RawMessage{}
	for _, name := range anime.EffectiveSectionOrder {
		value, err := anime.DefaultSectionValue(name)
		if err != nil {
			t.Fatal(err)
		}
		var section map[string]any
		if err := json.Unmarshal([]byte(encode(value)), &section); err != nil {
			t.Fatal(err)
		}
		if name == anime.SectionNamingRules {
			section["preset"] = nil
		}
		if name == anime.SectionSubgroups {
			section["name_keywords"] = nil
			section["provider_defaults"] = nil
		}
		defaults[name] = json.RawMessage(encode(section))
	}
	args := []any{root, "one_shot", "deployment_owner_v1", "manual_acquire_v1", root, d, `{}`, protocol.Digest("acquisition-input:v1", canonical), encode(map[string]any{"kind": "one_shot", "intent_digest": d}), `{"state":"known","value":"fixture","reason_code":null}`, animeID, 2592000, 2592000, 5184000, "none", nil, nil, string(canonical), payload, hash, nil, d, manifest, release, task, key, nil, frozen, `{}`, "fixture-submit-" + task, "fixture-settle-" + task, "fixture-settle-" + task, `{}`, 3, qualification, encode(business), 0, 0, "fixture-corr", "release", release, encode(defaults)}
	var evidenceProtocol bool
	if err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_proc WHERE pronamespace='public'::regnamespace AND proname='accept_acquisition_queued_with_vars')`).Scan(&evidenceProtocol); err != nil {
		t.Fatal(err)
	}
	entry := "accept_acquisition_queued"
	if evidenceProtocol {
		entry = "accept_acquisition_queued_with_vars"
	}
	// A new-protocol frozen fixture records a completion through the production
	// wrapper for the fixture's own expected output. The program is still
	// checked (digest and compile), but the time-limited evaluator is not run,
	// so a loaded host cannot fail the seed. Historical lanes never backfill a
	// record or fabricate an earlier time.
	if evidenceProtocol {
		var record any
		vars := value.Object["config"].Object["vars"]
		if vars.Object["mode"].String == "frozen" {
			p := vars.Object["program"].Object
			if _, err := namingvars.NewProgram(namingvars.ProgramRef{Digest: p["digest"].String, Version: p["version"].String}, p["source"].String); err != nil {
				t.Fatal(err)
			}
			var output namingvars.Vars
			if err := json.Unmarshal(protocol.Canonical(vars.Object["output"]), &output); err != nil {
				t.Fatal(err)
			}
			known := vars.Object["known_inputs"].Object
			var files []namingvars.File
			if err := json.Unmarshal(protocol.Canonical(known["files"]), &files); err != nil {
				t.Fatal(err)
			}
			completion, err := varsevidence.Evaluate(ctx, fixedVarsOutput(output), namingvars.Input{ReleaseTitle: known["release_title"].Object["value"].String, Files: files}, varsevidence.ServerClock)
			if err != nil {
				t.Fatal(err)
			}
			original, err := completion.Record(vars, root, args[7].(string))
			if err != nil {
				t.Fatalf("fixture output differs from actual evaluation: %v", err)
			}
			record = original
		}
		args = append(args, record)
	}
	params := make([]string, len(args))
	for i := range params {
		params[i] = fmt.Sprintf("$%d", i+1)
	}
	var raw []byte
	if err := db.QueryRowContext(ctx, "SELECT "+entry+"("+strings.Join(params, ",")+")", args...).Scan(&raw); err != nil {
		t.Fatalf("seed queued download: %v", err)
	}
	// Fixture-created submit is terminally owner-arranged so each test can create
	// its own work and exercise ClaimDue without competing with acceptance work.
	if _, err := db.ExecContext(ctx, `SELECT public.work_item_close(id,'succeeded') FROM work_items WHERE id=$1`, "fixture-submit-"+task); err != nil {
		t.Fatal(err)
	}
}

// SeedObservedDownload creates the accepted owner graph before arranging an
// observation for downstream tests. db may be the fixture transaction itself.
func SeedObservedDownload(t *testing.T, db DownloadDB, root, task, release, key, frozen, policy, state, correlation string) {
	SeedObservedDownloadManifest(t, db, root, task, release, key, frozen, policy, state, correlation, `[{"file_ref":"file-a","ordinal":0,"relative_path":"file-a.mkv","exact_size":1,"media_kind":"video","identity_evidence":{}}]`)
}

// SeedObservedDownloadManifest binds downstream observations to the accepted inventory.
func SeedObservedDownloadManifest(t *testing.T, db DownloadDB, root, task, release, key, frozen, policy, state, correlation, manifest string) {
	t.Helper()
	ctx := context.Background()
	var animeID, subscription, source string
	if err := db.QueryRowContext(ctx, `SELECT s.managed_anime_id,s.id,r.rss_source_id FROM releases r JOIN rss_sources f ON f.id=r.rss_source_id JOIN subscriptions s ON s.id=f.subscription_id WHERE r.id=$1`, release).Scan(&animeID, &subscription, &source); err != nil {
		t.Fatal(err)
	}
	// Circular legacy release pointers are arranged only after acceptance; the
	// release enters acceptance from its pre-accept verdict and keeps any verdict
	// that is legal for an attached task, otherwise it is the submitted one.
	var verdict string
	if err := db.QueryRowContext(ctx, `SELECT verdict FROM releases WHERE id=$1`, release).Scan(&verdict); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE releases SET download_task_id=NULL,verdict='accepted_pending_download' WHERE id=$1`, release); err != nil {
		t.Fatal(err)
	}
	SeedDownloadManifest(t, db, root, task, release, animeID, subscription, source, key, []byte("fixture:"+task), strings.Repeat("a", 40), policy, manifest)
	if _, err := db.ExecContext(ctx, `UPDATE releases SET verdict=CASE WHEN $2 IN ('accepted_pending_download','submitted','filtered','waiting_metadata','parse_failed') THEN $2 ELSE 'submitted' END WHERE id=$1`, release, verdict); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE download_tasks SET frozen_policy=$2::jsonb,state=$3,provider_correlation=NULLIF($4,'null')::jsonb WHERE id=$1`, task, policy, state, correlation); err != nil {
		t.Fatal(err)
	}
}

// fixedVarsOutput is a varsevidence.Evaluator that returns the fixture's
// expected output without running the time-limited program evaluator.
type fixedVarsOutput namingvars.Vars

func (f fixedVarsOutput) Evaluate(namingvars.Input, time.Duration) (namingvars.Vars, error) {
	return namingvars.Vars(f), nil
}
