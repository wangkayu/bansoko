package application

import (
	"context"
	"testing"

	animedomain "bansoko/internal/anime/domain"
	configurationdomain "bansoko/internal/configuration/domain"
)

// download_timeouts is a Global-only section: it lists as the code default
// (6 h / 7 days) until saved, saves under the usual version CAS, refuses values
// outside the bounds or with stall > wait, and never joins the business
// snapshot.
func TestGlobalDownloadTimeoutsSection(t *testing.T) {
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
			if view.SectionName == animedomain.SectionDownloadTimeouts {
				return view
			}
		}
		t.Fatal("download_timeouts missing from the global sections")
		return configurationdomain.GlobalSectionView{}
	}
	if view := find(); view.Saved || view.Version != 1 || view.SchemaVersion != animedomain.DownloadTimeoutsSchemaVersion ||
		view.Value["stall_seconds"] != float64(21600) || view.Value["download_wait_seconds"] != float64(604800) {
		t.Fatalf("default view = %+v", view)
	}
	input := func(stall, wait int64) configurationdomain.GlobalSectionInput {
		return configurationdomain.GlobalSectionInput{SchemaVersion: animedomain.DownloadTimeoutsSchemaVersion, Value: map[string]interface{}{"stall_seconds": stall, "download_wait_seconds": wait}}
	}
	for _, bad := range []configurationdomain.GlobalSectionInput{input(599, 86400), input(86400, 3600), input(3600, 30*86400+1), input(7*86400+1, 30*86400)} {
		if _, err := svc.PutGlobalSection(ctx, animedomain.SectionDownloadTimeouts, 1, bad); err == nil {
			t.Fatalf("invalid value %v was saved", bad.Value)
		}
	}
	saved, err := svc.PutGlobalSection(ctx, animedomain.SectionDownloadTimeouts, 1, input(3600, 2*86400))
	if err != nil || !saved.Saved || saved.Version != 1 || saved.Value["stall_seconds"] != float64(3600) {
		t.Fatalf("first save = %+v %v", saved, err)
	}
	if _, err := svc.PutGlobalSection(ctx, animedomain.SectionDownloadTimeouts, 2, input(7200, 86400)); err == nil {
		t.Fatal("a wrong expected version was accepted")
	}
	if view := find(); !view.Saved || view.Version != 1 || view.Value["download_wait_seconds"] != float64(2*86400) {
		t.Fatalf("listed view = %+v", view)
	}

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
	if _, present := snapshot.Sections[animedomain.SectionDownloadTimeouts]; present {
		t.Fatal("download_timeouts entered the business snapshot")
	}
}
