package persistence

import (
	"context"
	"testing"

	catalogdomain "bansoko/internal/catalog/domain"
	"bansoko/internal/testutil/disposabledb"
)

// Each test resets the disposable schema first, so the dictionary starts empty
// and fixed references stay stable and readable: a Mikan key is a positive
// integer and an AniBT key is a slug, exactly as the two providers publish them.
func TestGroupDictionaryEntriesRoundTripAndStayProviderScoped(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	repo := NewGroupDictionaryRepository(db)
	ctx := context.Background()

	inserted, err := repo.InsertGroupDictionaryEntry(ctx, "mikan", "990001", "SampleGroup", true)
	if err != nil {
		t.Fatal(err)
	}
	if inserted.Provider != "mikan" || inserted.GroupRef != "990001" || inserted.DisplayName != "SampleGroup" ||
		!inserted.Available || inserted.Source != catalogdomain.GroupDictionarySourceUser || inserted.AddedAt.IsZero() {
		t.Fatalf("stored entry drifted: %+v", inserted)
	}

	// The same reference under a second provider is a different entry: a Mikan
	// id and an AniBT slug are separate namespaces.
	if _, err := repo.InsertGroupDictionaryEntry(ctx, "anibt", "990001", "AniBT Sub", true); err != nil {
		t.Fatal(err)
	}
	listed, err := repo.ListGroupDictionaryEntries(ctx, "mikan")
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].GroupRef != "990001" {
		t.Fatalf("provider-scoped listing = %+v", listed)
	}

	found, ok, err := repo.FindGroupDictionaryEntry(ctx, "mikan", "990001")
	if err != nil || !ok || found.DisplayName != "SampleGroup" {
		t.Fatalf("exact find = %+v ok=%v err=%v", found, ok, err)
	}
	if _, ok, err := repo.FindGroupDictionaryEntry(ctx, "mikan", "990002"); err != nil || ok {
		t.Fatalf("absent find ok=%v err=%v", ok, err)
	}
	// A prefix is not a key: the repository resolves exact references only.
	if _, ok, err := repo.FindGroupDictionaryEntry(ctx, "mikan", "99000"); err != nil || ok {
		t.Fatalf("partial reference matched: ok=%v err=%v", ok, err)
	}
}

// A repeated retrieval of the same group must leave exactly one row and return
// the row the first write stored, which is what makes the retrieval endpoint
// repeatable.
func TestInsertGroupDictionaryEntryIsIdempotent(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	repo := NewGroupDictionaryRepository(db)
	ctx := context.Background()

	first, err := repo.InsertGroupDictionaryEntry(ctx, "anibt", "new-sub", "New Sub", true)
	if err != nil {
		t.Fatal(err)
	}
	repeat, err := repo.InsertGroupDictionaryEntry(ctx, "anibt", "new-sub", "New Sub (renamed upstream)", false)
	if err != nil {
		t.Fatalf("repeated insert was not idempotent: %v", err)
	}
	if repeat.AddedAt != first.AddedAt || repeat.DisplayName != first.DisplayName || repeat.Available != first.Available {
		t.Fatalf("a repeated insert churned the row: first=%+v repeat=%+v", first, repeat)
	}
	listed, err := repo.ListGroupDictionaryEntries(ctx, "anibt")
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 {
		t.Fatalf("repeated insert left %d rows", len(listed))
	}
}

func TestDeleteGroupDictionaryEntryReportsWhetherARowWasThere(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	repo := NewGroupDictionaryRepository(db)
	ctx := context.Background()
	if _, err := repo.InsertGroupDictionaryEntry(ctx, "mikan", "990068", "Retired Group", false); err != nil {
		t.Fatal(err)
	}

	deleted, err := repo.DeleteGroupDictionaryEntry(ctx, "mikan", "990068")
	if err != nil || !deleted {
		t.Fatalf("delete = %v err=%v", deleted, err)
	}
	if deleted, err := repo.DeleteGroupDictionaryEntry(ctx, "mikan", "990068"); err != nil || deleted {
		t.Fatalf("second delete reported a row: %v err=%v", deleted, err)
	}
	// A delete is scoped by provider, so a sibling provider's entry survives.
	if _, err := repo.InsertGroupDictionaryEntry(ctx, "anibt", "990068", "AniBT Sub", true); err != nil {
		t.Fatal(err)
	}
	if deleted, err := repo.DeleteGroupDictionaryEntry(ctx, "mikan", "990068"); err != nil || deleted {
		t.Fatalf("cross-provider delete = %v err=%v", deleted, err)
	}
	if _, ok, err := repo.FindGroupDictionaryEntry(ctx, "anibt", "990068"); err != nil || !ok {
		t.Fatalf("the sibling provider lost its entry: ok=%v err=%v", ok, err)
	}
}

// The table constrains what can enter the dictionary, so a broken write fails
// at the database rather than becoming a row later readers must defend against.
func TestGroupDictionaryTableConstraintsRefuseBrokenRows(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	ctx := context.Background()
	for name, statement := range map[string]string{
		"unknown provider": `INSERT INTO provider_group_dictionary(id, provider, provider_group_ref, display_name, source)
			VALUES ('sr-1-dict-bad-provider', 'bgm', '990010', 'x', 'user')`,
		"empty reference": `INSERT INTO provider_group_dictionary(id, provider, provider_group_ref, display_name, source)
			VALUES ('sr-1-dict-empty-ref', 'mikan', '', 'x', 'user')`,
		"unknown source": `INSERT INTO provider_group_dictionary(id, provider, provider_group_ref, display_name, source)
			VALUES ('sr-1-dict-bad-source', 'mikan', '990011', 'x', 'imported')`,
		"over-long name": `INSERT INTO provider_group_dictionary(id, provider, provider_group_ref, display_name, source)
			VALUES ('sr-1-dict-long-name', 'mikan', '990012', repeat('x', 257), 'user')`,
		"zero version": `INSERT INTO provider_group_dictionary(id, provider, provider_group_ref, display_name, source, version)
			VALUES ('sr-1-dict-zero-version', 'mikan', '990013', 'x', 'user', 0)`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := db.ExecContext(ctx, statement); err == nil {
				t.Fatalf("the dictionary accepted a broken row (%s)", name)
			}
		})
	}
}

// The collected layer is published with the release and is not a table: the
// dictionary's only rows are the user's own additions, so a reader can tell the
// two layers apart without a join.
func TestGroupDictionaryHoldsOnlyUserRows(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	repo := NewGroupDictionaryRepository(db)
	ctx := context.Background()
	if _, err := repo.InsertGroupDictionaryEntry(ctx, "mikan", "990020", "SampleGroup", true); err != nil {
		t.Fatal(err)
	}
	var sources string
	if err := db.QueryRowContext(ctx, `SELECT string_agg(DISTINCT source, ',' ORDER BY source) FROM provider_group_dictionary`).Scan(&sources); err != nil {
		t.Fatal(err)
	}
	if sources != string(catalogdomain.GroupDictionarySourceUser) {
		t.Fatalf("the dictionary holds %q rows", sources)
	}
}
