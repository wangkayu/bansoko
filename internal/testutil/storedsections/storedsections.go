// Package storedsections seeds the per-anime section values a subscription
// stores, for fixtures that create an instance with SQL instead of Subscribe.
// Every per-anime section is one-time input with a complete stored value; a
// fixture without them fails closed exactly like a damaged instance would.
package storedsections

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"

	animedomain "bansoko/internal/anime/domain"
)

// Execer is satisfied by *sql.DB and *sql.Tx.
type Execer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

// Sections lists every per-anime section an instance stores.
var Sections = []string{
	animedomain.SectionSubgroups,
	animedomain.SectionParser,
	animedomain.SectionReleaseRules,
	animedomain.SectionFileRules,
	animedomain.SectionNamingRules,
	animedomain.SectionFileEffect,
	animedomain.SectionTagFilters,
}

// DefaultValue returns the code default of one per-anime section as JSON.
func DefaultValue(t testing.TB, section string) json.RawMessage {
	t.Helper()
	value, err := animedomain.DefaultSectionValue(section)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// Seed stores every per-anime section at its code default for animeID. A row
// that already exists is kept, so a fixture can store its own value for a
// section first and seed the rest afterwards.
func Seed(t testing.TB, db Execer, animeID string) {
	t.Helper()
	for _, section := range Sections {
		owner := "rules"
		if section == animedomain.SectionSubgroups {
			owner = "subgroups"
		}
		if _, err := db.ExecContext(context.Background(), `
INSERT INTO anime_config_overrides(managed_anime_id, owner_type, section_name, schema_version, value)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (managed_anime_id, section_name) DO NOTHING`,
			animeID, owner, section, animedomain.SectionSchemaVersions[section], []byte(DefaultValue(t, section))); err != nil {
			t.Fatalf("seed stored %s section for %s: %v", section, animeID, err)
		}
	}
}
