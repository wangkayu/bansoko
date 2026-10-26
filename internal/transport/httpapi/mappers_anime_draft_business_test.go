package httpapi

import (
	"encoding/json"
	"testing"

	"bansoko/internal/transport/httpapi/generated"
)

// Every draft-carrying input must keep the frozen business snapshot and its
// digest: the read model and commands evaluate only the draft's own business.
func TestMapAnimeDraftKeepsBusinessSnapshot(t *testing.T) {
	var draft generated.AnimeDraft
	if err := json.Unmarshal([]byte(`{
		"origin": "anibt", "origin_entry_ref": "123", "title": "Show",
		"metadata": {}, "rss_sources": [],
		"subgroups": {"max_rss": 3, "entries": []},
		"rules": {"sections": {}},
		"business": {"schema_version": "business-snapshot-v1", "sections": {}, "section_versions": {}, "section_schemas": {},
			"parser": {"schema_version": "parser-policy-envelope-v1", "kind": "regex", "policy_version": 1, "profile_version": 1, "rules_owner_version": 1, "source_owner": "global", "provenance": "global"},
			"retention": {"admission_mode": "all", "quality_profile": {"version": "profile-v1", "items": []}},
			"retention_version": 1, "unclassified_route": "skip", "file_effect_version": 1,
			"programs": {"vars": {"digest": "sha256:v", "version": "naming-vars-v1@sha256:v"}, "attrrules": {"digest": "sha256:a", "version": "release-facts-v1@sha256:a"}}},
		"business_digest": "sha256:d"
	}`), &draft); err != nil {
		t.Fatal(err)
	}
	for name, input := range map[string]generated.RSSListViewInput{
		"rss list current_input": {InputSource: "current_input", Draft: &draft},
	} {
		mapped, err := mapRSSListReadInput(input)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if mapped.Draft == nil || mapped.Draft.Business == nil || mapped.Draft.BusinessDigest == nil || *mapped.Draft.BusinessDigest != "sha256:d" {
			t.Fatalf("%s dropped the business snapshot: %+v", name, mapped.Draft)
		}
		if mapped.Draft.Business.Programs.Vars.Digest != "sha256:v" {
			t.Fatalf("%s lost the exact Vars program reference: %+v", name, mapped.Draft.Business.Programs)
		}
	}
	direct, err := mapAnimeDraft(draft)
	if err != nil {
		t.Fatal(err)
	}
	if direct.Business == nil || direct.BusinessDigest == nil {
		t.Fatalf("mapAnimeDraft dropped the business snapshot")
	}
}
