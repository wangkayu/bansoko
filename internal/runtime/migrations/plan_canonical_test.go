package migrations

import "testing"

func TestPlanCanonicalSyntax(t *testing.T) {
	owner, _, _ := progressProbe(t, "canonical-syntax", nil, nil)
	for _, raw := range []string{`{"a":1,"b":false}`, `{"a":"猫<>&","b":[0,-1,null]}`, `{"a":"\u000a"}`, `{"a":"x y"}`} {
		var valid bool
		if err := owner.QueryRow(`SELECT plan_canonical_valid($1)`, raw).Scan(&valid); err != nil {
			t.Fatal(err)
		}
		if !valid {
			t.Fatalf("canonical rejected: %s", raw)
		}
	}
	for _, raw := range []string{` {"a":1}`, `{"a":1} `, `{"b":false,"a":1}`, `{"a":1,"a":2}`, `{"a":1.0}`, `{"a":1e0}`, `{"a":-0}`, `{"a":"\n"}`, `{"a":"\u732b"}`, `{"\u0061":1}`, `{"a":1} {}`, `{"a":"\u000A"}`} {
		var valid bool
		if err := owner.QueryRow(`SELECT plan_canonical_valid($1)`, raw).Scan(&valid); err != nil {
			t.Fatal(err)
		}
		if valid {
			t.Fatalf("noncanonical accepted: %s", raw)
		}
	}
}
