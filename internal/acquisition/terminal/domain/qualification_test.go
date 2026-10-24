package domain

import (
	"strings"
	"testing"
)

func qualificationFixture() string {
	d := `"sha256:` + strings.Repeat("a", 64) + `"`
	return `{"config":{"kind":"submitted","request_hash":` + d + `,"config_digest":` + d + `},"management":{"kind":"none"},"source":{"kind":"seed","seed_identity_digest":` + d + `},"observation":null,"feed":null,"programs":{"attr_fingerprint":` + d + `,"vars_digest":null,"translation":null},"placement":{"kind":"none"}}`
}
func TestQualificationClosedRegistry(t *testing.T) {
	raw := qualificationFixture()
	q, err := DecodeQualification([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeQualification(q.Canonical()); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, from, to string }{
		{"unknown", `"feed":null`, `"feed":null,"skip_guard":true`},
		{"missing", `"feed":null,`, ``},
		{"branch leakage", `"kind":"none"`, `"kind":"none","subscription_ref":"s"`},
		{"duplicate", `"feed":null`, `"feed":null,"feed":null`},
		{"null object", `"placement":{"kind":"none"}`, `"placement":null`},
		{"bad digest", `sha256:`, `sha512:`},
		{"source branch", `"kind":"seed"`, `"kind":"any"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := DecodeQualification([]byte(strings.Replace(raw, tc.from, tc.to, 1))); err == nil {
				t.Fatal("invalid qualification accepted")
			}
		})
	}
}
func TestQualificationScopesVersionsAndPlacementBranches(t *testing.T) {
	d := `"sha256:` + strings.Repeat("a", 64) + `"`
	for _, raw := range []string{
		`{"kind":"saved_revision","candidate_ref":"c","candidate_version":1,"revision":2,"revision_digest":` + d + `}`,
		`{"kind":"owner","managed_anime_ref":"a","config_version":2,"config_digest":` + d + `}`,
	} {
		v, err := ParseStrict([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		if err := Validate("ConfigQualificationV1", v); err != nil {
			t.Fatal(err)
		}
	}
	for _, raw := range []string{
		`{"provider":"mikan","catalog_entry_ref":"0"}`,
		`{"provider":"mikan","catalog_entry_ref":"01"}`,
		`{"provider":"mikan","catalog_entry_ref":"18446744073709551616"}`,
		`{"provider":"mikan","catalog_entry_ref":123}`,
	} {
		v, _ := ParseStrict([]byte(raw))
		if err := Validate("GroupScopeV1", v); err == nil {
			t.Errorf("invalid scope accepted: %s", raw)
		}
	}
	raw := `{"kind":"existing","placement_ref":"p","expected_revision":1,"file_fact_digest":` + d + `}`
	v, _ := ParseStrict([]byte(raw))
	if err := Validate("PlacementQualificationV1", v); err != nil {
		t.Fatal(err)
	}
	// Structural acceptance of existing is not verification of a DB/OS proof.
}
