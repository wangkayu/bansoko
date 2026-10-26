package integration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"

	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/testutil/frozenpolicy"
	"bansoko/internal/transport/httpapi/generated"
)

// draftRuleSections maps the draft's rules.sections keys to the business
// snapshot section names they freeze into.
var draftRuleSections = map[string]string{
	"parser":         animedomain.SectionParser,
	"release_filter": animedomain.SectionReleaseRules,
	"file_filter":    animedomain.SectionFileRules,
	"naming":         animedomain.SectionNamingRules,
	"file_effect":    animedomain.SectionFileEffect,
}

// seedDraftBusiness persists real Vars/attr source programs and returns the
// complete business snapshot the draft submits, with its digest. Rule sections
// the draft carries, the draft's subgroups and any persisted Global retention
// carry into the snapshot, so the frozen business states exactly what the
// draft configures; sections the draft leaves absent take the copy a new draft
// gets — the persisted Global naming row when one exists and the code default
// otherwise. Admission consumes only this frozen business: there is no
// active-sample fallback.
func seedDraftBusiness(t *testing.T, db *sql.DB, draft any) (generated.BusinessSnapshot, string) {
	t.Helper()
	b, err := frozenpolicy.SeedBusiness(context.Background(), db, `vars.actual = {title: input.release_title};`)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(draft)
	if err != nil {
		t.Fatal(err)
	}
	var shape struct {
		Subgroups json.RawMessage `json:"subgroups"`
		Rules     struct {
			Sections map[string]struct {
				Mode  string          `json:"mode"`
				Value json.RawMessage `json:"value"`
			} `json:"sections"`
		} `json:"rules"`
	}
	if err := json.Unmarshal(raw, &shape); err != nil {
		t.Fatal(err)
	}
	// A draft that leaves naming_rules absent copies the persisted Global row.
	// Naming targets are absolute by construction and the code default names
	// the deployment mount /library, so a real-effect fixture pins the rooted
	// templates in Global and the frozen snapshot must carry them. A section the
	// draft carries below overrides this copy.
	var namingSchema string
	var namingValue []byte
	var namingVersion int64
	switch err := db.QueryRowContext(context.Background(), `
SELECT schema_version, value, version FROM global_config_sections WHERE section_name = $1`, animedomain.SectionNamingRules).Scan(&namingSchema, &namingValue, &namingVersion); {
	case err == nil:
		b.Sections[animedomain.SectionNamingRules] = json.RawMessage(namingValue)
		b.SectionVersions[animedomain.SectionNamingRules] = namingVersion
		b.SectionSchemas[animedomain.SectionNamingRules] = namingSchema
	case errors.Is(err, sql.ErrNoRows):
	default:
		t.Fatal(err)
	}
	for key, section := range shape.Rules.Sections {
		name, ok := draftRuleSections[key]
		if !ok {
			t.Fatalf("draft rules section %q has no business section", key)
		}
		if section.Mode == "replace" {
			b.Sections[name] = section.Value
		}
	}
	if len(shape.Subgroups) > 0 {
		b.Sections[animedomain.SectionSubgroups] = shape.Subgroups
	}
	// A draft loads the persisted Global retention when one exists, exactly
	// as the business preset does; the snapshot then freezes it.
	var retention animedomain.RetentionValue
	var quality []byte
	switch err := db.QueryRowContext(context.Background(), `
SELECT value->>'admission_mode', value->'quality_profile'
FROM global_config_sections WHERE section_name = 'retention'`).Scan(&retention.AdmissionMode, &quality); {
	case err == nil:
		retention.QualityProfile = quality
		b.Retention = retention
	case errors.Is(err, sql.ErrNoRows):
	default:
		t.Fatal(err)
	}
	// Normalize through the generated wire shape before hashing, exactly as
	// the public save decodes it.
	raw, err = json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	var wire generated.BusinessSnapshot
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	raw, err = json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	var submitted animedomain.BusinessSnapshot
	if err := json.Unmarshal(raw, &submitted); err != nil {
		t.Fatal(err)
	}
	submitted, err = submitted.Freeze()
	if err != nil {
		t.Fatal(err)
	}
	digest, err := submitted.Digest()
	if err != nil {
		t.Fatal(err)
	}
	raw, err = json.Marshal(submitted)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	return wire, digest
}

// draftSectionEnvelopes returns the five rules sections of business as the
// draft's replace envelopes, keyed by the draft's rules.sections keys.
func draftSectionEnvelopes(t *testing.T, wire generated.BusinessSnapshot) map[string]json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	var business animedomain.BusinessSnapshot
	if err := json.Unmarshal(raw, &business); err != nil {
		t.Fatal(err)
	}
	out := make(map[string]json.RawMessage, len(draftRuleSections))
	for key, section := range draftRuleSections {
		wrapped, err := animedomain.SectionEnvelope(business.Sections[section])
		if err != nil {
			t.Fatal(err)
		}
		out[key] = wrapped
	}
	return out
}

// withDraftBusiness attaches the frozen business to a wire draft and gives the
// draft the same complete section values its snapshot froze.
func withDraftBusiness(t *testing.T, db *sql.DB, draft generated.AnimeDraft) generated.AnimeDraft {
	t.Helper()
	wire, digest := seedDraftBusiness(t, db, draft)
	sections := map[string]json.RawMessage{}
	raw, err := json.Marshal(draft.Rules.Sections)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &sections); err != nil {
		t.Fatal(err)
	}
	for key, value := range draftSectionEnvelopes(t, wire) {
		sections[key] = value
	}
	raw, err = json.Marshal(sections)
	if err != nil {
		t.Fatal(err)
	}
	draft.Rules.Sections = generated.RulesSections{}
	if err := json.Unmarshal(raw, &draft.Rules.Sections); err != nil {
		t.Fatal(err)
	}
	draft.Business = &wire
	draft.BusinessDigest = &digest
	return draft
}

// withDomainDraftBusiness attaches the frozen business to a domain draft.
func withDomainDraftBusiness(t *testing.T, db *sql.DB, draft animedomain.AnimeDraft) animedomain.AnimeDraft {
	t.Helper()
	wire, digest := seedDraftBusiness(t, db, draft)
	raw, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	var business animedomain.BusinessSnapshot
	if err := json.Unmarshal(raw, &business); err != nil {
		t.Fatal(err)
	}
	envelopes := draftSectionEnvelopes(t, wire)
	draft.Rules.Sections.Parser = envelopes["parser"]
	draft.Rules.Sections.ReleaseFilter = envelopes["release_filter"]
	draft.Rules.Sections.FileFilter = envelopes["file_filter"]
	draft.Rules.Sections.Naming = envelopes["naming"]
	draft.Rules.Sections.FileEffect = envelopes["file_effect"]
	draft.Business = &business
	draft.BusinessDigest = &digest
	return draft
}
