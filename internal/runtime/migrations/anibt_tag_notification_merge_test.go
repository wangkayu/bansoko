package migrations

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Migration 040 split the AniBT customTag into the codec, bitdepth, audio,
// source, platform and subformat sub-dimensions and folded them back into
// `anibt.tag` before reading the tag dimension, because
// templatefacts.AniBTTagValues still renders every customTag value there.
// Migration 051 re-created notification_value_facts from the 001 body and lost
// the fold; 055 and 066 carried the fold-less call forward, so a release whose
// customTag values were all classified (the persisted source_facts shape)
// projected `anibt.tag` = null against the Go payload's
// "10bit+AAC+HEVC+WEBRip". The finish gate compares the two and aborted the
// 入库 transaction with FILE_NOTIFICATION_PROOF_MISMATCH. Migration 070 restores
// the fold.
//
// The tripwire is the LATEST migration that projects `anibt.tag`: re-creating
// notification_value_facts again may change anything else, but the tag field
// has to keep folding the sub-dimensions, or the same production failure
// returns silently on the next rewrite of that function.
func TestAniBTTagNotificationFoldSurvivesTheLatestProjector(t *testing.T) {
	files := shippedMigrationFiles(t)
	sort.Strings(files)
	latest := ""
	var line string
	for _, name := range files {
		raw, err := os.ReadFile(filepath.Join(migrationsDir(t), name))
		if err != nil {
			t.Fatal(err)
		}
		for _, candidate := range strings.Split(string(raw), "\n") {
			if strings.Contains(candidate, "'anibt.tag',") {
				latest, line = name, candidate
			}
		}
	}
	if latest == "" {
		t.Fatal("no migration projects anibt.tag")
	}
	if !strings.Contains(line, "public.notification_anibt_custom_tags(") {
		t.Fatalf("%s projects anibt.tag without folding the AniBT sub-dimensions: %s", latest, strings.TrimSpace(line))
	}
	if !strings.Contains(strings.ReplaceAll(line, " ", ""), "notification_tag_values(public.notification_anibt_custom_tags(") {
		t.Fatalf("%s reads the tag dimension from something other than the fold: %s", latest, strings.TrimSpace(line))
	}

	// The helper is shipped by a migration, and the fold it performs is the one
	// the reader applies: a customTag sub-dimension becomes `tag:<value>` in
	// place, an unclassified or foreign dimension is left alone.
	merged := false
	for _, name := range files {
		raw, err := os.ReadFile(filepath.Join(migrationsDir(t), name))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), "FUNCTION notification_anibt_custom_tags(") {
			continue
		}
		merged = true
		requireFragments(t, string(raw), name+" AniBT custom tag fold",
			"'codec','bitdepth','audio','source','platform','subformat'",
			"THEN 'tag:'||substr(t.tag,strpos(t.tag,':')+1)",
			"ORDER BY t.ord")
	}
	if !merged {
		t.Fatal("no migration defines notification_anibt_custom_tags")
	}
}

// The 070 self-check runs inside the migration, so a database that applies it
// proves the projection end to end. This keeps the shape that makes that
// possible: the DO block must call the projector, and it must assert the
// documented value rather than merely that something was returned.
func TestAniBTTagNotificationMergeSelfCheck(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(migrationsDir(t), "070_anibt_tag_notification_merge.sql"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	requireFragments(t, text, "migration 070",
		"CREATE OR REPLACE FUNCTION notification_value_facts(",
		"CREATE OR REPLACE FUNCTION notification_anibt_custom_tags(",
		"DO $$",
		"projected := public.notification_value_facts('{}'::jsonb,",
		"'10bit+AAC+HEVC+WEBRip'",
		"RAISE EXCEPTION 'migration 070 self-check failed",
	)
	// 070 replaces the projector, so it must keep every other field 066 shipped;
	// dropping one here would be as silent as dropping the fold was.
	for _, field := range []string{
		"'rt.title'", "'rt.subgroup'", "'rt.version'", "'rt.resolution'", "'rt.source'", "'rt.codec'",
		"'rt.sub_lang'", "'rt.sub_flag'", "'rt.uncensored'",
		"'anibt.resolution'", "'anibt.subtitle'", "'anibt.language'", "'anibt.format'", "'anibt.tag'",
	} {
		if !strings.Contains(text, field) {
			t.Fatalf("migration 070 no longer projects %s", field)
		}
	}
}
