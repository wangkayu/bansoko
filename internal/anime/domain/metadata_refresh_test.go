package domain

import (
	"encoding/json"
	"testing"
)

func TestMetadataRefreshSectionValue(t *testing.T) {
	if SectionSchemaVersions[SectionMetadataRefresh] != MetadataRefreshSchemaVersion {
		t.Fatal("metadata_refresh schema version is not registered")
	}
	value, err := DefaultSectionValue(SectionMetadataRefresh)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"enabled":false}` {
		t.Fatalf("default value = %s", raw)
	}
	if err := ValidateSectionValue(SectionMetadataRefresh, raw); err != nil {
		t.Fatalf("default refused: %v", err)
	}
	for _, bad := range []string{
		`{}`,
		`{"enabled":"false"}`,
		`{"enabled":null}`,
		`{"enabled":false,"extra":1}`,
		`null`,
		`[]`,
	} {
		if err := ValidateSectionValue(SectionMetadataRefresh, json.RawMessage(bad)); err == nil {
			t.Fatalf("%s accepted", bad)
		}
	}
	for _, name := range RulesOwnerSectionOrder {
		if name == SectionMetadataRefresh {
			t.Fatal("metadata_refresh must not be a rules-owner (overridable) section")
		}
	}
}
