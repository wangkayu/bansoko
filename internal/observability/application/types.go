package application

import (
	"encoding/json"
	"time"
)

type RecordQuery struct {
	Cursor         *string
	Limit          *int
	Kind           *[]string
	Outcome        *string
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
