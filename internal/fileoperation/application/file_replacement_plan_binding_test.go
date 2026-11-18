package application

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"bansoko/internal/library/adapter/filesystem"
	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
)

type fileResultLog struct {
	observabilityport.NopRuntimeLogger
	events []observabilityport.RuntimeEvent
}

func (l *fileResultLog) Info(_ context.Context, event observabilityport.RuntimeEvent) {
	if event.Operation == "execute_file" {
		l.events = append(l.events, event)
	}
}

func (l *fileResultLog) Warn(ctx context.Context, event observabilityport.RuntimeEvent) {
	l.Info(ctx, event)
}

func TestOverwriteWithTargetAbsentAtPlanning(t *testing.T) {
	for _, tc := range []struct {
		name, method, displaced string
		arrives                 bool
	}{
		{"still_absent", "no_replace", "absent", false},
		{"arrives_after_plan", "replace", "known", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			db, f := openFileOperationDBWithPolicy(t, unmanagedOverwriteFixturePolicy)
			target := fixtureIncumbentPath(t, f)
			if err := os.Remove(target); err != nil {
				t.Fatal(err)
			}
			s := newFixtureServiceWithPorts(t, db, f, filesystem.OSFileExecutor{}, filesystem.OSFileObserver{})
			log := &fileResultLog{}
			s.SetDiagnosticRecorder(nil, log)
			prepareFixture(t, db, s, f.artifactID)
			if tc.arrives {
				if err := os.WriteFile(target, []byte(fixtureOverwriteIncumbentBytes), 0600); err != nil {
					t.Fatal(err)
				}
			}
			store, err := workitems.NewStore(db, 5, nil)
			if err != nil {
				t.Fatal(err)
			}
			claim, err := store.ClaimDue(ctx, "overwrite-observation", []string{workitems.KindFileExecute}, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			outcome, err := s.Handle(ctx, claim)
			if err != nil || outcome.Kind != worker.OutcomeCommitted || outcome.OwnerResult.State != "completed" {
				t.Fatalf("finish: %+v %v", outcome, err)
			}
			var policy, method, displaced string
			var expectedTarget bool
			if err := db.QueryRow(`SELECT collision_policy, publication_frozen_objects ? 'expected_target',
publication_step_checkpoint->'publish_target'->'proof'->>'atomic_method',
publication_step_checkpoint->'publish_target'->'proof'->'displaced'->>'state' FROM file_operations WHERE id=$1`, claim.Owner.ID).Scan(&policy, &expectedTarget, &method, &displaced); err != nil {
				t.Fatal(err)
			}
			if policy != "overwrite" || expectedTarget != tc.arrives || method != tc.method || displaced != tc.displaced {
				t.Fatalf("policy=%s observed=%v method=%s displaced=%s", policy, expectedTarget, method, displaced)
			}
			if actual, err := os.ReadFile(target); err != nil || string(actual) != "source-content" {
				t.Fatalf("published target = %q, %v", actual, err)
			}
			if len(log.events) != 0 || outcome.AfterCommit == nil {
				t.Fatalf("import log did not wait for the post-commit callback: %+v", log.events)
			}
			outcome.AfterCommit(ctx)
			if len(log.events) != 1 || log.events[0].WorkItemID != claim.ID || log.events[0].StateAfter != "completed" ||
				strings.Contains(log.events[0].Message, "replaced the existing file") != tc.arrives || strings.Contains(log.events[0].Message, target) {
				t.Fatalf("import log did not reflect the committed publication: %+v", log.events)
			}
		})
	}
}
