package application

import (
	configuration "bansoko/internal/configuration/application"
	configdomain "bansoko/internal/configuration/domain"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	generated "bansoko/internal/acquisition/terminal/adapter/persistence/generated"
	protocol "bansoko/internal/acquisition/terminal/domain"
	catalog "bansoko/internal/catalog/domain"
	"bansoko/internal/testutil/nativeinput"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/sqlc-dev/pqtype"
)

// Existing A1 fixture exercises the exact A1/A4 production builders followed by
// the generated 133 call. No exported JSON material, service or caller switch.
func checkQualifiedQueuedPostgres(t *testing.T, ctx context.Context, db *sql.DB, c admissionCapture) {
	t.Helper()
	s := func(v string) sql.NullString { return sql.NullString{String: v, Valid: true} }
	j := func(v string) pqtype.NullRawMessage {
		return pqtype.NullRawMessage{RawMessage: json.RawMessage(v), Valid: true}
	}
	n := func(v int32) sql.NullInt32 { return sql.NullInt32{Int32: v, Valid: true} }
	payload := []byte("qualified torrent bytes")
	if catalog.DigestBytes(payload) != c.payloadDigest {
		t.Fatal("fixture payload drift")
	}
	// An owner capture resolves the live sections (parser-section-v1), which only the
	// single-offset input schema 6 carries; the other config kinds keep the
	// historical schema 3 golden input.
	inputSchema := "AcquisitionInputV1"
	input := nativeinput.Canonical(t, c.payloadDigest, c.configDigest)
	inputValue, err := protocol.ParseStrict([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	inputValue.Object["source"].Object["kind"] = protocol.Value{Kind: protocol.KindString, String: "automatic_source"}
	if c.configKind == "owner" {
		resolved := captureSections(c.material)
		for i, section := range inputValue.Object["config"].Object["sections"].Array {
			key := section.Object["section_key"].String
			value, err := protocol.ParseStrict(resolved.Sections[key])
			if err != nil {
				t.Fatal(err)
			}
			if key == "naming_rules" {
				value, err = projectNamingSection(resolved.Sections[key])
				if err != nil {
					t.Fatal(err)
				}
			}
			if key == "subgroups" {
				value, err = protocol.ParseStrict([]byte(c.policyCanonical))
				if err != nil {
					t.Fatal(err)
				}
			}
			section.Object["value"] = value
			section.Object["config_version"] = protocol.Value{Kind: protocol.KindInteger, Int: resolved.Versions[key]}
			section.Object["schema_version"] = protocol.Value{Kind: protocol.KindString, String: resolved.SchemaVersions[key]}
			inputValue.Object["config"].Object["sections"].Array[i] = section
		}
		fact, err := valueOf(frozenValue("known", c.material.rulesOwnerVersion))
		if err != nil {
			t.Fatal(err)
		}
		inputValue.Object["config"].Object["rules_version"] = fact
		selection := inputValue.Object["config"].Object["parser"].Object["selection"].Object["value"]
		body := map[string]any{"selection": json.RawMessage(protocol.Canonical(selection))}
		for _, key := range []string{"source_version", "policy_version", "profile_version", "input_digest", "result_digest", "media_kind", "season", "season_bound", "parse_status", "parse_shape"} {
			body[key] = frozenValue("absent", nil)
		}
		body["rules_owner_version"] = json.RawMessage(protocol.Canonical(fact))
		provenance, err := valueOf(frozenValue("known", body))
		if err != nil {
			t.Fatal(err)
		}
		inputValue.Object["config"].Object["parser"].Object["provenance"] = provenance
		// Schema 6 freezes the torrent totals of exactly the accepted manifest
		// (one 1000-byte file below).
		for key, value := range map[string]any{"torrent_size": int64(1000), "torrent_file_count": int64(1)} {
			total, err := valueOf(frozenValue("known", value))
			if err != nil {
				t.Fatal(err)
			}
			inputValue.Object["source"].Object[key] = total
		}
	}
	if err := protocol.Validate(inputSchema, inputValue); err != nil {
		t.Fatal(err)
	}
	inputValue.Object["source"].Object["torrent_size"], err = valueOf(frozenValue("known", int64(1000)))
	if err != nil {
		t.Fatal(err)
	}
	inputValue.Object["source"].Object["torrent_file_count"], err = valueOf(frozenValue("known", int64(1)))
	if err != nil {
		t.Fatal(err)
	}
	input = string(protocol.Canonical(inputValue))
	inputDigest := protocol.Digest("acquisition-input:v1", []byte(input))
	witness, err := constructWitness(c, inputDigest, testWitnessLimits())
	if err != nil {
		t.Fatal(err)
	}
	q, err := protocol.ParseStrict(c.qualification.Canonical())
	if err != nil {
		t.Fatal(err)
	}
	directory, err := defaultSectionDirectory()
	if err != nil {
		t.Fatal(err)
	}
	args := generated.AcceptQualifiedQueuedParams{
		DefaultSections: j(string(directory)),
		CorrelationID:   s("corr-a3"), CausationType: s("release"), CausationID: s(c.material.release.id),
		AcquisitionID: s("acq-a3-real"), EntryKind: s("one_shot"), AccessScope: s("deployment_owner_v1"), RequestNamespace: s("manual_acquire_v1"), RequestKey: s("a3-key"), RequestHash: s(c.requestHash), CanonicalRequest: j(`{"fixture":"a3"}`), AdmissionDigest: s(inputDigest), ConfigIdentity: j(`{"kind":"one_shot","intent_digest":"` + c.requestHash + `"}`), DisplayTitle: j(`{"state":"known","value":"Show","reason_code":null}`), OriginManagedAnimeRef: s(q.Object["management"].Object["managed_anime_ref"].String),
		DetailRetentionSeconds: n(2592000), ReceiptAfterAcceptSeconds: n(2592000), ReceiptAfterTerminalSeconds: n(2592000), PreparationMode: s("provider_selection_v1"), PreparationDigest: s(witness.digest), PreparationWitness: s(witness.canonical), InputCanonicalUtf8: s(input), Payload: payload, ValidatedInfohashV1: s(strings.Repeat("a", 40)), ManifestDigest: s(catalog.DigestBytes([]byte("manifest"))), ManifestFiles: j(`[{"file_ref":"file-a","ordinal":0,"relative_path":"ep01.mkv","exact_size":1000,"media_kind":"video","identity_evidence":{}}]`),
		SourceReleaseID: s(q.Object["source"].Object["release_ref"].String), DownloadTaskID: s("dt-a3-real"), TaskIdempotencyKey: s("dt-a3-real"), TaskFrozenInput: j(`{"trigger":"test"}`), TaskFrozenPolicy: j(`{"policy":"test"}`), SubmitWorkItemID: s("wi-a3-submit"), SettleWorkItemID: s("wi-a3-settle"), SettleIdempotencyKey: s("wi-a3-settle"), SubmitInputSnapshot: j(`{"trigger":"test"}`), MaxAttempts: n(3), Qualification: j(string(c.qualification.Canonical())), BusinessSnapshot: j(c.businessJSON), WitnessMaxBytes: n(200000), WitnessMaxRows: n(50),
	}
	if c.configKind == "owner" {
		t.Run("accept_then_put_global_waits", func(t *testing.T) {
			ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if _, err := tx.ExecContext(ctx, `UPDATE releases SET verdict='accepted_pending_download' WHERE id=$1`, c.material.release.id); err != nil {
				t.Fatal(err)
			}
			if _, err := generated.New(tx).AcceptQualifiedQueued(ctx, args); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				_, err := configuration.NewService(db, nil).PutGlobalSection(ctx, "file_effect", 0, configdomain.GlobalSectionInput{SchemaVersion: captureSections(c.material).SchemaVersions["file_effect"], Value: map[string]any{"operation": "move", "collision_policy": "fail"}})
				done <- err
			}()
			select {
			case err := <-done:
				t.Fatalf("PutSection failed to wait: %v", err)
			case <-time.After(150 * time.Millisecond):
			}
			if err := tx.Rollback(); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal("PutSection did not converge")
			}
			if _, err := db.ExecContext(ctx, `DELETE FROM global_config_sections WHERE section_name='file_effect'`); err != nil {
				t.Fatal(err)
			}
		})
		for _, mutation := range []string{"missing", "extra", "illegal", "illegal_subgroups", "illegal_subgroup_entry", "illegal_filter_regex", "nonobject", "naming_nonobject", "parser_retired_offset_key", "keyword_score_string", "provider_score_string", "entry_bool_string", "same_default"} {
			t.Run("default_directory_"+mutation, func(t *testing.T) {
				tx, err := db.BeginTx(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback()
				var defaults map[string]json.RawMessage
				if err := json.Unmarshal(directory, &defaults); err != nil {
					t.Fatal(err)
				}
				params := args
				switch mutation {
				case "missing":
					delete(defaults, "parser")
				case "extra":
					defaults["unknown"] = json.RawMessage(`{}`)
				case "illegal":
					defaults["file_effect"] = json.RawMessage(`{"operation":"invalid","collision_policy":"fail"}`)
				case "illegal_subgroups":
					defaults["subgroups"] = json.RawMessage(`{"max_rss":-1,"entries":[],"name_keywords":null,"provider_defaults":null}`)
				case "illegal_filter_regex":
					defaults["release_rules"] = json.RawMessage(`{"must_contain":"[","not_in":""}`)
				case "keyword_score_string":
					defaults["subgroups"] = json.RawMessage(`{"max_rss":3,"entries":[],"name_keywords":{"anibt":[{"keyword":"a","score":"1"}]},"provider_defaults":null}`)
				case "provider_score_string":
					defaults["subgroups"] = json.RawMessage(`{"max_rss":3,"entries":[],"name_keywords":null,"provider_defaults":{"anibt":{"mode":"whitelist","score":"50","groups":[]}}}`)
				case "entry_bool_string":
					defaults["subgroups"] = json.RawMessage(`{"max_rss":3,"entries":[{"provider":"anibt","group_ref":"a","display_name":"A","selection_mode":"auto","score":50,"effective_selected":"true"}],"name_keywords":null,"provider_defaults":null}`)
				case "parser_retired_offset_key":
					defaults["parser"] = json.RawMessage(`{"episode_patterns":[],"default_offset":0}`)
				case "naming_nonobject":
					defaults["naming_rules"] = json.RawMessage(`{"language":"js-naming-1","movie":[],"unknown":{},"tv":{},"preset":null,"subtitle_suffix_template":""}`)
				case "illegal_subgroup_entry":
					defaults["subgroups"] = json.RawMessage(`{"max_rss":3,"entries":[{"provider":"invalid","group_ref":"a","display_name":"A","selection_mode":"auto","score":50}],"name_keywords":null,"provider_defaults":null}`)
				case "same_default":
					if _, err := tx.ExecContext(ctx, `INSERT INTO global_config_sections(section_name,schema_version,value,version) VALUES('file_effect',$1,$2::jsonb,1)`, captureSections(c.material).SchemaVersions["file_effect"], string(defaults["file_effect"])); err != nil {
						t.Fatal(err)
					}
				}
				raw, err := json.Marshal(defaults)
				if err != nil {
					t.Fatal(err)
				}
				if mutation == "nonobject" {
					raw = []byte(`[]`)
				}
				params.DefaultSections = j(string(raw))
				if _, err := tx.ExecContext(ctx, `UPDATE releases SET verdict='accepted_pending_download' WHERE id=$1`, c.material.release.id); err != nil {
					t.Fatal(err)
				}
				if _, err := tx.ExecContext(ctx, `SAVEPOINT default_case`); err != nil {
					t.Fatal(err)
				}
				_, err = generated.New(tx).AcceptQualifiedQueued(ctx, params)
				if mutation == "same_default" {
					if err != nil {
						t.Fatalf("equal default Global rejected: %v", err)
					}
					return
				}
				var pg *pgconn.PgError
				want := "ACQ_QUALIFICATION_INVALID"
				if mutation == "illegal_filter_regex" {
					want = "ACQ_QUALIFICATION_CONFIG_CHANGED"
				}
				if !errors.As(err, &pg) || pg.Message != want {
					t.Fatalf("directory %s: %v", mutation, err)
				}
				if _, err := tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT default_case`); err != nil {
					t.Fatal(err)
				}
				var roots int
				if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM acquisitions`).Scan(&roots); err != nil || roots != 0 {
					t.Fatalf("invalid directory roots=%d err=%v", roots, err)
				}
			})
		}
		t.Run("capture_then_first_global_save", func(t *testing.T) {
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			var defaults map[string]json.RawMessage
			if err := json.Unmarshal(directory, &defaults); err != nil {
				t.Fatal(err)
			}
			key := "file_effect"
			if _, ok := defaults[key]; !ok {
				t.Fatal("fixture missing default file_effect")
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO global_config_sections(section_name,schema_version,value,version) VALUES($1,$2,$3::jsonb,1)`, key, captureSections(c.material).SchemaVersions[key], `{"collision_policy":"fail","operation":"move"}`); err != nil {
				t.Fatal(err)
			}
			if _, err := tx.ExecContext(ctx, `UPDATE releases SET verdict='accepted_pending_download' WHERE id=$1`, c.material.release.id); err != nil {
				t.Fatal(err)
			}
			_, err = generated.New(tx).AcceptQualifiedQueued(ctx, args)
			var pg *pgconn.PgError
			if !errors.As(err, &pg) || pg.Message != "ACQ_QUALIFICATION_CONFIG_CHANGED" {
				t.Fatalf("first Global save must become INPUT_STALE: %v", err)
			}
		})
	}
	for _, bad := range []bool{true, false} {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if _, err := tx.ExecContext(ctx, `UPDATE releases SET verdict='accepted_pending_download' WHERE id=$1`, c.material.release.id); err != nil {
			t.Fatal(err)
		}
		params := args
		if bad {
			var w map[string]any
			if err := json.Unmarshal([]byte(witness.canonical), &w); err != nil {
				t.Fatal(err)
			}
			w["selected_refs"] = []string{}
			canonical := mustCanonical(w)
			params.PreparationWitness = s(string(canonical))
			params.PreparationDigest = s(protocol.Digest(witnessDigestDomain, canonical))
		}
		raw, err := generated.New(tx).AcceptQualifiedQueued(ctx, params)
		if bad {
			var pg *pgconn.PgError
			if !errors.As(err, &pg) || pg.Message != "ACQ_ACCEPT_PREPARATION_MISMATCH" {
				t.Fatalf("SQL accepted tampered native witness: %v", err)
			}
		} else {
			if err != nil {
				t.Fatal(err)
			}
			var result map[string]any
			if err := json.Unmarshal(raw, &result); err != nil || result["status"] != "accepted" {
				t.Fatalf("real builder acceptance: %s %v", raw, err)
			}
			var count int
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM acquisitions WHERE id='acq-a3-real'`).Scan(&count); err != nil || count != 1 {
				t.Fatal("missing graph")
			}
		}
		if err := tx.Rollback(); err != nil {
			t.Fatal(err)
		}
	}
}
