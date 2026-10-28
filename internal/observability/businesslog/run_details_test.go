package businesslog

import (
	"database/sql"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestRunContextFreezesGroupTitleAndPlanEpisodes(t *testing.T) {
	got := RunContext(" NEST ", "[NEST] Title - 02 [1080p].mkv", sql.NullString{String: `["tv:s2:e2"]`, Valid: true})
	want := map[string]any{"group_name": "NEST", "release_title": "[NEST] Title - 02 [1080p].mkv", "episodes": []int{2}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("RunContext = %#v, want %#v", got, want)
	}
	if empty := RunContext("", " ", sql.NullString{}); len(empty) != 0 {
		t.Fatalf("empty facts must give an empty context, got %#v", empty)
	}
}

func TestPlanEpisodes(t *testing.T) {
	for name, tc := range map[string]struct {
		raw  sql.NullString
		want []int
	}{
		"null":          {sql.NullString{}, nil},
		"invalid json":  {sql.NullString{String: "{", Valid: true}, nil},
		"empty":         {sql.NullString{String: "[]", Valid: true}, nil},
		"movie only":    {sql.NullString{String: `["movie:0"]`, Valid: true}, nil},
		"batch sorted":  {sql.NullString{String: `["tv:s1:e3","tv:s1:e1","tv:s1:e2"]`, Valid: true}, []int{1, 2, 3}},
		"deduplicated":  {sql.NullString{String: `["tv:s1:e5","tv:s1:e5"]`, Valid: true}, []int{5}},
		"unresolved ok": {sql.NullString{String: `["unresolved","tv:s1:e4"]`, Valid: true}, []int{4}},
	} {
		t.Run(name, func(t *testing.T) {
			if got := PlanEpisodes(tc.raw); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("PlanEpisodes = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestLibraryFolderNeverKeepsTheRoot(t *testing.T) {
	for in, want := range map[string]string{
		"/media/anime/侦探已经死了。/Season 2/侦探已经死了。 S02E02.mkv": "侦探已经死了。/Season 2",
		"/library/Show/file.mkv": "library/Show",
		"/file.mkv":              "",
		"file.mkv":               "",
		"":                       "",
		"/a/b/c/":                "a/b",
	} {
		if got := LibraryFolder(in); got != want {
			t.Fatalf("LibraryFolder(%q) = %q, want %q", in, got, want)
		}
		if strings.HasPrefix(LibraryFolder(in), "/") {
			t.Fatalf("LibraryFolder(%q) keeps a leading slash", in)
		}
	}
}

func TestFileName(t *testing.T) {
	if got := FileName("", "/media/Show/Season 1/Show S01E01.mkv"); got != "Show S01E01.mkv" {
		t.Fatalf("FileName = %q", got)
	}
	if got := FileName("  ", ""); got != "" {
		t.Fatalf("FileName of nothing = %q", got)
	}
}

func TestMarshalDetailsOmitsEmptyValues(t *testing.T) {
	raw, err := MarshalDetails(map[string]any{"group_name": "", "episodes": []int{}, "replaced_files": []string{}, "kept_file": nil, "size_bytes": int64(0)})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got["size_bytes"] != float64(0) {
		t.Fatalf("details = %s, want only size_bytes", raw)
	}
	none, err := MarshalDetails(map[string]any{"group_name": ""})
	if err != nil || none != nil {
		t.Fatalf("no values must encode as nil, got %s %v", none, err)
	}
}

func TestBoundTextKeepsRunes(t *testing.T) {
	if got := BoundText("侦探已经死了", 7); got != "侦探" {
		t.Fatalf("BoundText = %q", got)
	}
}
