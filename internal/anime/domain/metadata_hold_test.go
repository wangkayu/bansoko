package domain

import (
	"encoding/json"
	"testing"
)

func TestMetadataHoldSectionValue(t *testing.T) {
	if SectionSchemaVersions[SectionMetadataHold] != MetadataHoldSchemaVersion {
		t.Fatal("metadata_hold schema version is not registered")
	}
	value, err := DefaultSectionValue(SectionMetadataHold)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"allow_missing_linkage":false}` {
		t.Fatalf("default value = %s", raw)
	}
	if err := ValidateSectionValue(SectionMetadataHold, raw); err != nil {
		t.Fatalf("default refused: %v", err)
	}
	for _, bad := range []string{
		`{}`,
		`{"allow_missing_linkage":"false"}`,
		`{"allow_missing_linkage":null}`,
		`{"allow_missing_linkage":false,"extra":1}`,
		`null`,
		`[]`,
	} {
		if err := ValidateSectionValue(SectionMetadataHold, json.RawMessage(bad)); err == nil {
			t.Fatalf("%s accepted", bad)
		}
	}
	for _, name := range RulesOwnerSectionOrder {
		if name == SectionMetadataHold {
			t.Fatal("metadata_hold must not be a rules-owner (overridable) section")
		}
	}
	for _, name := range GlobalOnlySectionOrder {
		if name == SectionMetadataHold {
			return
		}
	}
	t.Fatal("metadata_hold is a Global-only section")
}

// The switch releases the decision but leaves the missing list a fact: readers
// and records still name the absent link.
func TestAdmittedMetadataHoldKeepsTheMissingFact(t *testing.T) {
	hold := MetadataHoldFromBindings(nil, nil)
	if !hold.Held || len(hold.Missing) != 2 {
		t.Fatalf("hold = %+v, want both links missing and held", hold)
	}
	released := AdmittedMetadataHold(hold, true)
	if released.Held {
		t.Fatal("the switch releases the hold")
	}
	if len(released.Missing) != 2 || released.Missing[0] != MetadataHoldMissing("bgm") || released.Missing[1] != MetadataHoldMissing("tmdb") {
		t.Fatalf("released hold lost the missing list: %+v", released)
	}
	if offline := AdmittedMetadataHold(hold, false); !offline.Held {
		t.Fatal("the switch off keeps the hold")
	}
	complete := MetadataHoldFromBindings(&NullableBGMBinding{WorkId: "123"}, &NullableTMDBBinding{WorkId: "456", MediaKind: "tv", Season: 1})
	if complete.Held || len(complete.Missing) != 0 {
		t.Fatalf("a complete binding holds nothing: %+v", complete)
	}
}
