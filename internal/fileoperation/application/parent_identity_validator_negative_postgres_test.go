package application

import (
	"context"
	"encoding/json"
	"testing"
)

// Parent-dimension component negative: directly invoke the
// parent schema anchors observed_identity_valid and file_wire_identity_valid with malformed
// parent Identities (the PathIdentityV1 shape). This
// isolates the parent predicate (the prepare producer negative covers it only indirectly). The
// three-step proof->parent dimension is registered composed_guarded_coverage:
// the write-entry enumeration is statically borne by the adjudication, the
// inequality
// negatives come from the group matrix (stage_temp/reserve_version/publish_target parent), and the
// full-entry runs come from the production chain; this function supplies the validator-direct
// component negative. Pure frozen reference only — objects, before, after and digest keep their own
// independent validation.
func verifyParentIdentityValidatorNegatives(t *testing.T, ctx context.Context) {
	t.Helper()
	owner := ownerTestDB(t)
	valid := func() map[string]any {
		return map[string]any{"kind": "local_fs", "device": "2049", "inode": "824432"}
	}
	call := func(name string, parent map[string]any, want bool) {
		b, err := json.Marshal(parent)
		if err != nil {
			t.Fatal(err)
		}
		for _, validator := range []string{"observed_identity_valid", "file_wire_identity_valid"} {
			var got bool
			if err := owner.QueryRowContext(ctx, `SELECT public.`+validator+`($1::jsonb)`, string(b)).Scan(&got); err != nil {
				t.Fatal(err)
			}
			if got != want {
				t.Fatalf("parent validator %s %s=%v want %v", validator, name, got, want)
			}
			t.Logf("sql_component Q12/%s/%s valid=%v", validator, name, got)
		}
	}
	// PathIdentityV1: kind, device and inode only. The retired
	// FV members (mount_ref, generation/change nanos, handle) are refused.
	call("valid", valid(), true)
	p := valid()
	p["extra"] = true
	call("extra_key", p, false)
	p = valid()
	p["kind"] = "nfs"
	call("wrong_kind", p, false)
	p = valid()
	p["inode"] = "0"
	call("inode_zero", p, false)
	p = valid()
	p["device"] = "-1"
	call("device_negative", p, false)
	p = valid()
	p["device"] = "99999999999999999999"
	call("device_oversize", p, false)
	p = valid()
	p["device"] = json.Number("2049")
	call("device_number", p, false)
	for _, retired := range []string{"mount_ref", "generation_nanos", "change_nanos", "handle"} {
		p = valid()
		p[retired] = map[string]any{"state": "unavailable", "value": nil, "reason_code": "RETIRED"}
		call("retired_"+retired, p, false)
	}
}
