package eventactions

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"

	"bansoko/internal/apperrors"
	"bansoko/internal/events"
	observability "bansoko/internal/observability/port"
	redaction "bansoko/internal/redact"
	"bansoko/internal/templatecatalog/notification"
)

type Service struct {
	db     *sql.DB
	hub    *events.Hub
	logger observability.RuntimeLogger
	root   string
}

func New(db *sql.DB, hub *events.Hub, logger observability.RuntimeLogger) *Service {
	if logger == nil {
		logger = observability.NopRuntimeLogger{}
	}
	return &Service{db: db, hub: hub, logger: logger, root: Root}
}

type Binding struct {
	Kind           string  `json:"kind"`
	Script         *string `json:"script"`
	Enabled        bool    `json:"enabled"`
	TimeoutSeconds int     `json:"timeout_seconds"`
	RecordResult   bool    `json:"record_result"`
	Version        int     `json:"version"`
	Check          string  `json:"check,omitempty"`
}

func (s *Service) ListBindings(ctx context.Context) ([]Binding, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT kind, script, enabled, timeout_seconds, version, record_result FROM event_action_bindings ORDER BY kind`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Binding, 0, 3)
	for rows.Next() {
		var binding Binding
		if err := rows.Scan(&binding.Kind, &binding.Script, &binding.Enabled, &binding.TimeoutSeconds, &binding.Version, &binding.RecordResult); err != nil {
			return nil, err
		}
		out = append(out, binding)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		s.checkBinding(&out[i])
	}
	return out, nil
}

func (s *Service) checkBinding(binding *Binding) {
	if binding.Script == nil {
		return
	}
	view, file := s.openScript(*binding.Script, false)
	if file != nil {
		_ = file.Close()
	}
	binding.Check = view.Check
}

func (s *Service) Save(ctx context.Context, binding Binding) (Binding, error) {
	invalid := func(code, message string) (Binding, error) {
		return Binding{}, &apperrors.Business{Status: 422, Code: code, Message: message}
	}
	if len(notification.PayloadKeys(binding.Kind)) == 0 || binding.Version < 0 ||
		(binding.TimeoutSeconds != 15 && binding.TimeoutSeconds != 30 && binding.TimeoutSeconds != 60) ||
		(binding.Script == nil && binding.Enabled) || (binding.Script != nil && !filename.MatchString(*binding.Script)) {
		return invalid("EVENT_ACTION_INVALID", "The event binding is invalid.")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Binding{}, err
	}
	defer tx.Rollback()
	var previous Binding
	err = tx.QueryRowContext(ctx, `SELECT script, enabled, timeout_seconds, version, record_result FROM event_action_bindings WHERE kind=$1 FOR UPDATE`, binding.Kind).
		Scan(&previous.Script, &previous.Enabled, &previous.TimeoutSeconds, &previous.Version, &previous.RecordResult)
	if err != nil {
		return Binding{}, err
	}
	if previous.Version != binding.Version {
		return Binding{}, &apperrors.VersionConflict{Owner: "event_action", CurrentVersion: int64(previous.Version)}
	}
	same := previous.Script == nil && binding.Script == nil || previous.Script != nil && binding.Script != nil && *previous.Script == *binding.Script
	if binding.Script != nil && (!same || binding.Enabled && !previous.Enabled) {
		s.checkBinding(&binding)
		if binding.Check != Ready {
			return invalid("EVENT_SCRIPT_NOT_AVAILABLE", "The script is not available for selection.")
		}
	}
	if binding.Enabled && !previous.Enabled && !canRun() {
		return invalid("EVENT_SCRIPT_RUNTIME_UNAVAILABLE", "Script execution requires the unprivileged Linux application runtime.")
	}
	if !same || binding.Enabled != previous.Enabled || binding.TimeoutSeconds != previous.TimeoutSeconds || binding.RecordResult != previous.RecordResult {
		// Timeout and reporting changes do not reset the event boundary.
		err = tx.QueryRowContext(ctx, `UPDATE event_action_bindings SET script=$2, enabled=$3, timeout_seconds=$4, record_result=$6,
            version=version+1, active_from=CASE WHEN NOT $5 OR enabled<>$3 THEN clock_timestamp() ELSE active_from END,
            missing=CASE WHEN NOT $5 OR enabled<>$3 THEN false ELSE missing END WHERE kind=$1 RETURNING version`,
			binding.Kind, binding.Script, binding.Enabled, binding.TimeoutSeconds, same, binding.RecordResult).Scan(&binding.Version)
		if err != nil {
			return Binding{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Binding{}, err
	}
	s.checkBinding(&binding)
	return binding, nil
}

type Attempt struct {
	ID       string    `json:"id"`
	Kind     string    `json:"kind"`
	Script   string    `json:"script"`
	Time     time.Time `json:"time"`
	State    string    `json:"state"`
	ExitCode *int      `json:"exit_code,omitempty"`
}

func (s *Service) Attempts(ctx context.Context, warnings bool) ([]Attempt, error) {
	query := `SELECT id, kind, script, started_at, state, exit_code FROM event_action_attempts `
	if warnings {
		query += `WHERE warning `
	}
	rows, err := s.db.QueryContext(ctx, query+`ORDER BY started_at DESC, id DESC LIMIT 50`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Attempt, 0)
	for rows.Next() {
		var item Attempt
		if err := rows.Scan(&item.ID, &item.Kind, &item.Script, &item.Time, &item.State, &item.ExitCode); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// Run blocks until the background lifecycle cancels it. The database lock
// serializes across app incarnations; an out-of-process crash makes running
// rows unconfirmed rather than replaying a potentially effected action.
func (s *Service) Run(ctx context.Context) {
	if !canRun() {
		return
	}
	for ctx.Err() == nil {
		if err := s.consume(ctx); err != nil && ctx.Err() == nil {
			s.logger.Warn(ctx, observability.RuntimeEvent{Message: "event action queue unavailable", Component: "event_actions", Operation: "consume", Outcome: "warning", ReasonCode: "event_action_queue_unavailable"})
		}
		if !wait(ctx, 5*time.Second) {
			return
		}
	}
}

func wait(ctx context.Context, elapsed time.Duration) bool {
	timer := time.NewTimer(elapsed)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (s *Service) consume(ctx context.Context) error {
	qctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	conn, err := s.db.Conn(qctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	var held bool
	// Always discard the underlying connection; a cancelled acquisition or
	// unlock must not leave a session advisory lock in the pool.
	defer func() { _ = conn.Raw(func(any) error { return driver.ErrBadConn }) }()
	if err := conn.QueryRowContext(qctx, `SELECT pg_try_advisory_lock(hashtextextended('bansoko.event_actions', 0))`).Scan(&held); err != nil || !held {
		return err
	}
	result, err := conn.ExecContext(qctx, `UPDATE event_action_attempts SET state='unconfirmed' WHERE state='running'`)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count > 0 {
		s.logger.Warn(ctx, observability.RuntimeEvent{Message: "interrupted event script results unconfirmed", Component: "event_actions", Operation: "recover", Outcome: "unconfirmed"})
		s.hub.Publish(events.Event{Target: events.TargetRecords})
	}
	cancel()
	nextSweep := time.Time{}
	for ctx.Err() == nil {
		if time.Now().After(nextSweep) {
			scanCtx, done := context.WithTimeout(ctx, 5*time.Second)
			err = s.sweep(scanCtx, conn)
			done()
			if err != nil {
				return err
			}
			nextSweep = time.Now().Add(30 * time.Second)
		}
		worked, err := s.step(ctx, conn)
		if err != nil {
			return err
		}
		if !worked && !wait(ctx, 2*time.Second) {
			break
		}
	}
	return nil
}

func (s *Service) sweep(ctx context.Context, conn *sql.Conn) error {
	rows, err := conn.QueryContext(ctx, `SELECT kind, script FROM event_action_bindings WHERE enabled AND missing`)
	if err != nil {
		return err
	}
	var recovered []Binding
	for rows.Next() {
		var binding Binding
		if err := rows.Scan(&binding.Kind, &binding.Script); err != nil {
			rows.Close()
			return err
		}
		recovered = append(recovered, binding)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, binding := range recovered {
		s.checkBinding(&binding)
		if binding.Check == Ready {
			if _, err := conn.ExecContext(ctx, `UPDATE event_action_bindings SET missing=false WHERE kind=$1 AND script=$2`, binding.Kind, binding.Script); err != nil {
				return err
			}
		}
	}
	_, err = conn.ExecContext(ctx, `DELETE FROM event_action_attempts AS r WHERE started_at < now() - interval '90 days'
        AND NOT EXISTS (SELECT 1 FROM notification_admissions AS a WHERE a.id=r.event_id)`)
	return err
}

type invocation struct {
	Version    int            `json:"version"`
	EventID    string         `json:"event_id"`
	Event      string         `json:"event"`
	ActionID   uuid.UUID      `json:"action_id"`
	OccurredAt time.Time      `json:"occurred_at"`
	Facts      map[string]any `json:"facts"`
}

func (s *Service) step(ctx context.Context, conn *sql.Conn) (bool, error) {
	item := invocation{Version: 1, ActionID: uuid.New()}
	var bindingVersion, timeout int
	var recordResult bool
	var name string
	var payload []byte
	queryCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	err := conn.QueryRowContext(queryCtx, `WITH pending AS MATERIALIZED (
        SELECT a.id, a.trigger_kind, a.occurred_at, a.payload, b.version, b.script, b.timeout_seconds, b.record_result,
               COALESCE(d.acquisition_id, f.acquisition_id) AS acquisition_id, a.source_owner_id, a.source_owner_version
        FROM event_action_bindings AS b JOIN notification_admissions AS a ON a.trigger_kind=b.kind
        LEFT JOIN download_tasks AS d ON a.trigger_kind='download_submitted' AND a.source_owner_type='download_task' AND d.id=a.source_owner_id
        LEFT JOIN file_operations AS f ON a.trigger_kind IN ('file_operation_completed','file_operation_failed')
            AND a.source_owner_type='file_operation' AND f.id=a.source_owner_id
        WHERE b.enabled AND a.event_action_ready_at >= b.active_from
          AND NOT EXISTS (SELECT 1 FROM event_action_attempts AS r WHERE r.event_id=a.id)
        ORDER BY a.event_action_ready_at, a.id LIMIT 1 FOR UPDATE OF b
    ), claimed AS (
        INSERT INTO event_action_attempts (id, event_id, kind, binding_version, script, occurred_at, state, acquisition_id, source_id, source_version)
        SELECT $1, id, trigger_kind, version, script, occurred_at, 'running', acquisition_id, source_owner_id, source_owner_version
        FROM pending ON CONFLICT (event_id) DO NOTHING RETURNING event_id
    ) SELECT p.id, p.trigger_kind, p.occurred_at, p.payload, p.version, p.script, p.timeout_seconds, p.record_result
      FROM pending p JOIN claimed c ON c.event_id=p.id`, item.ActionID).
		Scan(&item.EventID, &item.Event, &item.OccurredAt, &payload, &bindingVersion, &name, &timeout, &recordResult)
	cancel()
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	// QueryRow closed its rows and the durable claim committed before the
	// external process can start. Never start if draining or preflight failed.
	state, code := "unconfirmed", (*int)(nil)
	var startedAt *time.Time
	var result scriptResult
	if ctx.Err() == nil {
		view, file := s.openScript(name, false)
		if file != nil {
			defer file.Close()
		}
		switch view.Check {
		case Missing:
			state = "missing"
		case Ready:
			if input, err := scriptInput(item, payload); err == nil {
				var capture *resultCapture
				if recordResult {
					capture = &resultCapture{}
				}
				state, code, startedAt = execute(ctx, file, input, time.Duration(timeout)*time.Second, capture)
				if code != nil && *code >= 0 {
					result = capture.result()
					result.summary, _ = redaction.Apply(result.summary, 240, redaction.Mask, "<redacted runtime message>")
				}
			} else {
				state = "failed"
			}
		default:
			state = "unavailable"
		}
	}
	settle, done := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer done()
	var firstMissing bool
	err = conn.QueryRowContext(settle, `WITH first_missing AS (
        UPDATE event_action_bindings SET missing=true WHERE kind=$3 AND script=$4 AND enabled AND NOT missing AND $2='missing' RETURNING kind
    ), recovered AS (
        UPDATE event_action_bindings SET missing=false WHERE kind=$3 AND script=$4 AND enabled AND missing AND $2 IN ('completed', 'failed', 'timeout') RETURNING kind
    ) UPDATE event_action_attempts SET state=$2, exit_code=$5, warning=EXISTS (SELECT 1 FROM first_missing),
        process_started_at=$6, script_summary=NULLIF($7::text,''), http_status=NULLIF($8::smallint,0)
        WHERE id=$1 RETURNING warning`,
		item.ActionID, state, item.Event, name, code, startedAt, result.summary, result.httpStatus).Scan(&firstMissing)
	if err == nil {
		event := observability.RuntimeEvent{
			Message: "event script finished", Component: "event_actions", Operation: "execute",
			Outcome: state, Kind: item.Event, CorrelationID: item.ActionID.String(),
			ScriptName: name, ExitCode: code,
			ScriptSummary: result.summary, HTTPStatus: result.httpStatus,
		}
		switch {
		case state == "completed":
			s.logger.Info(settle, event)
		case state != "missing" || firstMissing:
			s.logger.Warn(settle, event)
		}
		s.hub.Publish(events.Event{Target: events.TargetRecords})
	}
	return true, err
}

func scriptInput(item invocation, raw []byte) ([]byte, error) {
	if len(raw) > 256*1024 {
		return nil, errors.New("event facts exceed the script input limit")
	}
	var snapshot map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if err := decoder.Decode(&snapshot); err != nil {
		return nil, err
	}
	item.Facts = make(map[string]any)
	for _, key := range notification.PayloadKeys(item.Event) {
		if value, ok := snapshot[key]; ok {
			item.Facts[key] = redact(value)
		}
	}
	for _, prefix := range []string{"rt", "frt"} {
		for _, key := range notification.FileReltitleFactKeys(prefix) {
			if value, ok := snapshot[key]; ok {
				item.Facts[key] = redact(value)
			}
		}
	}
	return json.Marshal(item)
}

func privateKey(key string) bool {
	lower := strings.ToLower(key)
	for _, part := range []string{"secret", "token", "password", "credential", "passkey", "url"} {
		if strings.Contains(lower, part) {
			return true
		}
	}
	return false
}

func redact(value any) any {
	switch entry := value.(type) {
	case string:
		if strings.Contains(entry, "://") {
			return "[redacted]"
		}
	case []any:
		for i, child := range entry {
			entry[i] = redact(child)
		}
	case map[string]any:
		for key, child := range entry {
			if privateKey(key) {
				delete(entry, key)
			} else {
				entry[key] = redact(child)
			}
		}
	}
	return value
}
