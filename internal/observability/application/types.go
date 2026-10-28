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

type DiagnosticQuery struct {
	Cursor    *string
	Limit     *int
	Severity  *string
	Component *string
	From      *time.Time
	To        *time.Time
}

type DiagnosticDetails struct {
	Raw json.RawMessage
}

type DiagnosticEpisode struct {
	Component        string
	Details          *DiagnosticDetails
	ErrorCode        string
	FirstFailedAt    time.Time
	ID               string
	LastError        string
	LastFailedAt     time.Time
	ManagedAnimeID   *string
	OccurrenceCount  int
	Operation        string
	RetentionUntil   time.Time
	ScopeID          string
	ScopeKind        string
	Severity         string
	SucceededAfterAt *time.Time
}

type DiagnosticPage struct {
	Episodes   []DiagnosticEpisode
	NextCursor *string
}
