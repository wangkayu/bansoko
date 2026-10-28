package activity

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	observabilityport "bansoko/internal/observability/port"
)

// configHoldEntries: the import's wait for configuration changed and the
// change was not logged yet. Each hold revision is one fact: a waiting plan
// writes one line per missing item with the files it blocks; a plan whose
// hold is gone (queued again, or already planned) writes the configuration
// complete line. A plan that failed out of the wait marks its revision logged
// itself and writes nothing here.
func configHoldEntries(ctx context.Context, db *sql.DB, window time.Duration, batch int) ([]entry, error) {
	rows, err := db.QueryContext(ctx, `
SELECT fact.id, fact.version, fact.artifact_id, fact.acquisition_id, COALESCE(fact.config_hold::text, ''),
       fact.config_hold_revision, fact.updated_at,
       `+scopeColumns+`
FROM postprocessing_plans fact`+scopeJoin+`
WHERE fact.config_hold_revision > fact.config_hold_logged_revision
  AND fact.updated_at > CURRENT_TIMESTAMP - $1::interval
ORDER BY fact.updated_at, fact.id
LIMIT $2`, windowStart(window), batch)
	if err != nil {
		return nil, fmt.Errorf("scan import configuration holds: %w", err)
	}
	defer rows.Close()
	var out []entry
	for rows.Next() {
		var planID, artifactID, acquisitionID, holdText string
		var version, revision int64
		var at time.Time
		var scope runScope
		if err := rows.Scan(append([]any{&planID, &version, &artifactID, &acquisitionID, &holdText, &revision, &at}, scope.targets()...)...); err != nil {
			return nil, err
		}
		key := "acquisition:config_hold:" + planID + ":" + strconv.FormatInt(revision, 10)
		record := func(index int, reason string, message observabilityport.MessageCode, params map[string]any, step map[string]any) observabilityport.ActivityRecord {
			return observabilityport.ActivityRecord{
				SubscriptionID: scope.subscriptionID, ManagedAnimeID: scope.managedAnimeID, Kind: string(observabilityport.ActivityKindImportWaitingConfig),
				Outcome: "info", ReasonCode: reason, SubjectType: "postprocessing_plan", SubjectID: planID, SubjectVersion: version,
				DedupeKey: key + ":" + strconv.Itoa(index), ReleaseID: scope.releaseID, MessageCode: message, MessageParams: params,
				CorrelationID: acquisitionID, CausationType: "artifact", CausationID: artifactID, OccurredAt: at,
				Details: scope.details(step),
			}
		}
		e := entry{managedAnimeID: scope.managedAnimeID, fact: fact{kind: "plan_config_hold", id: planID, fileRef: strconv.FormatInt(revision, 10)}}
		if holdText == "" {
			e.record = record(0, "import_config_resumed", observabilityport.MessageImportConfigResumed, nil, nil)
			out = append(out, e)
			continue
		}
		var hold []struct {
			Item  string `json:"item"`
			Files int64  `json:"files"`
		}
		if err := json.Unmarshal([]byte(holdText), &hold); err != nil {
			return nil, fmt.Errorf("decode import configuration hold of plan %s: %w", planID, err)
		}
		for i, item := range hold {
			params := map[string]any{observabilityport.ParamCount: item.Files}
			message := observabilityport.MessageImportWaitingTMDB
			switch names, variable := strings.CutPrefix(item.Item, "template_variable_undefined:"); {
			case variable:
				message = observabilityport.MessageImportWaitingVariable
				params[observabilityport.ParamVariables] = names
			case item.Item == "binding_missing:bgm":
				message = observabilityport.MessageImportWaitingBGM
			}
			r := record(i, "import_waiting_config", message, params, map[string]any{"item_count": item.Files})
			if i == 0 {
				e.record = r
			} else {
				e.more = append(e.more, r)
			}
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
