package migrations

import (
	"context"
	"strings"
	"testing"
)

func TestAcquisitionSubscriptionPolicy(t *testing.T) {
	db, _ := setupQualification(t)
	for _, tc := range []struct{ name, kind, policy, state, want string }{
		{"manual-stopped", "subscription_direct", "manual_active_or_stopped", "stopped", ""},
		{"automatic-active", "automatic_source", "automatic_active", "active", ""},
		{"automatic-stopped", "automatic_source", "automatic_active", "stopped", "ACQ_QUALIFICATION_MANAGEMENT_CHANGED"},
		{"mismatched", "automatic_source", "manual_active_or_stopped", "active", "ACQ_QUALIFICATION_INVALID"},
		{"unknown", "subscription_direct", "unknown", "active", "ACQ_QUALIFICATION_INVALID"},
		{"missing", "subscription_direct", "", "active", "ACQ_QUALIFICATION_INVALID"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tag := "policy-" + tc.name
			seedAcceptableRelease(t, db, tag)
			p := validQueued(t, "acq-"+tag, "key-"+tag, tag)
			if _, err := db.ExecContext(context.Background(), `UPDATE subscriptions SET state=$1 WHERE id=$2`, tc.state, "sub-"+tag); err != nil {
				t.Fatal(err)
			}
			mutateQualification(t, &p, func(q map[string]any) {
				m := q["management"].(map[string]any)
				if tc.policy == "" {
					delete(m, "subscription_policy")
				} else {
					m["subscription_policy"] = tc.policy
				}
			})
			p.InputCanonical = strings.Replace(p.InputCanonical, `"kind":"subscription_direct"`, `"kind":"`+tc.kind+`"`, 1)
			p.AdmissionDigest = acqDomainDigest("acquisition-input:v1", []byte(p.InputCanonical))
			_, err := callQueuedAccept(t, db, p)
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %s, got %v", tc.want, err)
			}
		})
	}
}
