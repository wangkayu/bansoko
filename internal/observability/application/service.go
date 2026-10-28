// Package application implements the read-only observability projection.
// It unions routine activities, subscription log entries, automation dispatch
// runs, and plugin deliveries into a single time-ordered record stream and
// queries diagnostic episodes separately. No writes, no owner mutations.
package application

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"bansoko/internal/apperrors"
	adapterconfig "bansoko/internal/plugintarget/config"
	deliverypayload "bansoko/internal/plugintarget/payload"
)

// Service is the observability read service.
type Service struct {
	db *sql.DB
}

// NewService returns an observability read service bound to the shared pool.
func NewService(db *sql.DB) (*Service, error) {
	if db == nil {
		return nil, errors.New("observability service database is nil")
	}
	return &Service{db: db}, nil
}

// --- Records ---

const defaultRecordLimit = 20
const maxRecordLimit = 100

// validRecordKinds is the closed set of record kinds the union projection
// knows how to query. It mirrors the openapi enum for the kind parameter.
var validRecordKinds = map[string]bool{
	"routine":    true,
	"business":   true,
	"automation": true,
	"plugin":     true,
}

// recordSourceRanks give every union source a stable total-order rank. The
// keyset cursor is (record_time, source_rank, id): record_time alone can tie
// across sources and ids are only unique per table, so the rank keeps the
// union order strict and replayable. Ranks never change once assigned.
//
// This map is the single source of truth: every union query projects its rank
// from it and the cursor validator accepts exactly its values, so a source's
// SQL order, cursor filter, and cursor codec cannot drift apart.
var recordSourceRanks = map[string]int{
	"routine":    1,
	"business":   2,
	"automation": 3,
	"plugin":     4,
}

// diagnosticCursorRank is the constant rank of the single-table diagnostics
// stream. Diagnostics share the cursor codec with the union so both surfaces
// accept and produce one shape; any other rank is a foreign cursor.
const diagnosticCursorRank = 0

// validRecordSourceRank reports whether rank was issued by the union encoder.
func validRecordSourceRank(rank int) bool {
	for _, candidate := range recordSourceRanks {
		if candidate == rank {
			return true
		}
	}
	return false
}

// ListRecords returns the union of routine, business, automation, and plugin
// records ordered by record_time DESC with keyset cursor pagination. The state
// and search filters select whole Release runs: a business entry of a run is
// in the state of its run and matches a search when any entry of its run
// does.
func (s *Service) ListRecords(ctx context.Context, params RecordQuery) (RecordPage, error) {
	limit := defaultRecordLimit
	if params.Limit != nil {
		limit = int(*params.Limit)
	}
	if limit < 1 {
		limit = 1
	}
	if limit > maxRecordLimit {
		limit = maxRecordLimit
	}

	var cursorTime time.Time
	var cursorRank int
	var cursorID string
	if params.Cursor != nil && *params.Cursor != "" {
		t, rank, id, err := decodeCursor(string(*params.Cursor))
		if err != nil || !validRecordSourceRank(rank) {
			return RecordPage{}, invalidCursorError()
		}
		cursorTime = t
		cursorRank = rank
		cursorID = id
	}

	// Build the UNION ALL query. Each source projects the page key
	// (record_time, id, kind, kind_rank); row_data is built for the page rows.
	//
	// row_data is an explicit per-contract jsonb_build_object, never
	// to_jsonb(table.*): the record union is marshalled verbatim to the client,
	// so a storage-only column (plugin_deliveries.secret_ref is the one that
	// leaked) or any column added later would otherwise leave the API without
	// anyone touching this file. The field lists below are the read allowlist.
	var args []interface{}
	argIdx := 1

	// kind filter: only include the requested sources. The parameter is
	// repeatable, so callers narrow the union to an explicit kind set. Each
	// value must be one of the known record kinds; an unknown value (for
	// example a comma-joined "business,plugin") is rejected as a bad request
	// rather than silently narrowing the union to nothing.
	kindFilter := map[string]struct{}{}
	if params.Kind != nil {
		for _, kind := range *params.Kind {
			k := string(kind)
			if !validRecordKinds[k] {
				return RecordPage{}, &apperrors.BadRequest{
					Code:    "INVALID_KIND",
					Message: fmt.Sprintf("Unknown observability record kind %q.", k),
				}
			}
			kindFilter[k] = struct{}{}
		}
	}

	stateFilter, err := recordStateParam(params.State)
	if err != nil {
		return RecordPage{}, err
	}
	searchPattern, err := recordSearchParam(params.Q)
	if err != nil {
		return RecordPage{}, err
	}
	entryKinds, err := recordEntryKindsParam(params.EntryKind)
	if err != nil {
		return RecordPage{}, err
	}
	businessKinds := classifyBusinessKinds(entryKinds)

	// time range
	var fromTime, toTime *time.Time
	if params.From != nil {
		t := time.Time(*params.From)
		fromTime = &t
	}
	if params.To != nil {
		t := time.Time(*params.To)
		toTime = &t
	}

	// managed_anime_id filter
	var managedAnimeID string
	if params.ManagedAnimeID != nil {
		managedAnimeID = string(*params.ManagedAnimeID)
	}

	// A source is one record table, read under the alias e. Only its sort key
	// columns are read while the page is selected; the contract JSON of a
	// record is built afterwards for the rows of the page alone (rowData), so
	// the cost of a page does not grow with the size of the history.
	type sourceQuery struct {
		kind    string
		table   string
		timeCol string // the record_time column, ordered DESC with id
		// state is the WHERE fragment selecting the source's rows in a state.
		state func(state string) string
		// search is the WHERE fragment of a search pattern bind parameter
		// (e.g. "$4"); nil = the source has no searchable text.
		search  func(pattern string) string
		managed string // column of the managed anime id; "" = none
		columns []string
		// computed members of the contract JSON: name -> SQL over alias src.
		computed [][2]string
	}

	sources := []sourceQuery{
		{
			kind: "routine", table: "routine_activities", timeCol: "last_executed_at",
			state: routineStates.predicate, managed: "managed_anime_id",
			search: func(pattern string) string { return animeTitleMatch("e.managed_anime_id", pattern) },
			columns: []string{"id", "kind", "scope_kind", "scope_id", "managed_anime_id", "outcome",
				"last_executed_at", "last_result_code", "summary", "details", "updated_at"},
		},
		{
			kind: "business", table: "subscription_log_entries", timeCol: "occurred_at",
			state:   func(state string) string { return businessStatePredicate(businessKinds, state) },
			search:  businessSearchPredicate,
			managed: "managed_anime_id",
			columns: []string{"id", "subscription_id", "managed_anime_id", "kind", "outcome", "occurred_at",
				"message_code", "message_params", "rss_source_id", "release_id", "group_ref", "details", "reason_code",
				"subject_type", "subject_id", "subject_version", "correlation_id", "causation_type",
				"causation_id", "dedupe_key", "retention_until"},
			computed: [][2]string{
				{"anime_title", `COALESCE((SELECT left(COALESCE(NULLIF(btrim(ma.title_override), ''), ma.title), 512) FROM managed_anime ma WHERE ma.id = src.managed_anime_id), '')`},
				{"source", `COALESCE((SELECT CASE WHEN sub.origin = 'oneshot' THEN 'one_shot' ELSE 'subscription' END FROM subscriptions sub WHERE sub.id = src.subscription_id), 'subscription')`},
			},
		},
		{
			kind: "automation", table: "automation_dispatch_runs", timeCol: "created_at",
			state: automationStates.predicate,
			columns: []string{"id", "kind", "trigger", "state", "target_count", "enqueued_count", "merged_count",
				"skipped_count", "dispatch_error_count", "started_at", "finished_at", "retention_until"},
		},
		{
			// secret_ref is deliberately absent: it is storage lineage for the
			// encrypted secret value and must never cross the API boundary.
			kind: "plugin", table: "plugin_deliveries", timeCol: "created_at",
			state: pluginStates.predicate, search: pluginSearchPredicate,
			managed: "managed_anime_id",
			columns: []string{"id", "plugin_target_config_id", "plugin_target_config_version", "adapter_kind",
				"adapter_config_snapshot", "delivery_kind", "trigger_kind", "source_owner_type", "source_owner_id",
				"source_owner_version", "source_result_key", "test_request_idempotency_key", "test_request_hash",
				"managed_anime_id", "state", "version", "idempotency_key", "payload_schema_version", "payload",
				"payload_digest", "template_language_version", "template_catalog_version",
				"title_template_snapshot", "body_template_snapshot", "rendered_title", "rendered_body",
				"rendered_message_digest", "target_display", "attempt_count", "last_error_code", "last_error",
				"delivered_at", "created_at", "updated_at", "retention_until"},
		},
	}
	// rowData is the contract JSON of one source row: an explicit column list,
	// the key of each member is its column name.
	rowData := func(src sourceQuery, alias string) string {
		var b strings.Builder
		b.WriteString("jsonb_build_object(")
		for i, column := range src.columns {
			if i > 0 {
				b.WriteString(", ")
			}
			fmt.Fprintf(&b, "'%s', %s.%s", column, alias, column)
		}
		for _, member := range src.computed {
			fmt.Fprintf(&b, ", '%s', %s", member[0], member[1])
		}
		b.WriteString(")")
		return b.String()
	}

	// Filter sources by kind
	var activeSources []sourceQuery
	for _, src := range sources {
		if len(kindFilter) == 0 {
			activeSources = append(activeSources, src)
			continue
		}
		if _, ok := kindFilter[src.kind]; ok {
			activeSources = append(activeSources, src)
		}
	}

	// Defensive guard: an empty active source set would produce an empty UNION,
	// which is a syntax error. With kind validation above this path is
	// unreachable for kind filtering, but keep the guard so a future
	// source/kind split can never regress into a 500.
	if len(activeSources) == 0 {
		return RecordPage{
			Records:    []Record{},
			NextCursor: nil,
		}, nil
	}

	// The keyset cursor is (record_time, kind_rank, id) in DESC order. Within one
	// source the rank is a constant, so the cursor predicate of a source reduces
	// to a plain bound on (record_time, id) that an ordered index can serve; a
	// drift between this reduction and the outer ORDER BY would skip or repeat
	// rows across pages.
	cursorTimeIdx, cursorIDIdx := 0, 0
	if cursorID != "" {
		cursorTimeIdx, cursorIDIdx = argIdx, argIdx+1
		args = append(args, cursorTime, cursorID)
		argIdx += 2
	}
	limitIdx := argIdx
	args = append(args, limit+1)
	argIdx++
	// Build per-source WHERE clauses and union parts. Every part's rank comes
	// from the same map the cursor validator accepts, so an unranked source is
	// a programming error surfaced as a typed failure instead of a malformed
	// query. Each part takes its own top limit+1 keys before the union.
	var unionParts []string
	for _, src := range activeSources {
		rank, ok := recordSourceRanks[src.kind]
		if !ok {
			return RecordPage{}, fmt.Errorf("observability record source %q has no cursor rank", src.kind)
		}
		var where []string
		timeCol := "e." + src.timeCol

		// entry_kind narrows the business source alone.
		if src.kind == "business" && entryKinds != nil {
			where = append(where, "e.kind IN "+sqlList(entryKinds))
		}
		if stateFilter != "" {
			where = append(where, src.state(stateFilter))
		}
		if searchPattern != "" {
			if src.search == nil {
				where = append(where, "FALSE")
			} else {
				where = append(where, src.search(fmt.Sprintf("$%d", argIdx)))
				args = append(args, searchPattern)
				argIdx++
			}
		}
		if fromTime != nil {
			where = append(where, fmt.Sprintf("%s >= $%d", timeCol, argIdx))
			args = append(args, *fromTime)
			argIdx++
		}
		if toTime != nil {
			where = append(where, fmt.Sprintf("%s <= $%d", timeCol, argIdx))
			args = append(args, *toTime)
			argIdx++
		}
		if managedAnimeID != "" {
			if src.managed == "" {
				where = append(where, "FALSE")
			} else {
				where = append(where, fmt.Sprintf("e.%s = $%d", src.managed, argIdx))
				args = append(args, managedAnimeID)
				argIdx++
			}
		}
		if cursorID != "" {
			switch {
			case rank < cursorRank:
				where = append(where, fmt.Sprintf("%s <= $%d", timeCol, cursorTimeIdx))
			case rank > cursorRank:
				where = append(where, fmt.Sprintf("%s < $%d", timeCol, cursorTimeIdx))
			default:
				where = append(where, fmt.Sprintf("(%s, e.id) < ($%d, $%d)", timeCol, cursorTimeIdx, cursorIDIdx))
			}
		}

		part := fmt.Sprintf("(SELECT %s AS record_time, e.id, '%s'::text AS kind, %d::int AS kind_rank FROM %s e", timeCol, src.kind, rank, src.table)
		if len(where) > 0 {
			part += " WHERE " + strings.Join(where, " AND ")
		}
		part += fmt.Sprintf(" ORDER BY %s DESC, e.id DESC LIMIT $%d)", timeCol, limitIdx)
		unionParts = append(unionParts, part)
	}

	// The page keys: strict total order, record_time can tie across sources and
	// ids are only unique per table, so the per-source rank breaks ties.
	var rowDataParts []string
	for _, src := range activeSources {
		rowDataParts = append(rowDataParts, fmt.Sprintf("(SELECT %s FROM %s src WHERE page.kind = '%s' AND src.id = page.id)", rowData(src, "src"), src.table, src.kind))
	}
	query := fmt.Sprintf(
		"WITH page AS (SELECT record_time, id, kind, kind_rank FROM (%s) AS combined ORDER BY record_time DESC, kind_rank DESC, id DESC LIMIT $%d) "+
			"SELECT record_time, id, kind, kind_rank, COALESCE(%s) AS row_data FROM page ORDER BY record_time DESC, kind_rank DESC, id DESC",
		strings.Join(unionParts, " UNION ALL "),
		limitIdx,
		strings.Join(rowDataParts, ", "),
	)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return RecordPage{}, fmt.Errorf("query observability records: %w", err)
	}
	defer rows.Close()

	type recordWithPos struct {
		rec  Record
		time time.Time
		rank int
		id   string
	}

	var records []recordWithPos

	for rows.Next() {
		var recordTime time.Time
		var id, kind string
		var kindRank int
		var rowData []byte
		if err := rows.Scan(&recordTime, &id, &kind, &kindRank, &rowData); err != nil {
			return RecordPage{}, fmt.Errorf("scan observability record: %w", err)
		}

		rec, err := recordFromRow(kind, rowData)
		if err != nil {
			return RecordPage{}, err
		}
		records = append(records, recordWithPos{rec: rec, time: recordTime, rank: kindRank, id: id})
	}
	if err := rows.Err(); err != nil {
		return RecordPage{}, fmt.Errorf("iterate observability records: %w", err)
	}

	// Determine next cursor: if we got more than limit rows, there's a next page.
	// The cursor points to the last row of the returned page so the next request
	// fetches rows strictly after it.
	var nextCursor *string
	pageRecords := make([]Record, 0, limit)
	for i, rp := range records {
		if i >= limit {
			break
		}
		pageRecords = append(pageRecords, rp.rec)
	}
	if len(records) > limit {
		lastPos := records[limit-1]
		c := encodeCursor(lastPos.time, lastPos.rank, lastPos.id)
		nextCursor = &c
	}

	return RecordPage{
		Records:    pageRecords,
		NextCursor: nextCursor,
	}, nil
}

func recordFromRow(kind string, rowData []byte) (Record, error) {
	var value map[string]json.RawMessage
	if err := json.Unmarshal(rowData, &value); err != nil {
		return Record{}, fmt.Errorf("decode %s record: %w", kind, err)
	}
	if value == nil {
		return Record{}, fmt.Errorf("decode %s record: expected JSON object", kind)
	}
	payloadKey, ok := map[string]string{
		"routine":    "routine",
		"business":   "entry",
		"automation": "run",
		"plugin":     "delivery",
	}[kind]
	if !ok {
		return Record{}, fmt.Errorf("build %s record: unknown record kind", kind)
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return Record{}, fmt.Errorf("encode %s record payload: %w", kind, err)
	}
	if kind == "plugin" {
		// The plugin member carries the delivery payload and its frozen adapter
		// config, so both are projected through the same codecs the dedicated
		// endpoint uses: anything that is not provably canonical is withheld and
		// reported through payload_status / config_status instead of being
		// forwarded as-is.
		payload, err = deliverypayload.ProjectRow(payload)
		if err != nil {
			return Record{}, fmt.Errorf("project plugin record: %w", err)
		}
		payload, err = adapterconfig.ProjectRow(payload, "adapter_config_snapshot")
		if err != nil {
			return Record{}, fmt.Errorf("project plugin adapter config: %w", err)
		}
	}
	raw, err := json.Marshal(map[string]json.RawMessage{
		"record_type": json.RawMessage(strconv.Quote(kind)),
		payloadKey:    payload,
	})
	if err != nil {
		return Record{}, fmt.Errorf("build %s record: %w", kind, err)
	}
	return Record{Raw: raw}, nil
}

// --- Cursor helpers ---

// cursorVersion identifies the current cursor payload shape. The payload is
// "<version>|<record_time RFC3339Nano>|<source_rank>|<id>" base64-url encoded.
// Any other version or foreign shape is rejected as a typed invalid cursor
// instead of being mis-decoded, so pagination can never skip or repeat rows.
const cursorVersion = "1"

// encodeCursor encodes a keyset position as an opaque base64 string.
func encodeCursor(t time.Time, rank int, id string) string {
	raw := cursorVersion + "|" + t.UTC().Format(time.RFC3339Nano) + "|" + strconv.Itoa(rank) + "|" + id
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// decodeCursor decodes an opaque cursor back into a time, source rank, and ID.
// Anything that is not the current versioned three-field payload is an error.
func decodeCursor(cursor string) (time.Time, int, string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return time.Time{}, 0, "", fmt.Errorf("base64 decode: %w", err)
	}
	parts := strings.Split(string(raw), "|")
	if len(parts) != 4 || parts[0] != cursorVersion {
		return time.Time{}, 0, "", errors.New("malformed or stale cursor")
	}
	t, err := time.Parse(time.RFC3339Nano, parts[1])
	if err != nil {
		return time.Time{}, 0, "", fmt.Errorf("time parse: %w", err)
	}
	rank, err := strconv.Atoi(parts[2])
	if err != nil {
		return time.Time{}, 0, "", fmt.Errorf("rank parse: %w", err)
	}
	return t, rank, parts[3], nil
}

// invalidCursorError is the typed transport boundary for malformed or stale
// cursors. It maps to a 400 INVALID_CURSOR response; the raw decode error is
// never exposed.
func invalidCursorError() error {
	return &apperrors.BadRequest{Code: "INVALID_CURSOR", Message: "The cursor is invalid or stale."}
}

// nullableStringPointer converts a nullable SQL string into a Go pointer.
func nullableStringPointer(v sql.NullString) *string {
	if v.Valid {
		return &v.String
	}
	return nil
}
