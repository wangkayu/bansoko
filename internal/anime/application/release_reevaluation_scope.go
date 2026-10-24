package application

// Release re-evaluation scope: the one definition of which Releases a change
// of an anime's evaluation inputs (rules, offset, AniBT mode, a completed
// metadata hold) evaluates again. A Release submitted for download keeps its
// frozen input, and an adopted, failed-acquisition or baseline Release is
// never evaluated automatically. The schedulers select with the SQL form and
// the RSS list reads the Go form, so a Release the schedulers skip is never
// shown waiting on a re-evaluation.

// releaseReevaluationExcludedVerdicts are the verdicts the schedulers skip.
var releaseReevaluationExcludedVerdicts = map[string]struct{}{
	"adopted_existing":   {},
	"acquisition_failed": {},
	"resume_baseline":    {},
	"start_baseline":     {},
}

// ReleaseReevaluationScopeSQL is the scheduler predicate over a releases row
// aliased r.
const ReleaseReevaluationScopeSQL = `r.download_task_id IS NULL
  AND r.verdict NOT IN ('adopted_existing', 'acquisition_failed', 'resume_baseline', 'start_baseline')`

// ReleaseInReevaluationScope is ReleaseReevaluationScopeSQL for one Release:
// submitted says whether it has a current download task.
func ReleaseInReevaluationScope(verdict string, submitted bool) bool {
	if submitted {
		return false
	}
	_, excluded := releaseReevaluationExcludedVerdicts[verdict]
	return !excluded
}
