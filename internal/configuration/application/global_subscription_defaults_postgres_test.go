package application

import (
	"context"
	"testing"

	animedomain "bansoko/internal/anime/domain"
	configurationdomain "bansoko/internal/configuration/domain"
)

// subscription_defaults is a one-time-input Global section: it lists as the
// code default until saved, saves under the usual version CAS, refuses partial
// or unknown members, and never joins the business snapshot or its digest.
// ReadGlobalSubscriptionDefaults is the single reader a new draft's preset
// carries.
func TestGlobalSubscriptionDefaultsSection(t *testing.T) {
	db := openConnectionTestDB(t)
	ctx := context.Background()
	t.Cleanup(func() {
		_, _ = db.ExecContext(ctx, `DELETE FROM global_config_sections WHERE section_name = $1`, animedomain.SectionSubscriptionDefaults)
	})
	svc := NewService(db, newConnectionTestSecretStore(t, db))

	find := func() configurationdomain.GlobalSectionView {
		t.Helper()
		views, err := svc.ListGlobalSections(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, view := range views {
			if view.SectionName == animedomain.SectionSubscriptionDefaults {
				return view
			}
		}
		t.Fatal("subscription_defaults missing from the global sections")
		return configurationdomain.GlobalSectionView{}
	}
	if view := find(); view.Saved || view.Version != 1 || view.SchemaVersion != animedomain.SubscriptionDefaultsSchemaVersion ||
		view.Value["ignore_multi_video_releases"] != false {
		t.Fatalf("default view = %+v", view)
	}
	defaults, err := svc.ReadGlobalSubscriptionDefaults(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if defaults.IgnoreMultiVideoReleases {
		t.Fatalf("code default read back true: %+v", defaults)
	}

	saved, err := svc.PutGlobalSection(ctx, animedomain.SectionSubscriptionDefaults, 1, configurationdomain.GlobalSectionInput{
		SchemaVersion: animedomain.SubscriptionDefaultsSchemaVersion,
		Value:         map[string]interface{}{"ignore_multi_video_releases": true},
	})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if saved.Version != 1 || !saved.Saved || saved.Value["ignore_multi_video_releases"] != true {
		t.Fatalf("saved view = %+v", saved)
	}
	if view := find(); !view.Saved || view.Version != 1 || view.Value["ignore_multi_video_releases"] != true {
		t.Fatalf("listed view = %+v", view)
	}
	defaults, err = svc.ReadGlobalSubscriptionDefaults(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !defaults.IgnoreMultiVideoReleases {
		t.Fatalf("saved value read back false: %+v", defaults)
	}

	for _, bad := range []map[string]interface{}{
		{},
		{"ignore_multi_video_releases": "true"},
		{"ignore_multi_video_releases": true, "extra": 1},
	} {
		if _, err := svc.PutGlobalSection(ctx, animedomain.SectionSubscriptionDefaults, saved.Version, configurationdomain.GlobalSectionInput{
			SchemaVersion: animedomain.SubscriptionDefaultsSchemaVersion,
			Value:         bad,
		}); err == nil {
			t.Fatalf("value accepted: %v", bad)
		}
	}

	snapshot, err := svc.ReadBusinessPreset(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := snapshot.Sections[animedomain.SectionSubscriptionDefaults]; ok {
		t.Fatal("subscription_defaults leaked into the business snapshot")
	}
}
