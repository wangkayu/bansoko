package application

import (
	"encoding/json"
	"time"
)

type RecordQuery struct {
	Cursor *string
	Limit  *int
	Kind   *[]string
	// EntryKind narrows the business source to these journal entry kinds; the
	// other sources are unaffected.
	EntryKind *[]string
	// State selects records by their state (in_progress, success, failure,
	// info); a business entry of a Release run takes the state of its run.
	State *string
	// Q is a case-insensitive search text; a business entry of a Release run
	// matches when any entry of its run does.
	Q *string
	// EventView is the read projection of the business source: primary drops
	// the repeated release filtering process before pagination, all (the
	// default when nil) returns every record. Other sources are unaffected.
	EventView      *string
	From           *time.Time
	To             *time.Time
	ManagedAnimeID *string
}

type Record struct {
	Raw json.RawMessage
}

type RecordPage struct {
	Records    []Record
	NextCursor *string
}

// DiagnosticQuery selects diagnostic episodes. State is "open" or "recovered";
// nil lists both groups, open first.
type DiagnosticQuery struct {
	Cursor *string
	Limit  *int
	State  *string
}

// DiagnosticTarget is the thing an episode is about.
type DiagnosticTarget struct {
	Kind string
	ID   string
	Name string
	Host *string
}

// DiagnosticDetail is the structured fact of the latest failure.
type DiagnosticDetail struct {
	ErrorClass string
	HTTPStatus *int
	Via        *string
}

// DiagnosticAffected counts the distinct objects a condition kept from working.
type DiagnosticAffected struct {
	Kind  string
	Count int
}

// DiagnosticOccurrence is one entry of an episode's bounded recent history.
type DiagnosticOccurrence struct {
	At         time.Time
	ErrorClass *string
	Via        *string
	Host       *string
}

// DiagnosticRetry says what the product does about the condition.
type DiagnosticRetry struct {
	Mode           string
	SettingsTarget *string
}

type DiagnosticEpisode struct {
	ID               string
	Kind             string
	Variant          string
	Target           DiagnosticTarget
	Detail           *DiagnosticDetail
	Affected         *DiagnosticAffected
	Recent           []DiagnosticOccurrence
	FirstFailedAt    time.Time
	LastFailedAt     time.Time
	SucceededAfterAt *time.Time
	OccurrenceCount  int
	Retry            DiagnosticRetry
	Severity         string
	RetentionUntil   time.Time
}

// DiagnosticInternalOperation is the cumulative HTTP failure count for one
// fixed OpenAPI operationId. "request" is the legacy or unrouted bucket.
type DiagnosticInternalOperation struct {
	Operation       string
	OccurrenceCount int
	LastFailedAt    time.Time
	LastRequestID   *string
}

// DiagnosticInternal folds every open internal episode into one summary.
// HTTPOperations breaks out only the HTTP request faults; the total also
// includes internal worker and maintenance sources.
type DiagnosticInternal struct {
	OccurrenceCount int
	LastFailedAt    time.Time
	LastRequestID   *string
	HTTPOperations  []DiagnosticInternalOperation
}

type DiagnosticPage struct {
	Episodes   []DiagnosticEpisode
	NextCursor *string
	// Internal is set on the first page when internal errors are open.
	Internal *DiagnosticInternal
}
