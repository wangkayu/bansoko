package application

import (
	protocol "bansoko/internal/acquisition/terminal/domain"
	"bansoko/internal/testutil/disposabledb/guard"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5/pgconn"
	"testing"
)

// verifyObservedSatisfactionSQLConsumption is a consumer matrix over a real
// PathSatisfactionV1 observation: the SQL helper accepts the stored
// evidence and refuses a foreign size, a foreign path and the retired V1
// content-proof shape.
func verifyObservedSatisfactionSQLConsumption(t *testing.T, owner *sql.DB, plan string) {
	t.Helper()
	runtimeDB := guard.MustRuntime(t)
	defer runtimeDB.Close()
	_, denied := runtimeDB.Exec(`SELECT public.observed_satisfaction_valid('{}'::jsonb,'','','',1,'','','','',1,'')`)
	var pg *pgconn.PgError
	if !errors.As(denied, &pg) || pg.Code != "42501" {
		t.Fatalf("observed helper ACL: %v", denied)
	}
	var runtimeExecute, publicExecute bool
	if err := owner.QueryRow(`SELECT has_function_privilege('bansoko_runtime','public.observed_satisfaction_valid(jsonb,text,text,text,bigint,text,text,text,text,bigint,text)','EXECUTE'), EXISTS(SELECT 1 FROM pg_proc p CROSS JOIN LATERAL aclexplode(COALESCE(p.proacl,acldefault('f',p.proowner))) a WHERE p.oid='public.observed_satisfaction_valid(jsonb,text,text,text,bigint,text,text,text,text,bigint,text)'::regprocedure AND a.grantee=0 AND a.privilege_type='EXECUTE')`).Scan(&runtimeExecute, &publicExecute); err != nil {
		t.Fatal(err)
	}
	if runtimeExecute || publicExecute {
		t.Fatal("observed helper execution privilege leaked")
	}
	var raw []byte
	var root, artifact, ref, input, spec, path, hash string
	var version, size int64
	if err := owner.QueryRow(`SELECT i.plan_evidence,p.acquisition_id,p.artifact_id,p.artifact_version,i.file_ref,p.input_digest,i.spec_digest,i.canonical_path,f.size_bytes,f.fingerprint->>'value' FROM acquisition_files i JOIN postprocessing_plans p ON p.acquisition_id=i.acquisition_id JOIN artifact_files f ON f.artifact_id=p.artifact_id AND f.file_ref=i.file_ref WHERE p.id=$1 AND i.plan_kind IN ('adopt','retain')`, plan).Scan(&raw, &root, &artifact, &version, &ref, &input, &spec, &path, &size, &hash); err != nil {
		t.Fatal(err)
	}
	for _, variant := range []string{"stored", "observed_size", "expected_size", "foreign_path", "content_proof", "v1"} {
		var wrapper struct {
			Canonical string `json:"canonical"`
		}
		if err := json.Unmarshal(raw, &wrapper); err != nil {
			t.Fatal(err)
		}
		v, err := protocol.ParseStrict([]byte(wrapper.Canonical))
		if err != nil {
			t.Fatal(err)
		}
		o := v.Object["observed_object"]
		switch variant {
		case "observed_size":
			o.Object["size"] = protocol.Value{Kind: protocol.KindInteger, Int: size + 1}
		case "expected_size":
			v.Object["expected_size"] = protocol.Value{Kind: protocol.KindInteger, Int: size + 1}
		case "foreign_path":
			o.Object["location"].Object["canonical_path"] = protocol.Value{Kind: protocol.KindString, String: path + ".foreign"}
		case "content_proof":
			o.Object["content"] = protocol.Value{Kind: protocol.KindNull}
		case "v1":
			v.Object["schema_version"] = protocol.Value{Kind: protocol.KindInteger, Int: 1}
			v.Object["basis"] = protocol.Value{Kind: protocol.KindString, String: "stable_read"}
		}
		canonical := protocol.Canonical(v)
		evidence, err := json.Marshal(map[string]any{"canonical": string(canonical), "digest": protocol.Digest("acquisition-observed-satisfaction:v1", canonical), "observation": json.RawMessage(canonical), "final_object": json.RawMessage(protocol.Canonical(o))})
		if err != nil {
			t.Fatal(err)
		}
		var valid bool
		if err := owner.QueryRow(`SELECT public.observed_satisfaction_valid($1::jsonb,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, string(evidence), plan, root, artifact, version, ref, input, spec, path, size, hash).Scan(&valid); err != nil {
			t.Fatal(err)
		}
		if valid != (variant == "stored") {
			t.Fatalf("consumer variant=%s valid=%v", variant, valid)
		}
	}
}
