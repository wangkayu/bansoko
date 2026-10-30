package application

import (
	"database/sql"
	"errors"
	"strings"
	"testing"

	"bansoko/internal/apperrors"
)

// TestListRecordsRejectsInvalidFilters: every new filter is validated before
// any SQL is built (the nil DB is never reached).
func TestListRecordsRejectsInvalidFilters(t *testing.T) {
	svc, err := NewService(&sql.DB{})
	if err != nil {
		t.Fatal(err)
	}
	text := func(value string) *string { return &value }
	kinds := func(values ...string) *[]string { return &values }
	cases := []struct {
		name  string
		query RecordQuery
		code  string
	}{
		{"unknown state", RecordQuery{State: text("partial")}, "INVALID_STATE"},
		{"retired outcome value", RecordQuery{State: text("unknown")}, "INVALID_STATE"},
		{"search too long", RecordQuery{Q: text(strings.Repeat("番", maxRecordSearchRunes+1))}, "INVALID_QUERY"},
		{"search with NUL", RecordQuery{Q: text("a\x00b")}, "INVALID_QUERY"},
		{"search not UTF-8", RecordQuery{Q: text("a\xffb")}, "INVALID_QUERY"},
		{"unknown entry kind", RecordQuery{EntryKind: kinds("acquisition_settled", "bogus")}, "INVALID_ENTRY_KIND"},
		{"retired entry kind", RecordQuery{EntryKind: kinds("rss_refresh_failed")}, "INVALID_ENTRY_KIND"},
		{"unknown event view", RecordQuery{EventView: text("noise")}, "INVALID_EVENT_VIEW"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.ListRecords(t.Context(), tc.query)
			var bad *apperrors.BadRequest
			if !errors.As(err, &bad) {
				t.Fatalf("expected *apperrors.BadRequest, got %T: %v", err, err)
			}
			if bad.Code != tc.code {
				t.Fatalf("code: got %q, want %q", bad.Code, tc.code)
			}
		})
	}
}

// TestRecordEventViewParam: nil is the API default (all); the closed set is
// validated before any SQL is built.
func TestRecordEventViewParam(t *testing.T) {
	text := func(value string) *string { return &value }
	if got, err := recordEventViewParam(nil); err != nil || got != RecordEventViewAll {
		t.Fatalf("default view: got %q, %v", got, err)
	}
	for _, view := range []string{RecordEventViewPrimary, RecordEventViewAll} {
		if got, err := recordEventViewParam(text(view)); err != nil || got != view {
			t.Fatalf("view %q: got %q, %v", view, got, err)
		}
	}
	if _, err := recordEventViewParam(text("primary-ish")); err == nil {
		t.Fatal("an unknown view must not pass")
	}
}

// TestPrimaryEventViewPredicateShape pins the exact exclusion: only
// release_processed info entries with one of the two reasons, with COALESCE so
// a record without a reason stays visible.
func TestPrimaryEventViewPredicateShape(t *testing.T) {
	want := "NOT (e.kind = 'release_processed' AND e.outcome = 'info' AND COALESCE(e.reason_code, '') IN ('awaiting_competition', 'no_placeable_files'))"
	if got := primaryEventViewPredicate(); got != want {
		t.Fatalf("primary view predicate:\n got %s\nwant %s", got, want)
	}
}

func TestRecordSearchParam(t *testing.T) {
	text := func(value string) *string { return &value }
	cases := []struct {
		in   *string
		want string
	}{
		{nil, ""},
		{text("   "), ""},
		{text(" 柯南 "), "%柯南%"},
		{text(`100%_a\b`), `%100\%\_a\\b%`},
		{text(strings.Repeat("番", maxRecordSearchRunes)), "%" + strings.Repeat("番", maxRecordSearchRunes) + "%"},
	}
	for _, tc := range cases {
		got, err := recordSearchParam(tc.in)
		if err != nil {
			t.Fatalf("recordSearchParam: %v", err)
		}
		if got != tc.want {
			t.Fatalf("pattern: got %q, want %q", got, tc.want)
		}
	}
}

func TestRecordEntryKindsParamSortsAndDedupes(t *testing.T) {
	values := []string{"placement_changed", "acquisition_settled", "placement_changed"}
	got, err := recordEntryKindsParam(&values)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "acquisition_settled,placement_changed" {
		t.Fatalf("entry kinds: %v", got)
	}
	empty := []string{}
	if got, err := recordEntryKindsParam(&empty); err != nil || got != nil {
		t.Fatalf("an empty entry kind list is no filter: %v %v", got, err)
	}
}

// TestBusinessStatePredicateBadgeShape: the badge query (failed settlements
// only) reduces to kind and outcome equalities, with no run subquery, so the
// (kind, outcome, occurred_at, id) index serves it.
func TestBusinessStatePredicateBadgeShape(t *testing.T) {
	got := businessStatePredicate(classifyBusinessKinds([]string{settledKind}), RecordStateFailure)
	if want := "((e.kind = 'acquisition_settled' AND e.outcome = 'failure'))"; got != want {
		t.Fatalf("badge predicate:\n got %s\nwant %s", got, want)
	}
	if got := businessStatePredicate(classifyBusinessKinds([]string{settledKind}), RecordStateInProgress); got != "FALSE" {
		t.Fatalf("a settlement is never in progress: %s", got)
	}
}

func TestBusinessStatePredicateRunSteps(t *testing.T) {
	all := businessStatePredicate(classifyBusinessKinds(nil), RecordStateInProgress)
	if !strings.Contains(all, "subscription_log_entries s WHERE s.release_id = e.release_id AND s.kind = 'acquisition_settled'") {
		t.Fatalf("run steps must read their settlement: %s", all)
	}
	if strings.Contains(all, "e.kind NOT IN") {
		t.Fatalf("no entry outside a run is in progress: %s", all)
	}
	others := businessStatePredicate(classifyBusinessKinds([]string{"rss_new_content", "release_processed"}), RecordStateInfo)
	if want := "((e.kind IN ('rss_new_content', 'release_processed') AND e.outcome = 'info'))"; others != want {
		t.Fatalf("entries outside runs use their outcome:\n got %s\nwant %s", others, want)
	}
}

func TestSourceStatePredicates(t *testing.T) {
	if got := pluginStates.predicate(RecordStateFailure); got != "e.state IN ('failed', 'unknown')" {
		t.Fatalf("an unknown delivery is a failure: %s", got)
	}
	if got := routineStates.predicate(RecordStateInProgress); got != "FALSE" {
		t.Fatalf("routine records are never in progress: %s", got)
	}
	if got := automationStates.predicate(RecordStateFailure); got != "e.state IN ('partial', 'failed')" {
		t.Fatalf("automation failure: %s", got)
	}
}

func TestSQLListQuotes(t *testing.T) {
	if got := sqlList([]string{"a", "b'c"}); got != "('a', 'b''c')" {
		t.Fatalf("sqlList: %s", got)
	}
}
