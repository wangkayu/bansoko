// Package presetdraft builds test drafts the way a real draft is created:
// every per-anime section is one-time input copied from Global, so a draft
// carries complete section values and the business snapshot it froze. Saving,
// previewing or subscribing a draft never reads Global; a fixture without the
// copy fails closed exactly like a damaged client draft would.
package presetdraft

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"

	animedomain "bansoko/internal/anime/domain"
	configuration "bansoko/internal/configuration/application"
	"bansoko/internal/transport/httpapi/generated"
)

// wireRuleSections maps the draft's rules.sections keys to section names.
var wireRuleSections = map[string]string{
	"parser":         animedomain.SectionParser,
	"release_filter": animedomain.SectionReleaseRules,
	"file_filter":    animedomain.SectionFileRules,
	"naming":         animedomain.SectionNamingRules,
	"file_effect":    animedomain.SectionFileEffect,
	"tag_filters":    animedomain.SectionTagFilters,
}

func defaultEnvelopes() (map[string]json.RawMessage, error) {
	out := make(map[string]json.RawMessage, len(wireRuleSections))
	for key, section := range wireRuleSections {
		value, err := animedomain.DefaultSectionValue(section)
		if err != nil {
			return nil, err
		}
		raw, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		wrapped, err := animedomain.SectionEnvelope(raw)
		if err != nil {
			return nil, err
		}
		out[key] = wrapped
	}
	return out, nil
}

// DefaultRulesValue returns wire rules carrying every section at its code
// default, as a draft created from a never-saved Global does.
func DefaultRulesValue() (generated.RulesDraft, error) {
	envelopes, err := defaultEnvelopes()
	if err != nil {
		return generated.RulesDraft{}, err
	}
	raw, err := json.Marshal(envelopes)
	if err != nil {
		return generated.RulesDraft{}, err
	}
	var sections generated.RulesSections
	if err := json.Unmarshal(raw, &sections); err != nil {
		return generated.RulesDraft{}, err
	}
	return generated.RulesDraft{Sections: sections}, nil
}

// DefaultRules is DefaultRulesValue for a test.
func DefaultRules(t testing.TB) generated.RulesDraft {
	t.Helper()
	rules, err := DefaultRulesValue()
	if err != nil {
		t.Fatal(err)
	}
	return rules
}

// DefaultDomainRules is DefaultRules in the domain shape.
func DefaultDomainRules(t testing.TB) animedomain.RulesSections {
	t.Helper()
	envelopes, err := defaultEnvelopes()
	if err != nil {
		t.Fatal(err)
	}
	return animedomain.RulesSections{
		Parser:        envelopes["parser"],
		ReleaseFilter: envelopes["release_filter"],
		FileFilter:    envelopes["file_filter"],
		Naming:        envelopes["naming"],
		FileEffect:    envelopes["file_effect"],
		TagFilters:    envelopes["tag_filters"],
	}
}

// LoadDomain copies the current Global business preset into draft exactly as
// the load-business-preset action does: sections the draft leaves absent
// take the Global value, edited sections are kept, and the draft carries the
// frozen business snapshot and its digest.
func LoadDomain(t testing.TB, db *sql.DB, draft animedomain.AnimeDraft) animedomain.AnimeDraft {
	t.Helper()
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	preset, err := configuration.ReadBusinessPresetTx(ctx, tx)
	_ = tx.Rollback()
	if err != nil {
		t.Fatalf("read business preset: %v", err)
	}
	tagFilters, err := configuration.NewService(db, nil).ReadGlobalTagFilters(ctx)
	if err != nil {
		t.Fatalf("read global tag_filters: %v", err)
	}
	loaded, err := animedomain.LoadBusinessIntoDraft(draft, preset, tagFilters)
	if err != nil {
		t.Fatalf("load business preset into draft: %v", err)
	}
	return loaded
}

// Load is LoadDomain for a wire draft: it copies the current Global business
// preset into draft and returns the wire draft carrying the frozen business
// snapshot and its digest, as a client draft created through the
// load-business-preset action does.
func Load(t testing.TB, db *sql.DB, draft generated.AnimeDraft) generated.AnimeDraft {
	t.Helper()
	raw, err := json.Marshal(draft)
	if err != nil {
		t.Fatal(err)
	}
	var mapped animedomain.AnimeDraft
	if err := json.Unmarshal(raw, &mapped); err != nil {
		t.Fatalf("map wire draft: %v", err)
	}
	loaded := LoadDomain(t, db, mapped)
	raw, err = json.Marshal(loaded)
	if err != nil {
		t.Fatal(err)
	}
	var wire generated.AnimeDraft
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("map loaded draft to wire: %v", err)
	}
	// Digest the snapshot exactly as the server decodes it from the wire, so
	// typed optional fields cannot make the submitted digest disagree.
	raw, err = json.Marshal(wire.Business)
	if err != nil {
		t.Fatal(err)
	}
	var submitted animedomain.BusinessSnapshot
	if err := json.Unmarshal(raw, &submitted); err != nil {
		t.Fatal(err)
	}
	frozen, err := submitted.Freeze()
	if err != nil {
		t.Fatalf("freeze loaded business: %v", err)
	}
	digest, err := frozen.Digest()
	if err != nil {
		t.Fatal(err)
	}
	wire.BusinessDigest = &digest
	return wire
}
