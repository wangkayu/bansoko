package application

import (
	"database/sql"
	"encoding/json"
	"testing"

	animedomain "bansoko/internal/anime/domain"
)

func TestSectionOverrideChanged(t *testing.T) {
	schema := sql.NullString{String: animedomain.SectionSchemaVersions[animedomain.SectionNamingRules], Valid: true}
	next := animedomain.OverrideValue{Value: json.RawMessage(`{"b":2,"a":1}`)}
	if sectionOverrideChanged(animedomain.SectionNamingRules, schema, []byte(`{"a":1,"b":2}`), next) {
		t.Fatal("an equal value with different key order must not count as a change")
	}
	if !sectionOverrideChanged(animedomain.SectionNamingRules, schema, []byte(`{"a":1,"b":3}`), next) {
		t.Fatal("a different value must count as a change")
	}
	if !sectionOverrideChanged(animedomain.SectionNamingRules, sql.NullString{}, nil, next) {
		t.Fatal("a missing stored row must count as a change")
	}
	if !sectionOverrideChanged(animedomain.SectionNamingRules, sql.NullString{String: "old-schema", Valid: true}, []byte(`{"a":1,"b":2}`), next) {
		t.Fatal("a schema change must count as a change")
	}
}
