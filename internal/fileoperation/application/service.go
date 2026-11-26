package application

import (
	"database/sql"
	"errors"
	"time"

	facts "bansoko/internal/release/facts"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
)

// NewService constructs the FileOperation owner. Targets are absolute by
// construction, so the service needs no library-root resolver.
func NewService(db *sql.DB, executor FileExecutor, observer FileObserver, clocks ...func() time.Time) (*Service, error) {
	if db == nil {
		return nil, errors.New("file operation service database is nil")
	}
	if executor == nil {
		return nil, errors.New("file operation executor is nil")
	}
	if observer == nil {
		return nil, errors.New("file operation observer is nil")
	}
	now := func() time.Time { return time.Now().UTC() }
	if len(clocks) > 0 && clocks[0] != nil {
		now = clocks[0]
	}
	return &Service{
		db:        db,
		workItems: &workitems.Store{DB: db, RetentionDays: workitems.DefaultRetentionDays},
		executor:  executor,
		observer:  observer,
		parentAuthorizer: func() ParentIdentityAuthorizer {
			if authorizer, ok := executor.(ParentIdentityAuthorizer); ok {
				return authorizer
			}
			return nil
		}(),
		// The media facts store reads the Release snapshot prepare requires;
		// SetMediaFactsStore may replace it.
		mediaFacts: facts.NewStore(db),
		now:        now,
	}, nil
}

var _ worker.Handler = (*Service)(nil)
