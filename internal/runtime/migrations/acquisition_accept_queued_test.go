package migrations

import (
	"bansoko/internal/testutil/nativeinput"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"bansoko/internal/runtime/database"
	"github.com/jackc/pgx/v5/pgconn"
)

// adoptSHA256 is the raw sha256 of the concatenated parts, as the migrations
// fixtures spell it.
func adoptSHA256(parts ...[]byte) string {
	h := sha256.New()
	for _, part := range parts {
		h.Write(part)
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// acqDomainDigest prefixes the bytes with a domain separator, matching the
// digest formulas the input and witness tables enforce as CHECKs.
func acqDomainDigest(domainSeparator string, payload []byte) string {
	return adoptSHA256([]byte(domainSeparator), []byte{0x00}, payload)
}

func adoptJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal parameter: %v", err)
	}
	return string(raw)
}

func adoptAcceptPGMessage(err error) string {
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		return pg.Message
	}
	return ""
}

// queryRower is satisfied by both *sql.DB and *sql.Tx.
type queryRower interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// These tests prove the queued branch of the acquisition accept: the owner-held
// accept entry writes the running root with its frozen input, payload digest,
// optional witness and early-sealed manifest columns, the unique rooted task
// over its locked source release and the two first work items in one
// transaction.

// seedAcceptableRelease seeds the owner chain up to an un-tasked release, the
// material the queued accept's source qualification locks.
func seedAcceptableRelease(t *testing.T, db *sql.DB, tag string) {
	t.Helper()
	seedQueuedPrograms(t, db)
	for _, statement := range []string{
		`INSERT INTO managed_anime(id, title) VALUES ('anime-` + tag + `', 'Show')`,
		`
INSERT INTO subscriptions(id, managed_anime_id, origin, instance_label, state)
VALUES ('sub-` + tag + `', 'anime-` + tag + `', 'custom', 'Show', 'active')`,
		`
INSERT INTO rss_sources(id, subscription_id, source_kind, locator, locator_display, group_name, title)
VALUES ('src-` + tag + `', 'sub-` + tag + `', 'custom_rss', 'https://feeds.example/` + tag + `', 'https://feeds.example/` + tag + `', 'G', 'Show')`,
		`INSERT INTO rss_source_runtime(rss_source_id) VALUES ('src-` + tag + `')`,
		`
INSERT INTO releases(id, rss_source_id, stable_item_key, raw_facts, source_facts, verdict, reason_code)
VALUES ('rel-` + tag + `', 'src-` + tag + `', 'key', '{}', '{}', 'accepted_pending_download', 'new')`,
	} {
		if _, err := db.ExecContext(context.Background(), statement); err != nil {
			t.Fatalf("seed release chain: %v", err)
		}
	}
}

type queuedParams struct {
	AcquisitionID, EntryKind, AccessScope, Namespace, RequestKey, RequestHash string
	CanonicalRequest, AdmissionDigest, ConfigIdentity, DisplayTitle           string
	OriginManagedRef                                                          any
	DetailSeconds, AfterAcceptSeconds, AfterTerminalSec                       int
	PreparationMode                                                           string
	PreparationDigest, PreparationWitness                                     any
	InputCanonical                                                            string
	Payload                                                                   []byte
	InfohashV1                                                                string
	InfohashV2                                                                any
	ManifestDigest, ManifestFiles                                             string
	SourceReleaseID                                                           string
	Qualification, BusinessSnapshot                                           string
	WitnessMaxBytes, WitnessMaxRows                                           int
	DefaultSections                                                           string
	TaskID, TaskIdempotency                                                   string
	QbConnectionID                                                            any
	TaskFrozenInput, TaskFrozenPolicy                                         string
	SubmitWorkID, SettleWorkID, SettleIdempotency                             string
	SubmitInputSnapshot                                                       string
	MaxAttempts                                                               int
}

func (p queuedParams) args() []any {
	return []any{
		p.AcquisitionID, p.EntryKind, p.AccessScope, p.Namespace, p.RequestKey, p.RequestHash,
		p.CanonicalRequest, p.AdmissionDigest, p.ConfigIdentity, p.DisplayTitle, p.OriginManagedRef,
		p.DetailSeconds, p.AfterAcceptSeconds, p.AfterTerminalSec,
		p.PreparationMode, p.PreparationDigest, p.PreparationWitness,
		p.InputCanonical, p.Payload, p.InfohashV1, p.InfohashV2,
		p.ManifestDigest, p.ManifestFiles,
		p.SourceReleaseID,
		p.TaskID, p.TaskIdempotency, p.QbConnectionID, p.TaskFrozenInput, p.TaskFrozenPolicy,
		p.SubmitWorkID, p.SettleWorkID, p.SettleIdempotency,
		p.SubmitInputSnapshot, p.MaxAttempts,
		p.Qualification, p.BusinessSnapshot, p.WitnessMaxBytes, p.WitnessMaxRows,
		"corr-" + p.AcquisitionID, "release", p.SourceReleaseID,
		defaultSections(p.DefaultSections),
	}
}

// queuedCallSQL renders the accept call with the vars-evaluation parameter,
// which tests leave disabled (no fabricated frozen evidence).
func queuedCallSQL() string {
	placeholders := make([]string, 43)
	for i := range placeholders {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
	}
	return "SELECT accept_acquisition_queued_with_vars(" + strings.Join(placeholders, ",") + ")"
}

// queuedCallArgs appends the disabled vars-evaluation argument to the params.
func queuedCallArgs(args []any) []any {
	return append(args, nil)
}

func callQueuedAccept(t *testing.T, db queryRower, p queuedParams) (map[string]any, error) {
	t.Helper()
	var raw []byte
	err := db.QueryRowContext(context.Background(), queuedCallSQL(), queuedCallArgs(p.args())...).Scan(&raw)
	if err != nil {
		return nil, err
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode the accept result: %v", err)
	}
	return decoded, nil
}

// validQueued builds a complete, valid queued accept over a two-file manifest
// against the release chain of the given tag.
func validQueued(t *testing.T, id, key, tag string) queuedParams {
	t.Helper()
	payload := []byte("the torrent bytes of " + id)
	business := queuedBusiness(t)
	digest, err := business.Digest()
	if err != nil {
		t.Fatal(err)
	}
	inputCanonical := nativeinput.Canonical(t, adoptSHA256(payload), digest)
	var input map[string]any
	if err := json.Unmarshal([]byte(inputCanonical), &input); err != nil {
		t.Fatal(err)
	}
	input["source"].(map[string]any)["kind"] = "subscription_direct"
	input["source"].(map[string]any)["manifest_digest"] = acqDigest("8")
	manifestFiles := []map[string]any{
		{"file_ref": "file-a", "ordinal": 0, "relative_path": "a/file-a.mkv", "exact_size": 1000, "media_kind": "video", "identity_evidence": map[string]any{"source": "test"}},
		{"file_ref": "file-b", "ordinal": 1, "relative_path": "a/file-b.ass", "exact_size": 100, "media_kind": "subtitle", "identity_evidence": map[string]any{"source": "test"}},
	}
	// The accept freezes the torrent totals of exactly this manifest.
	var torrentSize int64
	for _, f := range manifestFiles {
		torrentSize += int64(f["exact_size"].(int))
	}
	input["source"].(map[string]any)["torrent_size"] = map[string]any{"state": "known", "value": torrentSize, "reason_code": nil}
	input["source"].(map[string]any)["torrent_file_count"] = map[string]any{"state": "known", "value": len(manifestFiles), "reason_code": nil}
	inputCanonical = canonicalJSON(t, input)
	return queuedParams{
		AcquisitionID: id, EntryKind: "one_shot", AccessScope: "deployment_owner_v1",
		Namespace: "manual_acquire_v1", RequestKey: key, RequestHash: acqDigest("6"),
		CanonicalRequest: `{"group":"g"}`, AdmissionDigest: acqDomainDigest("acquisition-input:v1", []byte(inputCanonical)),
		ConfigIdentity:   adoptJSON(t, map[string]any{"kind": "one_shot", "intent_digest": acqDigest("7")}),
		DisplayTitle:     `{"state":"known","value":"t","reason_code":null}`,
		OriginManagedRef: "anime-" + tag,
		DetailSeconds:    thirtyDays, AfterAcceptSeconds: thirtyDays, AfterTerminalSec: 2 * thirtyDays,
		PreparationMode: "none",
		InputCanonical:  inputCanonical, Payload: payload,
		InfohashV1: strings.Repeat("a", 40), InfohashV2: nil,
		ManifestDigest: acqDigest("8"), ManifestFiles: adoptJSON(t, manifestFiles),
		SourceReleaseID: "rel-" + tag,
		Qualification:   submittedQualification(t, tag, business), BusinessSnapshot: adoptJSON(t, business),
		TaskID: "dt-" + id, TaskIdempotency: "dt-" + id,
		TaskFrozenInput: `{"trigger":"test"}`, TaskFrozenPolicy: `{"policy":"test"}`,
		SubmitWorkID: "wi-sub-" + id, SettleWorkID: "wi-set-" + id, SettleIdempotency: "wi-set-" + id,
		SubmitInputSnapshot: `{"trigger":"test"}`,
		MaxAttempts:         3,
	}
}

func TestAcquisitionQueuedAcceptWritesTheFirstGraph(t *testing.T) {
	db := installedDB(t)
	ctx := context.Background()
	seedAcceptableRelease(t, db, "one")

	result, err := callQueuedAccept(t, db, validQueued(t, "acq-q1", "key-q1", "one"))
	if err != nil {
		t.Fatalf("a valid queued accept must succeed: %v", err)
	}
	if result["status"] != "accepted" {
		t.Fatalf("status = %v, want accepted", result["status"])
	}
	receipt, ok := result["admission_receipt"].(map[string]any)
	if !ok {
		t.Fatalf("admission receipt missing: %v", result)
	}
	if receipt["download_task_id"] != "dt-acq-q1" || receipt["admission_kind"] != "download_queued" {
		t.Errorf("receipt must name the task and the queued admission: %v", receipt)
	}
	if receipt["accepted_at"] == nil || receipt["replay_guaranteed_until"] == nil {
		t.Errorf("receipt must carry the database times: %v", receipt)
	}
	if len(receipt) != 10 || receipt["requirements_digest"] != nil || receipt["target_digest"] != nil {
		t.Fatalf("queued receipt must match closed AcceptReceiptV1: %v", receipt)
	}

	// The root runs; the terminal fields stay empty until settlement.
	var state, branch, sourceRef, recordState string
	var admissionStatus int
	if err := db.QueryRowContext(ctx, `
SELECT state, execution_branch, admission_http_status, source_release_ref, record_state
FROM acquisitions WHERE id = 'acq-q1'`).
		Scan(&state, &branch, &admissionStatus, &sourceRef, &recordState); err != nil {
		t.Fatal(err)
	}
	if state != "running" || branch != "download" || admissionStatus != 202 || sourceRef != "rel-one" || recordState != "full" {
		t.Errorf("root head = (%s %s %d %s %s), want a running download root over rel-one",
			state, branch, admissionStatus, sourceRef, recordState)
	}

	// The unique task is rooted, and the release authority moved to it.
	var taskRoot, taskState string
	if err := db.QueryRowContext(ctx,
		`SELECT acquisition_id, state FROM download_tasks WHERE id = 'dt-acq-q1'`).
		Scan(&taskRoot, &taskState); err != nil {
		t.Fatal(err)
	}
	if taskRoot != "acq-q1" || taskState != "queued" {
		t.Errorf("task = (root %s, state %s), want (acq-q1, queued)", taskRoot, taskState)
	}
	var releaseTask, verdict string
	if err := db.QueryRowContext(ctx,
		`SELECT download_task_id, verdict FROM releases WHERE id = 'rel-one'`).
		Scan(&releaseTask, &verdict); err != nil {
		t.Fatal(err)
	}
	if releaseTask != "dt-acq-q1" || verdict != "accepted_pending_download" {
		t.Errorf("release = (task %s, verdict %s), want (dt-acq-q1, accepted_pending_download)", releaseTask, verdict)
	}

	// The manifest sealed early over two files.
	var manifestItems int
	if err := db.QueryRowContext(ctx,
		`SELECT manifest_item_count FROM acquisitions WHERE id = 'acq-q1'`).
		Scan(&manifestItems); err != nil {
		t.Fatal(err)
	}
	if manifestItems != 2 {
		t.Errorf("manifest item_count = %d, want 2", manifestItems)
	}

	// The two first work items: qb_submit on the task, settle on the root.
	var submitKind, settleKind string
	var submitIdem string
	if err := db.QueryRowContext(ctx, `
SELECT kind, idempotency_key FROM work_item_history
WHERE owner_type = 'download_task' AND owner_id = 'dt-acq-q1'`).Scan(&submitKind, &submitIdem); err != nil {
		t.Fatal(err)
	}
	if submitKind != "qb_submit" || submitIdem != "dt-acq-q1" {
		t.Errorf("submit work = (%s, %s), want (qb_submit, dt-acq-q1)", submitKind, submitIdem)
	}
	var settleRoot, settleSnapshot string
	if err := db.QueryRowContext(ctx, `
SELECT acquisition_id, input_snapshot::text FROM work_item_history
WHERE owner_type = 'acquisition' AND owner_id = 'acq-q1'`).Scan(&settleRoot, &settleSnapshot); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `
SELECT kind FROM work_item_history WHERE owner_type = 'acquisition' AND owner_id = 'acq-q1'`).Scan(&settleKind); err != nil {
		t.Fatal(err)
	}
	if settleKind != "acquisition_settle" || settleRoot != "acq-q1" || settleSnapshot != `{"schema_version": 1}` {
		t.Errorf("settle work = (%s, root %s, snapshot %s)", settleKind, settleRoot, settleSnapshot)
	}
}

func TestAcquisitionQueuedAcceptReplay(t *testing.T) {
	db := installedDB(t)
	seedAcceptableRelease(t, db, "replay")

	params := validQueued(t, "acq-replay", "key-replay", "replay")
	first, err := callQueuedAccept(t, db, params)
	if err != nil {
		t.Fatalf("first accept: %v", err)
	}
	again, err := callQueuedAccept(t, db, params)
	if err != nil {
		t.Fatalf("the same key and hash must replay: %v", err)
	}
	if again["status"] != "replayed" {
		t.Errorf("status = %v, want replayed", again["status"])
	}
	firstReceipt, _ := json.Marshal(first["admission_receipt"])
	againReceipt, _ := json.Marshal(again["admission_receipt"])
	if string(firstReceipt) != string(againReceipt) {
		t.Errorf("replayed receipt differs:\n%s\n%s", firstReceipt, againReceipt)
	}

	reused := params
	reused.RequestHash = acqDigest("f")
	if _, err := callQueuedAccept(t, db, reused); err == nil {
		t.Fatal("the same key with a different hash must be rejected")
	} else if code, message := sqlState(err), adoptAcceptPGMessage(err); code != "23514" || message != "IDEMPOTENCY_KEY_REUSED" {
		t.Errorf("hash conflict: SQLSTATE %q message %q, want 23514 IDEMPOTENCY_KEY_REUSED", code, message)
	}
}

func TestAcquisitionQueuedAcceptEligibility(t *testing.T) {
	db := installedDB(t)
	ctx := context.Background()
	seedAcceptableRelease(t, db, "elig")
	seedAcceptableRelease(t, db, "taken")
	seedAcceptableRelease(t, db, "retired")

	// The taken chain's release already belongs to an earlier accept.
	if _, err := callQueuedAccept(t, db, validQueued(t, "acq-taken", "key-taken", "taken")); err != nil {
		t.Fatalf("seed the taken accept: %v", err)
	}
	// Retire the retired chain's instance.
	if _, err := db.ExecContext(ctx, `
UPDATE managed_anime
SET lifecycle_state = 'retired', fence_epoch = fence_epoch + 1, lifecycle_version = lifecycle_version + 1, retired_at = CURRENT_TIMESTAMP, retired_subscription_version = 1
WHERE id = 'anime-retired'`); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name    string
		mutate  func(p *queuedParams)
		message string
	}{
		{"a missing source release", func(p *queuedParams) {
			p.SourceReleaseID = "rel-missing"
			mutateQualification(t, p, func(q map[string]any) { member(q, "source")["release_ref"] = "rel-missing" })
		}, "ACQ_QUALIFICATION_SOURCE_CHANGED"},
		{"a source owned by another instance", func(p *queuedParams) { p.OriginManagedRef = "anime-taken" }, "ACQ_QUALIFICATION_INPUT_MISMATCH"},
		{"a stale source version", func(p *queuedParams) {
			mutateQualification(t, p, func(q map[string]any) { member(q, "source")["expected_version"] = 99 })
		}, "ACQ_QUALIFICATION_SOURCE_CHANGED"},
		{"a source release already tasked", func(p *queuedParams) {
			p.SourceReleaseID = "rel-taken"
			p.OriginManagedRef = "anime-taken"
			mutateQualification(t, p, func(q map[string]any) {
				member(q, "management")["managed_anime_ref"] = "anime-taken"
				member(q, "management")["subscription_ref"] = "sub-taken"
				member(q, "source")["release_ref"] = "rel-taken"
				member(member(q, "source"), "source_ref")["ref"] = "src-taken"
			})
		}, "ACQ_QUALIFICATION_SOURCE_CHANGED"},
		{"a retired instance", func(p *queuedParams) {
			p.SourceReleaseID = "rel-retired"
			p.OriginManagedRef = "anime-retired"
			mutateQualification(t, p, func(q map[string]any) {
				member(q, "management")["managed_anime_ref"] = "anime-retired"
				member(q, "management")["subscription_ref"] = "sub-retired"
				member(q, "source")["release_ref"] = "rel-retired"
				member(member(q, "source"), "source_ref")["ref"] = "src-retired"
			})
		}, "ACQ_QUALIFICATION_MANAGEMENT_CHANGED"},
		{"a stale lifecycle version", func(p *queuedParams) {
			mutateQualification(t, p, func(q map[string]any) { member(q, "management")["expected_lifecycle_version"] = 7 })
		}, "ACQ_QUALIFICATION_MANAGEMENT_CHANGED"},
		{"payload bytes that disagree with the frozen input", func(p *queuedParams) { p.Payload = []byte("other payload") }, "ACQ_ACCEPT_PAYLOAD_DIGEST"},
		{"an empty manifest", func(p *queuedParams) { p.ManifestFiles = `[]` }, "ACQ_ACCEPT_EMPTY_MANIFEST"},
		{"a preparation digest that does not match the witness", func(p *queuedParams) {
			p.PreparationDigest = acqDigest("9")
		}, "ACQ_ACCEPT_PREPARATION_MISMATCH"},
		{"a retention window outside the approved range", func(p *queuedParams) { p.DetailSeconds = 7 }, "ACQ_ACCEPT_BAD_RETENTION"},
	} {
		params := validQueued(t, "acq-bad-"+tc.name, "key-bad-"+tc.name, "elig")
		if tc.name == "a preparation digest that does not match the witness" {
			params = qualifiedCatalog(t, db, "acq-bad-digest", "key-bad-digest", "digest-elig")
			if len(params.PreparationWitness.(string)) > params.WitnessMaxBytes || params.WitnessMaxRows <= 0 {
				t.Fatal("digest negative must have legal witness capacity")
			}
		}
		tc.mutate(&params)
		if _, err := callQueuedAccept(t, db, params); err == nil {
			t.Errorf("%s must be rejected", tc.name)
		} else if code, message := sqlState(err), adoptAcceptPGMessage(err); code != "23514" || message != tc.message {
			t.Errorf("%s: SQLSTATE %q message %q, want 23514 %s", tc.name, code, message, tc.message)
		}
	}

	// Nothing half-accepted is left behind.
	var roots, tasks int
	if err := db.QueryRowContext(ctx, `
SELECT (SELECT count(*) FROM acquisitions WHERE id LIKE 'acq-bad-%'),
       (SELECT count(*) FROM download_tasks WHERE id LIKE 'dt-acq-bad-%')`).Scan(&roots, &tasks); err != nil {
		t.Fatal(err)
	}
	if roots != 0 || tasks != 0 {
		t.Errorf("rejected accepts left %d roots and %d tasks, want none", roots, tasks)
	}
}

// The runtime role's only capability on the queued accept is EXECUTE: it runs
// the whole entry, and it still cannot write the graph directly.
func TestAcquisitionQueuedAcceptPrivileges(t *testing.T) {
	owner := installedDB(t)
	ctx := context.Background()

	var runtimeGranted, publicGranted bool
	if err := owner.QueryRowContext(ctx, `
SELECT has_function_privilege('bansoko_runtime', p.oid, 'EXECUTE'),
       has_function_privilege('public', p.oid, 'EXECUTE')
FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
WHERE n.nspname = 'public' AND p.proname = 'accept_acquisition_queued_with_vars'`).
		Scan(&runtimeGranted, &publicGranted); err != nil {
		t.Fatal(err)
	}
	if !runtimeGranted || publicGranted {
		t.Fatalf("EXECUTE on the queued entry: runtime %t public %t, want true false", runtimeGranted, publicGranted)
	}

	seedAcceptableRelease(t, owner, "priv")
	password := randomTestPassword(t)
	if err := database.ProvisionRuntimeRole(ctx, owner, database.RuntimeRoleConfig{Role: runtimeRoleTestName, Password: password}); err != nil {
		t.Fatalf("provision runtime role: %v", err)
	}
	runtime := openRuntimeConnection(t, password)
	defer runtime.Close()

	result, err := callQueuedAccept(t, runtime, validQueued(t, "acq-priv-q", "key-priv-q", "priv"))
	if err != nil {
		t.Fatalf("the runtime role must run the whole queued accept: %v", err)
	}
	if result["status"] != "accepted" {
		t.Fatalf("status = %v, want accepted", result["status"])
	}

	// A bad graph through the runtime fails as an invariant violation, not as
	// a privilege error and not as a silent write.
	bad := validQueued(t, "acq-priv-bad", "key-priv-bad", "priv")
	bad.SourceReleaseID = "rel-missing"
	mutateQualification(t, &bad, func(q map[string]any) { member(q, "source")["release_ref"] = "rel-missing" })
	if _, err := callQueuedAccept(t, runtime, bad); err == nil {
		t.Fatal("a missing source release through the runtime must be rejected")
	} else if code, message := sqlState(err), adoptAcceptPGMessage(err); code != "23514" || message != "ACQ_QUALIFICATION_SOURCE_CHANGED" {
		t.Errorf("runtime bad accept: SQLSTATE %q message %q, want 23514 ACQ_QUALIFICATION_SOURCE_CHANGED", code, message)
	}
}

func TestAcquisitionQueuedReplaySkipsCurrentEligibility(t *testing.T) {
	db := installedDB(t)
	ctx := context.Background()
	seedAcceptableRelease(t, db, "retire-after")
	params := validQueued(t, "acq-before-retire", "key-before-retire", "retire-after")
	if _, err := callQueuedAccept(t, db, params); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE managed_anime SET lifecycle_state = 'retired', fence_epoch = fence_epoch + 1, lifecycle_version = lifecycle_version + 1, retired_at = CURRENT_TIMESTAMP, retired_subscription_version = 1 WHERE id = 'anime-retire-after'`); err != nil {
		t.Fatal(err)
	}
	result, err := callQueuedAccept(t, db, params)
	if err != nil || result["status"] != "replayed" {
		t.Fatalf("accepted anchor must replay after retirement: %v %v", result, err)
	}
	// Later settlement changes the current terminal receipt, not the original
	// admission receipt. Replaying must return both stored receipts verbatim.
	terminal := `{"schema_version":1,"outcome":"failed"}`
	if _, err := db.ExecContext(ctx, `
UPDATE acquisitions
SET state = 'failed', terminal_at = accepted_at, terminal_schema = 'native_terminal_v1',
    terminal_receipt = $2::jsonb,
    receipt_until = accepted_at + receipt_after_terminal_seconds * INTERVAL '1 second',
    detail_until = accepted_at + detail_retention_seconds * INTERVAL '1 second'
WHERE id = $1`, params.AcquisitionID, terminal); err != nil {
		t.Fatal(err)
	}
	result, err = callQueuedAccept(t, db, params)
	if err != nil || result["status"] != "replayed" {
		t.Fatalf("terminal acquisition must replay: %v %v", result, err)
	}
	if got, ok := result["terminal_receipt"].(map[string]any); !ok || got["outcome"] != "failed" {
		t.Fatalf("replay dropped the stored terminal receipt: %v", result)
	}
	args := params.args()
	args[5] = nil
	var raw []byte
	if err := db.QueryRowContext(ctx, queuedCallSQL(), queuedCallArgs(args)...).Scan(&raw); err == nil || adoptAcceptPGMessage(err) != "IDEMPOTENCY_KEY_REUSED" {
		t.Fatalf("NULL request hash must not fall through SQL three-valued logic: %v", err)
	}
	params.RequestHash = "" // A different hash must never replay the accepted anchor.
	if _, err := callQueuedAccept(t, db, params); err == nil || adoptAcceptPGMessage(err) != "IDEMPOTENCY_KEY_REUSED" {
		t.Fatalf("different hash must not replay: %v", err)
	}
}

func TestAcquisitionQueuedWitnessAndWholeTransactionRollback(t *testing.T) {
	db := installedDB(t)
	ctx := context.Background()
	seedAcceptableRelease(t, db, "rollback")
	params := qualifiedCatalog(t, db, "acq-witness", "key-witness", "witness")
	witness := params.PreparationWitness.(string)
	if _, err := callQueuedAccept(t, db, params); err != nil {
		t.Fatal(err)
	}
	var stored string
	if err := db.QueryRowContext(ctx, `SELECT preparation_witness_canonical_utf8 FROM acquisitions WHERE id = $1`, params.AcquisitionID).Scan(&stored); err != nil || stored != witness {
		t.Fatalf("frozen witness: %q %v", stored, err)
	}

	// Fail at the very last write (settle item uniqueness), after the release
	// update and all other child writes. The function must roll back everything.
	bad := validQueued(t, "acq-rollback", "key-rollback", "rollback")
	bad.SettleWorkID = params.SettleWorkID
	if _, err := callQueuedAccept(t, db, bad); err == nil || sqlState(err) != "23505" {
		t.Fatalf("late uniqueness error: %v, want 23505", err)
	}
	var children int
	if err := db.QueryRowContext(ctx, `
SELECT (SELECT count(*) FROM acquisitions WHERE id = $1)
     + (SELECT count(*) FROM acquisition_files WHERE acquisition_id = $1)
     + (SELECT count(*) FROM download_tasks WHERE acquisition_id = $1)
     + (SELECT count(*) FROM work_item_history WHERE owner_id IN ($1, $2))`, bad.AcquisitionID, bad.TaskID).Scan(&children); err != nil || children != 0 {
		t.Fatalf("late failure left a partial graph: count=%d err=%v", children, err)
	}
	var releaseTask sql.NullString
	var verdict string
	if err := db.QueryRowContext(ctx, `SELECT download_task_id, verdict FROM releases WHERE id = $1`, bad.SourceReleaseID).Scan(&releaseTask, &verdict); err != nil || releaseTask.Valid || verdict != "accepted_pending_download" {
		t.Fatalf("release update escaped rollback: task=%v verdict=%q err=%v", releaseTask, verdict, err)
	}
}

func TestAcquisitionQueuedConcurrentReplayCreatesOneGraph(t *testing.T) {
	db := installedDB(t)
	ctx := context.Background()
	seedAcceptableRelease(t, db, "concurrent")
	params := validQueued(t, "acq-concurrent", "key-concurrent", "concurrent")
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := callQueuedAccept(t, tx, params); err != nil {
		t.Fatal(err)
	}

	// Hold the first acceptance uncommitted while a second connection calls
	// the same key. Observe the actual advisory-lock wait rather than sleeping
	// and assuming the competing transaction has reached the key lock.
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var pid int
	if err := conn.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	type replayResult struct {
		raw []byte
		err error
	}
	done := make(chan replayResult, 1)
	go func() {
		var raw []byte
		call, args := queuedCallSQL(), queuedCallArgs(params.args())
		err := conn.QueryRowContext(callCtx, call, args...).Scan(&raw)
		done <- replayResult{raw: raw, err: err}
	}()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiting bool
		if err := db.QueryRowContext(callCtx, `SELECT EXISTS (SELECT 1 FROM pg_locks WHERE pid = $1 AND locktype = 'advisory' AND NOT granted)`, pid).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case result := <-done:
			t.Fatalf("second call returned before the first committed: %s %v", result.raw, result.err)
		case <-callCtx.Done():
			t.Fatal("second acceptance never waited on the accept-key lock")
		case <-ticker.C:
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	result := <-done
	if result.err != nil {
		t.Fatal(result.err)
	}
	var response map[string]any
	if err := json.Unmarshal(result.raw, &response); err != nil || response["status"] != "replayed" {
		t.Fatalf("concurrent acceptance did not replay: %s %v", result.raw, err)
	}
	var roots, tasks, works int
	if err := db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM acquisitions), (SELECT count(*) FROM download_tasks), (SELECT count(*) FROM work_item_history)`).Scan(&roots, &tasks, &works); err != nil {
		t.Fatal(err)
	}
	if roots != 1 || tasks != 1 || works != 2 {
		t.Fatalf("concurrent accept duplicated the graph: roots=%d tasks=%d work=%d", roots, tasks, works)
	}
}
