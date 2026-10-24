package persistence

// The metadata_work_titles upsert records the TMDB work summary beside the
// titles: a match/search/re-pull that observes a bound work (re)records its
// overview, a work without an observable overview stores none (NULL, the
// "never matched reads empty" side) and a BGM row never stores one.
import (
	"context"
	"database/sql"
	"testing"

	identitymetadatadomain "bansoko/internal/identitymetadata/domain"
	"bansoko/internal/testutil/disposabledb"
)

func readOverview(t *testing.T, db *sql.DB, provider, kind, id string) sql.NullString {
	t.Helper()
	var overview sql.NullString
	if err := db.QueryRow(`SELECT overview FROM metadata_work_titles WHERE provider=$1 AND media_kind=$2 AND work_id=$3`, provider, kind, id).Scan(&overview); err != nil {
		t.Fatalf("read %s %s %s: %v", provider, kind, id, err)
	}
	return overview
}

func TestUpsertWorkTitlesRecordsAndRefreshesOverview(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	ctx := context.Background()

	if err := UpsertWorkTitles(ctx, db, []identitymetadatadomain.WorkTitles{{
		Provider: "tmdb", MediaKind: "tv", WorkID: "42",
		TitleCN: "作品", OriginalTitle: "Original Show", Overview: "  作品简介，记录于匹配后。  ", Year: identitymetadatadomain.NormalizeYear(yearPtr(2024)),
	}}); err != nil {
		t.Fatalf("record tmdb work titles: %v", err)
	}
	if got := readOverview(t, db, "tmdb", "tv", "42"); got.String != "作品简介，记录于匹配后。" || !got.Valid {
		t.Fatalf("overview = %q valid=%v, want the trimmed value", got.String, got.Valid)
	}

	// A later matching run updates the record (accumulate-on-observation).
	if err := UpsertWorkTitles(ctx, db, []identitymetadatadomain.WorkTitles{{
		Provider: "tmdb", MediaKind: "tv", WorkID: "42",
		TitleCN: "作品 II", OriginalTitle: "Original Show II", Overview: "更新后的简介", Year: identitymetadatadomain.NormalizeYear(yearPtr(2025)),
	}}); err != nil {
		t.Fatalf("re-match tmdb work titles: %v", err)
	}
	if got := readOverview(t, db, "tmdb", "tv", "42"); got.String != "更新后的简介" {
		t.Fatalf("overview after re-match = %q, want the updated value", got.String)
	}

	// An observation without an overview stores none: the row keeps title
	// value but overview NULL, and nothing is backfilled.
	if err := UpsertWorkTitles(ctx, db, []identitymetadatadomain.WorkTitles{{
		Provider: "tmdb", MediaKind: "movie", WorkID: "7", TitleCN: "无简介",
	}}); err != nil {
		t.Fatalf("record overview-less work: %v", err)
	}
	if got := readOverview(t, db, "tmdb", "movie", "7"); got.Valid {
		t.Fatalf("overview of an unobserved work = %q, want NULL", got.String)
	}

	// BGM has no overview surface: the writer stores NULL.
	if err := UpsertWorkTitles(ctx, db, []identitymetadatadomain.WorkTitles{{
		Provider: "bgm", MediaKind: "subject", WorkID: "10",
		Title: "ショー", TitleCN: "秀", Overview: "must not be stored",
	}}); err != nil {
		t.Fatalf("record bgm work titles: %v", err)
	}
	if got := readOverview(t, db, "bgm", "subject", "10"); got.Valid {
		t.Fatalf("bgm overview = %q, want NULL", got.String)
	}

	// An oversized overview degrades to NULL like an oversized title, and the
	// work with only that value still stores its titles.
	big := make([]byte, maxWorkOverviewBytes+1)
	for i := range big {
		big[i] = 'x'
	}
	if err := UpsertWorkTitles(ctx, db, []identitymetadatadomain.WorkTitles{{
		Provider: "tmdb", MediaKind: "tv", WorkID: "43", TitleCN: "长简介", Overview: string(big),
	}}); err != nil {
		t.Fatalf("record oversized overview: %v", err)
	}
	if got := readOverview(t, db, "tmdb", "tv", "43"); got.Valid {
		t.Fatalf("oversized overview = %q, want NULL", got.String)
	}
}

func yearPtr(value int) *int { return &value }
