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
	Q              *string
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

// DiagnosticInternal folds every open internal episode into one summary.
type DiagnosticInternal struct {
	OccurrenceCount int
	LastFailedAt    time.Time
	LastRequestID   *string
}

type DiagnosticPage struct {
	Episodes   []DiagnosticEpisode
	NextCursor *string
	// Internal is set on the first page when internal errors are open.
	Internal *DiagnosticInternal
}
