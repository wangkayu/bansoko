package migrations

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"bansoko/internal/qualityprofile"
)

func legacyDefaultProfile142(t *testing.T) []byte {
	t.Helper()
	var profile map[string]any
	if err := json.Unmarshal(qualityprofile.DefaultJSON(), &profile); err != nil {
		t.Fatal(err)
	}
	profile["version"] = "profile-v1"
	items := profile["items"].([]any)
	last := items[len(items)-1].(map[string]any)
	last["key"] = "release.revision"
	return mustJSON(t, profile)
}

func TestMigration142GlobalDefaultConversion(t *testing.T) {
	ctx := context.Background()
	db := openDisposableDB(t)
	defer db.Close()
	installMigrationsThrough(t, db, lastMigrationBefore142(t))
	legacy := legacyDefaultProfile142(t)
	var initialSectionVersion int64
	if err := db.QueryRowContext(ctx, `SELECT version FROM global_config_sections WHERE section_name='replacement'`).Scan(&initialSectionVersion); err != nil {
		t.Fatal(err)
	}
	var same bool
	if err := db.QueryRowContext(ctx, `SELECT value->'quality_profile'=$1::jsonb FROM global_config_sections WHERE section_name='replacement'`, legacy).Scan(&same); err != nil {
		t.Fatal(err)
	}
	if !same {
		t.Fatal("test precondition: migration 141 must leave the exact legacy built-in default")
	}
	if err := (Runner{DB: db, Dir: migrationsDir(t)}).Apply(ctx); err != nil {
		t.Fatalf("apply migration 142: %v", err)
	}
	var got []byte
	if err := db.QueryRowContext(ctx, `SELECT value->'quality_profile' FROM global_config_sections WHERE section_name='replacement'`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	parsed, err := qualityprofile.Parse(got)
	if err != nil {
		t.Fatalf("converted default is invalid: %v", err)
	}
	want, _ := json.Marshal(qualityprofile.Default())
	actual, _ := json.Marshal(parsed)
	if string(actual) != string(want) {
		t.Fatalf("converted default = %s, want %s", actual, want)
	}
	var gotSectionVersion int64
	if err := db.QueryRowContext(ctx, `SELECT version FROM global_config_sections WHERE section_name='replacement'`).Scan(&gotSectionVersion); err != nil {
		t.Fatal(err)
	}
	if gotSectionVersion != initialSectionVersion+1 {
		t.Fatalf("Global section version = %d, want %d", gotSectionVersion, initialSectionVersion+1)
	}
}

func TestMigration142RejectsNonEquivalentProfiles(t *testing.T) {
	for _, scope := range []string{"global", "instance"} {
		t.Run(scope, func(t *testing.T) {
			ctx := context.Background()
			db := openDisposableDB(t)
			defer db.Close()
			installMigrationsThrough(t, db, lastMigrationBefore142(t))
			custom := []byte(`{"version":"profile-v1","items":[{"key":"rt.resolution","ranks":[["1080p"]]}]}`)
			if scope == "global" {
				if _, err := db.ExecContext(ctx, `UPDATE global_config_sections SET value=jsonb_set(value,'{quality_profile}',$1::jsonb) WHERE section_name='replacement'`, custom); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := db.ExecContext(ctx, `INSERT INTO managed_anime(id,title) VALUES ('anime-142','Show')`); err != nil {
					t.Fatal(err)
				}
				if _, err := db.ExecContext(ctx, `INSERT INTO anime_business_configs(managed_anime_id,admission_mode,quality_profile,replacement_source_default_version,replacement_version) VALUES ('anime-142','single_copy',$1::jsonb,1,1)`, custom); err != nil {
					t.Fatal(err)
				}
			}
			err := (Runner{DB: db, Dir: migrationsDir(t)}).Apply(ctx)
			if err == nil || !strings.Contains(err.Error(), "FACT_SOURCE_") {
				t.Fatalf("%s legacy profile upgrade error = %v", scope, err)
			}
			var version string
			query := `SELECT value->'quality_profile'->>'version' FROM global_config_sections WHERE section_name='replacement'`
			if scope == "instance" {
				query = `SELECT quality_profile->>'version' FROM anime_business_configs WHERE managed_anime_id='anime-142'`
			}
			if err := db.QueryRowContext(ctx, query).Scan(&version); err != nil {
				t.Fatal(err)
			}
			if version != "profile-v1" {
				t.Fatalf("failed upgrade rewrote %s profile to %s", scope, version)
			}
		})
	}
}

func TestMigration142RejectsRetiredTemplateReferences(t *testing.T) {
	for _, template := range []string{
		`${chain(' ', media.resolution)}`,
		`${ep.kind}`,
		`${media.resolution}`,
		`${res}`,
		`${release.langs}`,
	} {
		t.Run(template, func(t *testing.T) {
			ctx := context.Background()
			db := openDisposableDB(t)
			defer db.Close()
			installMigrationsThrough(t, db, lastMigrationBefore142(t))
			library := mustJSON(t, map[string]any{
				"default_key": "legacy",
				"templates": []any{map[string]any{
					"key": "legacy", "name": "Legacy",
					"rules": map[string]any{"tv": map[string]any{"directory_template": template}},
				}},
			})
			if _, err := db.ExecContext(ctx, `UPDATE naming_template_library SET value=$1::jsonb`, library); err != nil {
				t.Fatal(err)
			}
			err := (Runner{DB: db, Dir: migrationsDir(t)}).Apply(ctx)
			if err == nil || !strings.Contains(err.Error(), "FACT_SOURCE_RETIRED_TEMPLATE") {
				t.Fatalf("retired template upgrade error = %v", err)
			}
			var preserved bool
			if err := db.QueryRowContext(ctx, `SELECT value=$1::jsonb FROM naming_template_library`, library).Scan(&preserved); err != nil {
				t.Fatal(err)
			}
			if !preserved {
				t.Fatal("failed upgrade rewrote the original template")
			}
		})
	}
}

func lastMigrationBefore142(t *testing.T) string {
	t.Helper()
	files := shippedMigrationFiles(t)
	for index, name := range files {
		if strings.HasPrefix(name, "142_") {
			if index == 0 {
				t.Fatal("migration 142 has no predecessor")
			}
			return files[index-1]
		}
	}
	t.Fatal("migration 142 not found")
	return ""
}
