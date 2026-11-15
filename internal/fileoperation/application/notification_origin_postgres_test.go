package application

import (
	"os"
	"testing"
)

func TestFileNotificationsPreserveAcceptedOrigin(t *testing.T) {
	for _, entry := range []string{"automatic", "subscription_add", "one_shot"} {
		for _, fail := range []bool{false, true} {
			name := entry + "/success"
			if fail {
				name = entry + "/failure"
			}
			t.Run(name, func(t *testing.T) {
				x := newTypedRuntimeFixture(t, func(policy map[string]any) { policy["fixture_entry_kind"] = entry }, false)
				if fail {
					if err := os.Remove(x.source); err != nil {
						t.Fatal(err)
					}
				}
				if handled, err := x.runFileWorker(t); !handled || err != nil {
					t.Fatalf("worker=%v error=%v", handled, err)
				}
				trigger := "file_operation_completed"
				if fail {
					trigger = "file_operation_failed"
				}
				admission := singleFileNotificationAdmission(t, x.db, x.opID, trigger)
				want := "subscription"
				if entry == "one_shot" {
					want = "one_shot"
				}
				if admission.payload["task.origin"] != want {
					t.Fatalf("%s: facts=%v", trigger, admission.payload)
				}
				var provider, frozen string
				if err := x.db.QueryRow(`SELECT n.source_evidence->>'provider', a.input->'source'->'provenance'->>'provider' FROM notification_admissions n JOIN file_operations o ON o.id=n.source_owner_id JOIN acquisitions a ON a.id=o.acquisition_id WHERE n.source_owner_id=$1 AND n.trigger_kind=$2`, x.opID, trigger).Scan(&provider, &frozen); err != nil {
					t.Fatal(err)
				}
				if provider != frozen || provider == "" {
					t.Fatalf("source=%q accepted=%q", provider, frozen)
				}
				// No successor work may manufacture a second logical notification.
				if handled, err := x.runFileWorker(t); handled || err != nil {
					t.Fatalf("replay worker=%v error=%v", handled, err)
				}
				if got := fileNotificationAdmissions(t, x.db, x.opID); len(got) != 1 {
					t.Fatalf("admissions=%v", got)
				}
			})
		}
	}
}
