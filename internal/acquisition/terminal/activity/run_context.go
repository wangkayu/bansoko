package activity

import (
	"database/sql"
	"encoding/json"
	"fmt"

	"bansoko/internal/observability/businesslog"
)

// Every entry of one acquisition carries the same run context (subgroup,
// episodes, torrent title) plus the facts of its own step, frozen here at
// write time, so the run log can show one download as one row.

// scopeColumns are the scope members every source selects after its own
// columns, in this order (scanned through runScope.targets).
const scopeColumns = `scope.release_id, scope.subscription_id, scope.managed_anime_id, scope.retry_ordinal,
       scope.group_name, scope.release_title, scope.coverage_keys`

// manifestTotals is the torrent's file list of the acquisition root: file
// count and byte total.
const manifestTotals = `(SELECT count(*) FROM acquisition_files mf WHERE mf.acquisition_id = root.id),
       (SELECT COALESCE(sum(mf.exact_size), 0)::bigint FROM acquisition_files mf WHERE mf.acquisition_id = root.id)`

// runScope is the scope of one fact plus its run context.
type runScope struct {
	releaseID, subscriptionID, managedAnimeID string
	retryOrdinal                              int64
	groupName, releaseTitle                   string
	coverageKeys                              sql.NullString
}

func (s *runScope) targets() []any {
	return []any{&s.releaseID, &s.subscriptionID, &s.managedAnimeID, &s.retryOrdinal, &s.groupName, &s.releaseTitle, &s.coverageKeys}
}

// attemptKey is the dedupe key of a fact an import retry makes again (a file
// result, the settlement): the first run keeps the plain key, retry n appends
// ":r<n>".
func (s runScope) attemptKey(key string) string {
	if s.retryOrdinal > 0 {
		return fmt.Sprintf("%s:r%d", key, s.retryOrdinal)
	}
	return key
}

// runContext is the run context alone (no step facts).
func (s runScope) runContext() map[string]any {
	return businesslog.RunContext(s.groupName, s.releaseTitle, s.coverageKeys)
}

// details is the entry details: the run context plus the step facts. An
// encoding failure (impossible for these value types) drops the details
// rather than the entry.
func (s runScope) details(step map[string]any) json.RawMessage {
	values := s.runContext()
	for key, value := range step {
		values[key] = value
	}
	raw, err := businesslog.MarshalDetails(values)
	if err != nil {
		return nil
	}
	return raw
}
