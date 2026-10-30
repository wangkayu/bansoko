package application

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"bansoko/internal/apperrors"
	observabilityport "bansoko/internal/observability/port"
)

// Record states of the records state filter. A state is read from each
// source's own lifecycle column; a business entry that belongs to a Release
// run takes the state of its run.
const (
	RecordStateInProgress = "in_progress"
	RecordStateSuccess    = "success"
	RecordStateFailure    = "failure"
	RecordStateInfo       = "info"
)

var recordStates = map[string]bool{
	RecordStateInProgress: true,
	RecordStateSuccess:    true,
	RecordStateFailure:    true,
	RecordStateInfo:       true,
}

// Event views of the records read projection. all is the API default (nil
// parameter); the events tab asks for primary explicitly.
const (
	RecordEventViewPrimary = "primary"
	RecordEventViewAll     = "all"
)

var recordEventViews = map[string]bool{
	RecordEventViewPrimary: true,
	RecordEventViewAll:     true,
}

// primaryEventViewReasons are the release_processed info reasons the primary
// view hides. Both repeat on every RSS cycle and neither is a badge record, so
// hiding them never hides a failure the owner has to act on.
var primaryEventViewReasons = []string{"awaiting_competition", "no_placeable_files"}

// primaryEventViewPredicate excludes the repeated release filtering process
// from the business source alone. COALESCE keeps older records without a
// reason visible, and the outcome check keeps the same reason on a failure.
func primaryEventViewPredicate() string {
	return fmt.Sprintf("NOT (e.kind = '%s' AND e.outcome = 'info' AND COALESCE(e.reason_code, '') IN %s)",
		string(observabilityport.ActivityKindReleaseProcessed), sqlList(primaryEventViewReasons))
}

// recordEventViewParam validates the event_view parameter; nil is the API
// default, all.
func recordEventViewParam(value *string) (string, error) {
	if value == nil {
		return RecordEventViewAll, nil
	}
	if !recordEventViews[*value] {
		return "", &apperrors.BadRequest{Code: "INVALID_EVENT_VIEW", Message: fmt.Sprintf("Unknown event view %q.", *value)}
	}
	return *value, nil
}

// maxRecordSearchRunes bounds the q parameter (the contract's maxLength).
const maxRecordSearchRunes = 100

// sourceStates maps a record state to the stored values of a non-business
// source column that carry it. A state with no values selects nothing.
type sourceStates struct {
	column string
	values map[string][]string
}

var (
	routineStates = sourceStates{column: "e.outcome", values: map[string][]string{
		RecordStateSuccess: {"success"},
		RecordStateFailure: {"failure"},
		RecordStateInfo:    {"warning"},
	}}
	automationStates = sourceStates{column: "e.state", values: map[string][]string{
		RecordStateInProgress: {"queued", "running"},
		RecordStateSuccess:    {"completed"},
		RecordStateFailure:    {"partial", "failed"},
	}}
	// An unknown delivery is unconfirmed: it counts as a failure.
	pluginStates = sourceStates{column: "e.state", values: map[string][]string{
		RecordStateInProgress: {"pending", "delivering", "waiting_retry"},
		RecordStateSuccess:    {"delivered"},
		RecordStateFailure:    {"failed", "unknown"},
	}}
)

// predicate is the WHERE fragment of state on this source.
func (s sourceStates) predicate(state string) string {
	values := s.values[state]
	if len(values) == 0 {
		return "FALSE"
	}
	return s.column + " IN " + sqlList(values)
}

// Business entry kinds by how their run is read. The steps and the settlement
// are the entries the events view groups into one acquisition of a Release;
// the core steps are the ones that make such a group a run (a group of only
// placement or replacement entries renders as single rows).
var (
	runStepKinds = []string{
		string(observabilityport.ActivityKindArtifactVerified),
		string(observabilityport.ActivityKindDownloadChanged),
		string(observabilityport.ActivityKindFileAlignmentCompleted),
		string(observabilityport.ActivityKindFileAlignmentFailed),
		string(observabilityport.ActivityKindPlacementChanged),
		string(observabilityport.ActivityKindReplacementDecided),
	}
	runCoreStepKinds = []string{
		string(observabilityport.ActivityKindArtifactVerified),
		string(observabilityport.ActivityKindDownloadChanged),
		string(observabilityport.ActivityKindFileAlignmentCompleted),
		string(observabilityport.ActivityKindFileAlignmentFailed),
	}
	settledKind = string(observabilityport.ActivityKindAcquisitionSettled)
)

// businessOutcomeStates: a business entry outside a run carries its own
// outcome as its state; no stored outcome is in progress.
var businessOutcomeStates = map[string]string{
	RecordStateSuccess: "success",
	RecordStateFailure: "failure",
	RecordStateInfo:    "info",
}

// settlementOf is the settlement that closes the run of entry x: the first
// acquisition_settled entry of the same Release at or after x. The events
// view orders a Release's entries by (occurred_at, kind rank, id) and the
// settlement ranks after every step, so a step at the settlement's own time
// belongs to it; settlements at one time order by id byte-wise.
func settlementOf(x, column string) string {
	return fmt.Sprintf(`(SELECT s.%[2]s FROM subscription_log_entries s WHERE s.release_id = %[1]s.release_id AND s.kind = '%[3]s' AND s.occurred_at >= %[1]s.occurred_at ORDER BY s.occurred_at, s.id COLLATE "C" LIMIT 1)`, x, column, settledKind)
}

// runStateExpr is the state of a run step e: the outcome of the settlement
// that closes its run; with none yet, in_progress when the open run has a
// core step, else the entry's own outcome.
func runStateExpr() string {
	openCore := fmt.Sprintf(`EXISTS (SELECT 1 FROM subscription_log_entries c WHERE c.release_id = e.release_id AND c.kind IN %s AND NOT EXISTS (SELECT 1 FROM subscription_log_entries s2 WHERE s2.release_id = c.release_id AND s2.kind = '%s' AND s2.occurred_at >= c.occurred_at))`, sqlList(runCoreStepKinds), settledKind)
	return fmt.Sprintf(`(CASE WHEN e.release_id IS NULL THEN e.outcome ELSE COALESCE(%s, CASE WHEN %s THEN '%s' ELSE e.outcome END) END)`, settlementOf("e", "outcome"), openCore, RecordStateInProgress)
}

// runKeyExpr identifies the run of entry x: its settlement's id, NULL while
// the run is open.
func runKeyExpr(x string) string {
	return fmt.Sprintf(`(CASE WHEN %[1]s.kind = '%[2]s' THEN %[1]s.id ELSE %[3]s END)`, x, settledKind, settlementOf(x, "id"))
}

// businessKindClasses splits the entry kinds the query reads into the
// settlement, the run steps and the other kinds. entryKinds nil means every
// kind; then others is nil and the other class is "every kind but the run
// kinds".
type businessKindClasses struct {
	settled bool
	steps   []string
	others  []string
	all     bool
}

func classifyBusinessKinds(entryKinds []string) businessKindClasses {
	if entryKinds == nil {
		return businessKindClasses{settled: true, steps: runStepKinds, all: true}
	}
	var out businessKindClasses
	step := map[string]bool{}
	for _, kind := range runStepKinds {
		step[kind] = true
	}
	for _, kind := range entryKinds {
		switch {
		case kind == settledKind:
			out.settled = true
		case step[kind]:
			out.steps = append(out.steps, kind)
		default:
			out.others = append(out.others, kind)
		}
	}
	return out
}

// businessStatePredicate selects business entries in state. Each class is a
// separate branch keyed on kind, so a query narrowed to one kind (the badge
// reads failed settlements) reduces to kind and outcome equalities the
// (kind, outcome, occurred_at, id) index serves.
func businessStatePredicate(classes businessKindClasses, state string) string {
	var branches []string
	outcome, hasOutcome := businessOutcomeStates[state]
	if classes.settled && hasOutcome {
		branches = append(branches, fmt.Sprintf("(e.kind = '%s' AND e.outcome = '%s')", settledKind, outcome))
	}
	if len(classes.steps) > 0 {
		branches = append(branches, fmt.Sprintf("(e.kind IN %s AND %s = '%s')", sqlList(classes.steps), runStateExpr(), state))
	}
	if hasOutcome {
		switch {
		case classes.all:
			branches = append(branches, fmt.Sprintf("(e.kind NOT IN %s AND e.outcome = '%s')", sqlList(append([]string{settledKind}, runStepKinds...)), outcome))
		case len(classes.others) > 0:
			branches = append(branches, fmt.Sprintf("(e.kind IN %s AND e.outcome = '%s')", sqlList(classes.others), outcome))
		}
	}
	if len(branches) == 0 {
		return "FALSE"
	}
	return "(" + strings.Join(branches, " OR ") + ")"
}

// animeTitleMatch: the entry's anime title (or its override) matches.
func animeTitleMatch(managed, pattern string) string {
	return fmt.Sprintf("EXISTS (SELECT 1 FROM managed_anime ma WHERE ma.id = %[1]s AND (ma.title ILIKE %[2]s OR ma.title_override ILIKE %[2]s))", managed, pattern)
}

// entryTextMatch: one of the entry's frozen display facts matches.
func entryTextMatch(x, pattern string) string {
	return fmt.Sprintf("(%[1]s.details->>'release_title' ILIKE %[2]s OR %[1]s.details->>'file_name' ILIKE %[2]s OR %[1]s.details->>'group_name' ILIKE %[2]s)", x, pattern)
}

// businessSearchPredicate selects business entries matching the search: the
// anime title, or the entry's display facts; an entry of a Release run
// matches when any entry of the same run does, so a search selects whole
// runs. Every entry of one Release shares its anime.
func businessSearchPredicate(pattern string) string {
	runKinds := sqlList(append([]string{settledKind}, runStepKinds...))
	sameRun := fmt.Sprintf("EXISTS (SELECT 1 FROM subscription_log_entries m WHERE m.release_id = e.release_id AND m.kind IN %s AND %s AND %s IS NOT DISTINCT FROM %s)",
		runKinds, entryTextMatch("m", pattern), runKeyExpr("m"), runKeyExpr("e"))
	return fmt.Sprintf("(%s OR (CASE WHEN e.release_id IS NOT NULL AND e.kind IN %s THEN %s ELSE %s END))",
		animeTitleMatch("e.managed_anime_id", pattern), runKinds, sameRun, entryTextMatch("e", pattern))
}

// pluginSearchPredicate: the notification target's name or the anime title.
func pluginSearchPredicate(pattern string) string {
	return fmt.Sprintf("(EXISTS (SELECT 1 FROM plugin_target_configs c WHERE c.id = e.plugin_target_config_id AND c.name ILIKE %s) OR %s)",
		pattern, animeTitleMatch("e.managed_anime_id", pattern))
}

// recordStateParam validates the state parameter ("" = no filter).
func recordStateParam(value *string) (string, error) {
	if value == nil {
		return "", nil
	}
	if !recordStates[*value] {
		return "", &apperrors.BadRequest{Code: "INVALID_STATE", Message: fmt.Sprintf("Unknown record state %q.", *value)}
	}
	return *value, nil
}

// recordSearchParam validates the q parameter and returns its ILIKE pattern
// ("" = no search). The text is trimmed; blank text is no search.
func recordSearchParam(value *string) (string, error) {
	if value == nil {
		return "", nil
	}
	text := strings.TrimSpace(*value)
	if text == "" {
		return "", nil
	}
	if !utf8.ValidString(text) || strings.ContainsRune(text, 0) {
		return "", &apperrors.BadRequest{Code: "INVALID_QUERY", Message: "The search text is not valid text."}
	}
	if utf8.RuneCountInString(text) > maxRecordSearchRunes {
		return "", &apperrors.BadRequest{Code: "INVALID_QUERY", Message: fmt.Sprintf("The search text is longer than %d characters.", maxRecordSearchRunes)}
	}
	return likePattern(text), nil
}

// likePattern is a contains-pattern for text under LIKE's default escape
// character (backslash).
func likePattern(text string) string {
	escaped := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(text)
	return "%" + escaped + "%"
}

// recordEntryKindsParam validates the entry_kind parameter: sorted unique
// journal kinds, nil = no filter.
func recordEntryKindsParam(values *[]string) ([]string, error) {
	if values == nil || len(*values) == 0 {
		return nil, nil
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(*values))
	for _, kind := range *values {
		if !observabilityport.IsActivityKind(kind) {
			return nil, &apperrors.BadRequest{Code: "INVALID_ENTRY_KIND", Message: fmt.Sprintf("Unknown journal entry kind %q.", kind)}
		}
		if !seen[kind] {
			seen[kind] = true
			out = append(out, kind)
		}
	}
	sort.Strings(out)
	return out, nil
}

// sqlList renders closed-set constants as a parenthesised SQL list. The
// values are code constants or values checked against a closed set; quoting
// keeps the fragment well formed regardless. Literal constants (not bind
// parameters) let the planner match partial indexes and kind equalities.
func sqlList(values []string) string {
	quoted := make([]string, len(values))
	for i, value := range values {
		quoted[i] = "'" + strings.ReplaceAll(value, "'", "''") + "'"
	}
	return "(" + strings.Join(quoted, ", ") + ")"
}
