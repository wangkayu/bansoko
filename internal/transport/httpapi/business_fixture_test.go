package httpapi

import (
	domain "bansoko/internal/anime/domain"
	"bansoko/internal/testutil/frozenpolicy"
	"bansoko/internal/transport/httpapi/generated"
	"context"
	"encoding/json"
	"testing"
)

func businessWireRoundTrip(b domain.BusinessSnapshot) (domain.BusinessSnapshot, error) {
	raw, err := json.Marshal(b)
	if err != nil {
		return b, err
	}
	var wire generated.BusinessSnapshot
	if err := json.Unmarshal(raw, &wire); err != nil {
		return b, err
	}
	raw, err = json.Marshal(wire)
	if err != nil {
		return b, err
	}
	var submitted domain.BusinessSnapshot
	if err := json.Unmarshal(raw, &submitted); err != nil {
		return b, err
	}
	return submitted.Freeze()
}

// Seed source history in the same test database before the public save call.
// No evaluation outputs or decoder exemptions are introduced.
func (r *testRig) seedBusinessBody(t *testing.T, body any) {
	t.Helper()
	wrapper, ok := body.(map[string]any)
	if !ok {
		return
	}
	d, ok := wrapper["draft"].(map[string]any)
	if !ok {
		return
	}
	if _, ok := d["business"]; ok {
		return
	}
	b, err := frozenpolicy.SeedBusiness(context.Background(), r.db, `vars.actual = {title: input.release_title};`)
	if err != nil {
		t.Fatal(err)
	}
	// The submitted business owns subgroup selection. Preserve this fixture's
	// explicitly configured provider group rather than freezing empty defaults.
	if subgroups, ok := d["subgroups"]; ok {
		raw, err := json.Marshal(subgroups)
		if err != nil {
			t.Fatal(err)
		}
		b.Sections["subgroups"] = raw
	}
	// Normalize through the actual generated wire shape before hashing: typed
	// optional/default fields must not change the snapshot after submission.
	b, err = businessWireRoundTrip(b)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := b.Digest()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	d["business"] = value
	d["business_digest"] = digest
}
