package application

import (
	"context"
	"encoding/json"
	"testing"

	animedomain "bansoko/internal/anime/domain"
	configurationdomain "bansoko/internal/configuration/domain"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// TestPutGlobalSectionRoundTrip pins the JSONB scan contract: the read back
// after a successful write must scan JSONB into []byte before Unmarshal.
// Scanning directly into a generated map type makes pgx return []uint8 and
// the request fails with an "unsupported Scan" 500 even though the row was
// written. Covers the first write (INSERT, version=1) and a second write
// (UPDATE + CAS, version=2).

func TestPutGlobalSectionRoundTrip(t *testing.T) {
	db := openConnectionTestDB(t)
	ctx := context.Background()
	t.Cleanup(func() {
		_, _ = db.ExecContext(ctx, `DELETE FROM global_config_sections WHERE section_name = $1`, animedomain.SectionSubgroups)
	})

	svc := NewService(db, newConnectionTestSecretStore(t, db))
	value := map[string]interface{}{
		"max_rss": 3,
		"entries": []interface{}{},
		"provider_defaults": map[string]interface{}{
			"mikan": map[string]interface{}{"mode": "blacklist", "score": 50, "groups": []interface{}{"58"}},
			"anibt": map[string]interface{}{"mode": "whitelist", "score": 70, "groups": []interface{}{}},
		},
	}

	listedSaved := func() (bool, int64) {
		t.Helper()
		views, err := svc.ListGlobalSections(ctx)
		if err != nil {
			t.Fatalf("list global sections: %v", err)
		}
		for _, view := range views {
			if view.SectionName == animedomain.SectionSubgroups {
				return view.Saved, view.Version
			}
		}
		t.Fatalf("subgroups missing from list")
		return false, 0
	}
	if saved, _ := listedSaved(); saved {
		t.Fatalf("code default listed as saved")
	}

	// First write: no existing row, INSERT version=1, the returned value must be
	// directly usable.
	first, err := svc.PutGlobalSection(ctx, animedomain.SectionSubgroups, 1, configurationdomain.GlobalSectionInput{
		SchemaVersion: animedomain.SectionSchemaVersions[animedomain.SectionSubgroups],
		Value:         value,
	})
	if err != nil {
		t.Fatalf("first put: %v", err)
	}
	if first.Version != 1 {
		t.Fatalf("first version = %d, want 1", first.Version)
	}
	// A first save is stored at version 1, so only Saved tells it apart from
	// the code default (the settings badge relies on this).
	if saved, version := listedSaved(); !first.Saved || !saved || version != 1 {
		t.Fatalf("first save: put saved=%v, listed saved=%v version=%d", first.Saved, saved, version)
	}
	groups, ok := first.Value["provider_defaults"].(map[string]interface{})["mikan"].(map[string]interface{})["groups"].([]interface{})
	if !ok || len(groups) != 1 || groups[0] != "58" {
		raw, _ := json.Marshal(first.Value)
		t.Fatalf("first value round-trip broken: %s", raw)
	}

	// Second write: the CAS passes, version increments to 2, and the read back
	// is still complete.
	value["max_rss"] = 5
	second, err := svc.PutGlobalSection(ctx, animedomain.SectionSubgroups, first.Version, configurationdomain.GlobalSectionInput{
		SchemaVersion: animedomain.SectionSchemaVersions[animedomain.SectionSubgroups],
		Value:         value,
	})
	if err != nil {
		t.Fatalf("second put: %v", err)
	}
	if second.Version != 2 {
		t.Fatalf("second version = %d, want 2", second.Version)
	}
	if second.Value["max_rss"] != float64(5) {
		t.Fatalf("second max_rss = %v, want 5", second.Value["max_rss"])
	}
}

// download_completion is a Global rules-owner section: it lists as the code
// default (seed) until saved, saves under the usual version CAS, rejects values
// outside the four actions, and never joins the business snapshot's read.
func TestGlobalDownloadCompletionSection(t *testing.T) {
	db := openConnectionTestDB(t)
	ctx := context.Background()
	svc := NewService(db, newConnectionTestSecretStore(t, db))

	find := func() configurationdomain.GlobalSectionView {
		t.Helper()
		views, err := svc.ListGlobalSections(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, view := range views {
			if view.SectionName == animedomain.SectionDownloadCompletion {
				return view
			}
		}
		t.Fatal("download_completion missing from the global sections")
		return configurationdomain.GlobalSectionView{}
	}
	if view := find(); view.Saved || view.Value["action"] != "seed" || view.SchemaVersion != "download-completion-v2" {
		t.Fatalf("default view = %+v", view)
	}

	for _, bad := range []map[string]interface{}{{"action": "wipe"}, {}, {"action": "seed", "extra": true},
		{"action": "pause", "seed_limits": map[string]interface{}{"ratio": 1, "at_limit": "stop"}},
		{"action": "seed", "seed_limits": map[string]interface{}{"at_limit": "stop"}}} {
		if _, err := svc.PutGlobalSection(ctx, animedomain.SectionDownloadCompletion, 1, configurationdomain.GlobalSectionInput{SchemaVersion: "download-completion-v2", Value: bad}); err == nil {
			t.Fatalf("invalid value %v was saved", bad)
		}
	}
	if _, err := svc.PutGlobalSection(ctx, animedomain.SectionDownloadCompletion, 1, configurationdomain.GlobalSectionInput{SchemaVersion: "wrong", Value: map[string]interface{}{"action": "seed"}}); err == nil {
		t.Fatal("a wrong schema version was saved")
	}
	saved, err := svc.PutGlobalSection(ctx, animedomain.SectionDownloadCompletion, 1, configurationdomain.GlobalSectionInput{SchemaVersion: "download-completion-v2", Value: map[string]interface{}{"action": "pause"}})
	if err != nil || !saved.Saved || saved.Version != 1 || saved.Value["action"] != "pause" {
		t.Fatalf("first save = %+v %v", saved, err)
	}
	second, err := svc.PutGlobalSection(ctx, animedomain.SectionDownloadCompletion, 1, configurationdomain.GlobalSectionInput{SchemaVersion: "download-completion-v2", Value: map[string]interface{}{"action": "remove"}})
	if err != nil || second.Version != 2 || second.Value["action"] != "remove" {
		t.Fatalf("second save = %+v %v", second, err)
	}
	if _, err := svc.PutGlobalSection(ctx, animedomain.SectionDownloadCompletion, 1, configurationdomain.GlobalSectionInput{SchemaVersion: "download-completion-v2", Value: map[string]interface{}{"action": "remove_with_data"}}); err == nil {
		t.Fatal("a stale expected version was accepted")
	}
	if view := find(); !view.Saved || view.Version != 2 || view.Value["action"] != "remove" {
		t.Fatalf("listed view = %+v", view)
	}
	limited, err := svc.PutGlobalSection(ctx, animedomain.SectionDownloadCompletion, 2, configurationdomain.GlobalSectionInput{SchemaVersion: "download-completion-v2", Value: map[string]interface{}{"action": "seed", "seed_limits": map[string]interface{}{"seeding_time_minutes": 1440, "ratio": 2, "at_limit": "remove_with_data"}}})
	if err != nil || limited.Version != 3 || limited.Value["seed_limits"] == nil {
		t.Fatalf("seed limits save = %+v %v", limited, err)
	}

	// The business snapshot freezes exactly the six sections: the live-only
	// download_completion Global row is not part of it.
	// (The preset reads the active Vars and attr-rule programs a server publishes
	// at startup; the Global write above does not depend on them.)
	seedActivePrograms(t, ctx, svc)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	snapshot, err := ReadBusinessPresetTx(ctx, tx)
	if err != nil {
		t.Fatal(err)
	}
	if _, present := snapshot.Sections[animedomain.SectionDownloadCompletion]; present {
		t.Fatal("download_completion entered the business snapshot")
	}
}
