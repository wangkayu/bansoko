package application

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	protocol "bansoko/internal/acquisition/terminal/domain"
	filesystem "bansoko/internal/library/adapter/filesystem"
	library "bansoko/internal/library/domain"
)

// ReplacementCapture has no JSON or public field assembly surface. Only this
// transaction-free reader may construct the approved object/decision witness.
type ReplacementCapture struct {
	release, path, managed                  string
	decisionID, placementID, subscriptionID string
	snapshot                                []byte
	replacement                             protocol.Value
}

const replacementCaptureQuery = `SELECT DISTINCT jsonb_build_object('decision',to_jsonb(d),'placement',to_jsonb(p),'policy',jsonb_build_object('managed_anime_id',policy.managed_anime_id,'admission_mode',policy.admission_mode,'quality_profile',policy.quality_profile,'source_default_version',policy.replacement_source_default_version,'version',policy.replacement_version,'updated_at',policy.updated_at),'subscription',to_jsonb(s),'comparison',comparison.value) AS snapshot
 FROM public.replacement_decisions d JOIN public.subscriptions s ON s.managed_anime_id=d.managed_anime_id
 JOIN public.anime_business_configs policy ON policy.managed_anime_id=d.managed_anime_id
 CROSS JOIN LATERAL jsonb_array_elements(d.pools) pool(value)
 CROSS JOIN LATERAL jsonb_array_elements(COALESCE(pool.value->'comparisons','[]'::jsonb)) comparison(value)
 JOIN public.placements p ON p.id=comparison.value->'placement'->>'placement_id' AND p.managed_anime_id=d.managed_anime_id
 WHERE d.candidate_release_id=$1 AND p.physical_path=$2 AND d.verdict='replace' AND d.state='admitted'
 AND s.state='active' AND s.version=d.subscription_version AND policy.replacement_version=d.policy_version
 AND p.version=(comparison.value->'placement'->>'version')::bigint
 AND p.target_fingerprint=comparison.value->'placement'->>'fingerprint'
 AND p.state='active' AND p.verification_state='verified' AND p.replacement_eligible
 AND pool.value->'losers' @> jsonb_build_array(comparison.value->'placement')
 AND pool.value->>'verdict'='replace' AND comparison.value->>'verdict'='replace' ORDER BY snapshot`

// ErrReplacementApprovalUnavailable reports that no admitted quality
// replacement approves overwriting the managed file at the path. Under the
// overwrite setting the file is then replaced directly and its placement is
// displaced.
var ErrReplacementApprovalUnavailable = errors.New("replacement approved comparison unavailable")

// CaptureReplacement reads committed approval first, closes the DB read, then
// performs a path-level filesystem observation. It creates no media or permission.
func CaptureReplacement(ctx context.Context, db *sql.DB, release, path string) (*ReplacementCapture, error) {
	if db == nil || release == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errors.New("replacement capture input invalid")
	}
	rows, err := db.QueryContext(ctx, replacementCaptureQuery, release, path)
	if err != nil {
		return nil, err
	}
	snapshot, err := singleApproval(rows)
	rows.Close()
	if err != nil {
		return nil, err
	}
	if snapshot == nil {
		return nil, ErrReplacementApprovalUnavailable
	}
	var stored struct {
		Decision struct {
			ID            string `json:"id"`
			Managed       string `json:"managed_anime_id"`
			PolicyVersion int64  `json:"policy_version"`
			Created       string `json:"created_at"`
		}
		Subscription struct {
			ID string `json:"id"`
		}
		Placement struct {
			ID          string `json:"id"`
			Version     int64  `json:"version"`
			Fingerprint string `json:"target_fingerprint"`
		}
		Comparison replacementComparisonSnapshot
	}
	if err := json.Unmarshal(snapshot, &stored); err != nil {
		return nil, err
	}
	observation, err := (filesystem.OSFileObserver{}).ObservePath(ctx, path)
	if err != nil {
		return nil, err
	}
	// Path-level verification: the approved incumbent is the regular file at
	// the placement's path with the placement's recorded size. A placement
	// without a recorded size cannot be compared and fails closed.
	if size, ok := placementFingerprintSize(stored.Placement.Fingerprint); !ok || size != observation.SizeBytes {
		return nil, errors.New("replacement approved object changed")
	}
	object := replacementObject(path, observation)
	comparison := map[string]any{"policy": map[string]any{"ref": stored.Decision.Managed, "version": stored.Decision.PolicyVersion}, "candidate_quality": replacementQuality(stored.Comparison.Candidate), "existing_quality": replacementQuality(stored.Comparison.Existing), "compared_dimensions": stored.Comparison.Dimensions, "verdict": "replace", "evaluated_at": stored.Decision.Created}
	// PostgreSQL timestamps use an offset spelling; canonical protocol emits UTC.
	at, err := parseReplacementTime(stored.Decision.Created)
	if err != nil {
		return nil, err
	}
	comparison["evaluated_at"] = at
	value, err := valueOf(map[string]any{"placement": map[string]any{"ref": stored.Placement.ID, "version": stored.Placement.Version}, "expected_object": object, "comparison": comparison})
	if err != nil {
		return nil, err
	}
	if err := protocol.Validate("ReplacementV1", value); err != nil {
		return nil, err
	}
	return &ReplacementCapture{release: release, path: path, managed: stored.Decision.Managed, decisionID: stored.Decision.ID, placementID: stored.Placement.ID, subscriptionID: stored.Subscription.ID, snapshot: snapshot, replacement: value}, nil
}

func (capture *ReplacementCapture) recheck(ctx context.Context, tx *sql.Tx, release, managed string) (protocol.Value, error) {
	if capture == nil || capture.release != release || capture.managed != managed || tx == nil {
		return protocol.Value{}, errors.New("replacement capture missing or wrong release")
	}
	// Shared row locks keep all approval identities stable through acceptance.
	if _, err := tx.ExecContext(ctx, `SELECT d.id FROM public.replacement_decisions d JOIN public.subscriptions s ON s.managed_anime_id=d.managed_anime_id JOIN public.anime_business_configs policy ON policy.managed_anime_id=d.managed_anime_id JOIN public.placements p ON p.managed_anime_id=d.managed_anime_id WHERE d.candidate_release_id=$1 AND d.managed_anime_id=$2 AND p.physical_path=$3 AND d.id=$4 AND p.id=$5 AND s.id=$6 FOR SHARE OF d,s,policy,p`, release, managed, capture.path, capture.decisionID, capture.placementID, capture.subscriptionID); err != nil {
		return protocol.Value{}, err
	}
	rows, err := tx.QueryContext(ctx, replacementCaptureQuery, release, capture.path)
	if err != nil {
		return protocol.Value{}, err
	}
	defer rows.Close()
	current, err := singleApproval(rows)
	if err != nil {
		return protocol.Value{}, err
	}
	if current == nil {
		return protocol.Value{}, errors.New("replacement approval stale")
	}
	if !bytes.Equal(current, capture.snapshot) {
		return protocol.Value{}, errors.New("replacement approval changed")
	}
	return capture.replacement, nil
}

// singleApproval reads the approved comparisons of one target path and
// returns the first (the query orders them), or nil when there is none. Two
// comparisons of different placements, or of one placement at different
// versions or fingerprints, are ambiguous. The comparisons of one placement
// frozen by several key decisions (a multi-episode file the candidate
// replaces key by key) are one approval of one object.
func singleApproval(rows *sql.Rows) ([]byte, error) {
	var first []byte
	var placement string
	for rows.Next() {
		var snapshot []byte
		if err := rows.Scan(&snapshot); err != nil {
			return nil, err
		}
		var stored struct {
			Comparison struct {
				Placement json.RawMessage `json:"placement"`
			} `json:"comparison"`
		}
		if err := json.Unmarshal(snapshot, &stored); err != nil {
			return nil, err
		}
		if first == nil {
			first, placement = snapshot, string(stored.Comparison.Placement)
			continue
		}
		if string(stored.Comparison.Placement) != placement {
			return nil, errors.New("replacement approved comparison ambiguous")
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return first, nil
}

func replacementObject(path string, o library.PathObservation) map[string]any {
	identity := map[string]any{"kind": "local_fs", "device": strconv.FormatUint(o.Identity.Device, 10), "inode": strconv.FormatUint(o.Identity.Inode, 10)}
	namespace, _ := valueOf(map[string]any{"root": filepath.Dir(path), "adapter_version": "local-stable-read-v1"})
	location := map[string]any{"namespace_digest": protocol.Digest("local-filesystem-namespace:v1", protocol.Canonical(namespace)), "root": filepath.Dir(path), "relative_path": filepath.Base(path), "canonical_path": path}
	return map[string]any{"location": location, "file_type": "regular", "size": o.SizeBytes, "identity": identity, "observed_at": protocol.FormatTime(o.ObservedAt)}
}

type replacementFactsSnapshot struct {
	Group      string `json:"group"`
	Language   string `json:"language"`
	Resolution string `json:"resolution"`
	Source     string `json:"source"`
	Revision   string `json:"revision"`
	Size       *int64 `json:"size_bytes"`
}
type replacementComparisonSnapshot struct {
	Candidate  replacementFactsSnapshot `json:"candidate"`
	Existing   replacementFactsSnapshot `json:"existing"`
	Dimensions []string                 `json:"dimensions"`
}

func replacementQuality(f replacementFactsSnapshot) map[string]any {
	field := func(value any, known bool) any {
		if !known {
			return map[string]any{"state": "missing", "value": nil, "reason_code": nil, "raw_digest": nil}
		}
		return map[string]any{"state": "value", "value": value, "reason_code": nil, "raw_digest": nil}
	}
	q := map[string]any{"group": field(f.Group, f.Group != ""), "resolution": field(f.Resolution, f.Resolution != ""), "source": field(f.Source, f.Source != ""), "revision": field(f.Revision, f.Revision != ""), "languages": field([]string{f.Language}, f.Language != ""), "size": field(f.Size, f.Size != nil)}
	for _, key := range []string{"mapping_version", "parser_provenance", "profile", "rules_version"} {
		q[key] = frozenValue("absent", nil)
	}
	return q
}
func parseReplacementTime(raw string) (string, error) {
	at, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return "", err
	}
	return protocol.FormatTime(at), nil
}

// placementFingerprintSize extracts the recorded size from a placement target
// fingerprint. Placements admitted from a FileOperation store the operation's
// fingerprint document ({"algorithm","value","size_bytes"}) as text; a bare
// digest records no size.
func placementFingerprintSize(stored string) (int64, bool) {
	stored = strings.TrimSpace(stored)
	if !strings.HasPrefix(stored, "{") {
		return 0, false
	}
	var document struct {
		Size *int64 `json:"size_bytes"`
	}
	if json.Unmarshal([]byte(stored), &document) != nil || document.Size == nil || *document.Size < 0 {
		return 0, false
	}
	return *document.Size, true
}
