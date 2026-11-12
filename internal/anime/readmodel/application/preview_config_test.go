package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	animeapplication "bansoko/internal/anime/application"
	animedomain "bansoko/internal/anime/domain"
)

func revisionConfigFixture(t *testing.T) animeapplication.CandidateRevision {
	t.Helper()
	draft := readableDraft(t, animedomain.ProviderKindMikan, "679")
	draft.Rules.Sections.TagFilters = json.RawMessage(`{"mode":"replace","value":{"anibt":{"require":["language:CHS"],"exclude":[]},"title":{"require":[],"exclude":[]}}}`)
	sections, err := animedomain.ResolveDraftEffectiveSections(draft)
	if err != nil {
		t.Fatal(err)
	}
	marshal := func(value any) json.RawMessage {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	return animeapplication.CandidateRevision{Draft: draft, Effective: marshal(sections.Sections), SectionVersions: marshal(sections.Versions), SectionSchemas: marshal(sections.SchemaVersions)}
}

func TestRevisionPreviewConfigUsesRevisionTags(t *testing.T) {
	row := revisionConfigFixture(t)
	got, err := revisionPreviewConfig(row)
	if err != nil {
		t.Fatal(err)
	}
	want, err := animedomain.ResolveDraftTagFilters(row.Draft)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.TagFilters, want) || got.TagFilters.Digest == animedomain.DefaultResolvedTagFilters().Digest {
		t.Fatalf("revision tags lost: %+v", got.TagFilters)
	}
	if !reflect.DeepEqual(got.DownloadCompletion, animedomain.DefaultResolvedDownloadCompletion()) {
		t.Fatalf("revision completion = %+v", got.DownloadCompletion)
	}
	row.Draft.Rules.Sections.TagFilters = json.RawMessage(`{"mode":"inherit"}`)
	if _, err := revisionPreviewConfig(row); err == nil {
		t.Fatal("invalid revision tags silently became defaults")
	}
}

type completionReadProbe struct{ calls int }

func (q *completionReadProbe) QueryContext(context.Context, string, ...any) (*sql.Rows, error) {
	q.calls++
	return nil, errors.New("completion read reached")
}

func TestOwnerPreviewConfigValidatesBeforeGlobalRead(t *testing.T) {
	row := revisionConfigFixture(t)
	resolved, err := row.Resolved()
	if err != nil {
		t.Fatal(err)
	}
	subgroups := animedomain.OverrideValue{Value: resolved.Sections[animedomain.SectionSubgroups]}
	rules := map[string]animedomain.OverrideValue{}
	for key, value := range resolved.Sections {
		rules[key] = animedomain.OverrideValue{Value: value}
	}
	probe := &completionReadProbe{}
	if _, err := ownerPreviewConfig(context.Background(), probe, subgroups, rules); err == nil || probe.calls != 0 {
		t.Fatalf("missing tags must fail before Global read: calls=%d err=%v", probe.calls, err)
	}
	tags, err := animedomain.ResolveDraftTagFilters(row.Draft)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(tags.Rules)
	if err != nil {
		t.Fatal(err)
	}
	rules[animedomain.SectionTagFilters] = animedomain.OverrideValue{Value: raw}
	if _, err := ownerPreviewConfig(context.Background(), probe, subgroups, rules); err == nil || err.Error() != "read global download completion: completion read reached" || probe.calls != 1 {
		t.Fatalf("valid sections must propagate Global read failure: calls=%d err=%v", probe.calls, err)
	}
}
