package application

import (
	"context"
	"testing"

	"bansoko/internal/testutil/animetest"
)

// TestGetAnimeWorkspaceListsTitleSuggestions proves the workspace metadata
// lists the cached titles of the bound works and the provider title, in the
// fixed source order, and omits every empty one.
func TestGetAnimeWorkspaceListsTitleSuggestions(t *testing.T) {
	db := animetest.OpenTestDB(t)
	readmodel, root := readmodelNewTestServices(t, db, &readmodelFakeFetcher{})
	ctx := context.Background()
	subscribe := readmodelSubscribeTestDraft(t, root, "subscribe-key-workspace-title-suggestions")
	animeID := subscribe.ManagedAnime.Id

	suggestions := func() []TitleSuggestion {
		t.Helper()
		workspace, err := readmodel.GetAnimeWorkspaceApplication(ctx, animeID)
		if err != nil {
			t.Fatalf("read workspace: %v", err)
		}
		return workspace.Metadata.TitleSuggestions
	}

	// Nothing is cached yet: only the provider title (the subscription's own title) is known.
	if got := suggestions(); len(got) != 1 || got[0].Source != "provider" || got[0].Text != "Test Show" {
		t.Fatalf("uncached suggestions = %+v, want the provider title alone", got)
	}

	// The bound works' cached titles (the draft binds bgm-123 and tmdb-456 tv); a work that is not bound is ignored.
	for _, row := range []string{
		`('tmdb', 'tv', 'tmdb-456', NULL, '示例中文名', 'Sample Original')`,
		`('bgm', 'subject', 'bgm-123', 'サンプル', '示例 BGM', NULL)`,
		`('tmdb', 'tv', 'tmdb-unbound', NULL, '未绑定', 'Unbound')`,
	} {
		if _, err := db.ExecContext(ctx, `INSERT INTO metadata_work_titles(provider, media_kind, work_id, title, title_cn, original_title) VALUES `+row); err != nil {
			t.Fatal(err)
		}
	}
	want := []TitleSuggestion{
		{Source: "tmdb_cn", Text: "示例中文名"},
		{Source: "tmdb_original", Text: "Sample Original"},
		{Source: "bgm_cn", Text: "示例 BGM"},
		{Source: "bgm", Text: "サンプル"},
		{Source: "provider", Text: "Test Show"},
	}
	got := suggestions()
	if len(got) != len(want) {
		t.Fatalf("suggestions = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("suggestions[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}

	// An empty cached value is omitted, not sent blank.
	if _, err := db.ExecContext(ctx, `UPDATE metadata_work_titles SET original_title = NULL WHERE provider = 'tmdb' AND work_id = 'tmdb-456'`); err != nil {
		t.Fatal(err)
	}
	for _, item := range suggestions() {
		if item.Source == "tmdb_original" || item.Text == "" {
			t.Fatalf("an empty source must be omitted: %+v", item)
		}
	}
}
