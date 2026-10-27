package migrations

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"bansoko/internal/qualityprofile"
)

// The seeded instance-wide retention profile is byte for byte the Go default.
func TestSeededReplacementProfileIsTheDefault(t *testing.T) {
	db := installedDB(t)
	var profile []byte
	if err := db.QueryRow(`SELECT value->'quality_profile' FROM global_config_sections WHERE section_name='replacement'`).Scan(&profile); err != nil {
		t.Fatal(err)
	}
	parsed, err := qualityprofile.Parse(profile)
	if err != nil {
		t.Fatalf("seeded profile is not a valid profile: %v", err)
	}
	want, _ := json.Marshal(qualityprofile.Default())
	got, _ := json.Marshal(parsed)
	if string(got) != string(want) {
		t.Fatalf("seeded profile = %s, want the default %s", got, want)
	}
}

// `-` binds tighter than `->`, so `x->'a' - 'b'` parses as `x -> ('a' - 'b')`
// (operator is not unique: unknown - unknown). Key removal must parenthesize
// the arrow operand.
func TestMigrationsParenthesizeKeyRemovalAfterArrow(t *testing.T) {
	unparenthesized := regexp.MustCompile(`->>?\s*'[^']*'\s*-\s*'`)
	for _, name := range shippedMigrationFiles(t) {
		raw, err := os.ReadFile(filepath.Join(migrationsDir(t), name))
		if err != nil {
			t.Fatal(err)
		}
		if match := unparenthesized.Find(raw); match != nil {
			t.Fatalf("%s: unparenthesized key removal after an arrow: %q", name, match)
		}
	}
}

// The retention profile validator accepts only the current profile shape, and
// retention policies carry no cutoff column.
func TestReplacementProfileValidatorAndNoCutoff(t *testing.T) {
	db := installedDB(t)
	for raw, want := range map[string]bool{
		string(qualityprofile.DefaultJSON()):                                                             true,
		`{"version":"profile-v1","items":[]}`:                                                            true,
		`{"version":"profile-v1","dimensions":[],"ranks":{},"codec_enabled":false}`:                      false,
		`{"version":"profile-v1","items":[{"key":"file.codec","ranks":[]}]}`:                             false,
		`{"version":"profile-v1","items":[{"key":"rt.codec","ranks":[]},{"key":"rt.codec","ranks":[]}]}`: false,
		`{"version":"profile-v1","items":[{"key":"rt.codec","ranks":[[]]}]}`:                             false,
		`{"version":"profile-v1","items":[{"key":"rt.codec","ranks":[["HEVC"]]}],"cutoff":{}}`:           false,
		`{"version":"profile-v1","items":[{"key":"anibt.language","ranks":[["CHS","CHT"],["CHS"]]}]}`:    true,
		`{"version":"profile-v1","items":[{"key":"anibt.language","ranks":[["CHS",1]]}]}`:                false,
		`{"version":"profile-v2","items":[]}`:                                                            false,
		`{"version":"profile-v1","items":[{"key":"rt.sub_flag","ranks":[["soft","ex"],["hard"]]}]}`:      true,
		`{"version":"profile-v1","items":[{"key":"rt.uncensored","ranks":[["true"]]}]}`:                  true,
		`{"version":"profile-v1","items":[{"key":"rt.uncensored","ranks":[["false"]]}]}`:                 true,
		`{"version":"profile-v1","items":[{"key":"rt.uncensored","ranks":[]}]}`:                          false,
		`{"version":"profile-v1","items":[{"key":"rt.uncensored","ranks":[["UNCENSORED"]]}]}`:            false,
		`{"version":"profile-v1","items":[{"key":"rt.uncensored","ranks":[["true"],["false"]]}]}`:        false,
		`{"version":"profile-v1","items":[{"key":"rt.uncensored","ranks":[["true","false"]]}]}`:          false,
		`{"version":"profile-v1","items":[{"key":"rt.content_rating","ranks":[["UNCENSORED"]]}]}`:        false,
	} {
		var ok bool
		if err := db.QueryRow(`SELECT replacement_quality_profile_valid($1::jsonb)`, raw).Scan(&ok); err != nil {
			t.Fatal(err)
		}
		if ok != want {
			t.Errorf("validator(%s) = %v, want %v", raw, ok, want)
		}
	}
	if columnPresent(t, db, "anime_business_configs", "cutoff") {
		t.Fatal("retention policies must not carry a cutoff column")
	}
}

// Replacement comparison values default to an empty object.
func TestReleaseCandidateFactsCompareValuesDefault(t *testing.T) {
	db := installedDB(t)
	var compareDefault string
	if err := db.QueryRow(`SELECT column_default FROM information_schema.columns WHERE table_name='release_candidate_facts' AND column_name='compare_values'`).Scan(&compareDefault); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(compareDefault, "'{}'") {
		t.Fatalf("compare_values default = %q", compareDefault)
	}
}

func columnPresent(t *testing.T, db *sql.DB, table, column string) bool {
	t.Helper()
	var present bool
	if err := db.QueryRowContext(context.Background(), `SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='public' AND table_name=$1 AND column_name=$2)`, table, column).Scan(&present); err != nil {
		t.Fatal(err)
	}
	return present
}
