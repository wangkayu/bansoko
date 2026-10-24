package naming

import (
	"errors"
	"strings"
	"testing"

	providerdomain "bansoko/internal/provider/domain"
	"bansoko/internal/templatefacts"
)

// sourceGroupContext is the naming context of one accepted release whose origin
// RSS source carries the given group identity. The identity reaches the render
// through SourceNamingFacts, the same frozen evidence Source.GroupName comes
// from, so the group fields and the effective group can never disagree.
func sourceGroupContext(kind string, group providerdomain.GroupRef, groupName string) NamingContext {
	ctx := sampleTVContext()
	ctx.Source = SourceNamingFacts{Kind: kind, GroupName: groupName, Group: group}
	return ctx
}

// sourceGroupFieldPaths are the group face of the naming catalog: the stable
// identity field of each provider root, and the display group name both ride
// the frozen source evidence into anime.group.
var sourceGroupFieldPaths = []string{
	"anibt.groupslug",
	"mikan.groupid",
	"anime.group",
}

// TestSourceGroupIdentityFieldsRenderPerSourceKind is the source kind x field
// matrix: a provider_group source exposes only its provider's root, the display
// group name (anime.group) is exactly the name the source configured — never
// the release title's group and never another root's naming — and every other
// field reads null (the `??` fallback proves null rather than empty).
func TestSourceGroupIdentityFieldsRenderPerSourceKind(t *testing.T) {
	cases := []struct {
		name      string
		kind      string
		group     providerdomain.GroupRef
		groupName string
		anibtSlug string
		mikanID   string
	}{
		{
			name: "anibt provider group", kind: "provider_group",
			group:     providerdomain.GroupRef{Provider: providerdomain.ProviderAniBT, ExternalID: "samplegroup"},
			groupName: "SampleGroup", anibtSlug: "samplegroup",
		},
		{
			name: "mikan provider group", kind: "provider_group",
			group:     providerdomain.GroupRef{Provider: providerdomain.ProviderMikan, ExternalID: "58"},
			groupName: "SampleGroup", mikanID: "58",
		},
		{
			name: "custom rss", kind: "custom_rss",
			groupName: "示例字幕组",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := sourceGroupContext(tc.kind, tc.group, tc.groupName)
			want := map[string]string{
				"anibt.groupslug": tc.anibtSlug,
				"mikan.groupid":   tc.mikanID,
				"anime.group":     tc.groupName,
			}
			for _, path := range sourceGroupFieldPaths {
				expected := want[path]
				if expected == "" {
					expected = "none"
				}
				got, err := renderNamespaceTemplate(t, "${"+path+" ?? 'none'}.${file.ext}", ctx)
				if err != nil {
					t.Fatalf("%s: render %s: %v", tc.name, path, err)
				}
				if got != expected+".mkv" {
					t.Fatalf("%s: %s = %q, want %q", tc.name, path, got, expected+".mkv")
				}
			}
		})
	}
}

// TestSourceGroupIdentityMissingFieldsRefuseTheRender: reading a provider root
// the source does not expose outside any fallback is the existing missing-fact
// refusal — never an empty segment and never another source's value; the
// display group name is defined for every source (empty instead).
func TestSourceGroupIdentityMissingFieldsRefuseTheRender(t *testing.T) {
	cases := []struct {
		name   string
		ctx    NamingContext
		source string
		reason string
	}{
		{
			name:   "custom source has no provider group id",
			ctx:    sourceGroupContext("custom_rss", providerdomain.GroupRef{}, "示例字幕组"),
			source: "${mikan.groupid}.${file.ext}",
			reason: "naming_fact_undefined_mikan_groupid",
		},
		{
			name:   "anibt source has no mikan root",
			ctx:    sourceGroupContext("provider_group", providerdomain.GroupRef{Provider: providerdomain.ProviderAniBT, ExternalID: "samplegroup"}, "SampleGroup"),
			source: "${mikan.groupid}.${file.ext}",
			reason: "naming_fact_undefined_mikan_groupid",
		},
		{
			name:   "mikan source has no anibt root",
			ctx:    withoutAniBTTags(sourceGroupContext("provider_group", providerdomain.GroupRef{Provider: providerdomain.ProviderMikan, ExternalID: "58"}, "SampleGroup")),
			source: "${anibt.groupslug}.${file.ext}",
			reason: "naming_fact_undefined_anibt_groupslug",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := renderNamespaceTemplate(t, tc.source, tc.ctx)
			var namingErr *Error
			if !errors.As(err, &namingErr) || namingErr.Code != ErrCodeNamingFactUndefined || namingErr.ReasonCode() != tc.reason {
				t.Fatalf("render error = %v, want %s", err, tc.reason)
			}
		})
	}
}

// TestSourceGroupIdentityFollowsTheSourceEvidence: the external identity and
// the effective display name are projected from the one frozen source record,
// so they move together with anime.group instead of drifting apart.
func TestSourceGroupIdentityFollowsTheSourceEvidence(t *testing.T) {
	ctx := sampleTVContext()
	ctx.Release.OriginalTitle = "进击的巨人 - 07 [1080p]" // no title group: the source group is effective
	ctx.Source = SourceNamingFacts{
		Kind:      "provider_group",
		GroupName: "SampleGroup",
		Group:     providerdomain.GroupRef{Provider: providerdomain.ProviderAniBT, ExternalID: "samplegroup"},
	}
	resolved := DefaultRegistry().ResolveAll(ResolverInput{Facts: ctx}).Context
	facts := UnifiedFacts(resolved)
	if facts.Anime.Group != "SampleGroup" {
		t.Fatalf("effective group = %q, want the source group", facts.Anime.Group)
	}
	value, ok := facts.Anibt.Get("GroupSlug")
	if !ok || value != "samplegroup" {
		t.Fatalf("anibt.groupslug = %q, %v; want samplegroup", value, ok)
	}
	// The render proof: one evidence change moves the effective group and the
	// identity field together (they are not separately looked up), and the
	// title's prefix group never appears (no fallback of any kind).
	ctx.Source = SourceNamingFacts{
		Kind:      "provider_group",
		GroupName: "Sakurato",
		Group:     providerdomain.GroupRef{Provider: providerdomain.ProviderAniBT, ExternalID: "samplegroup"},
	}
	resolved = DefaultRegistry().ResolveAll(ResolverInput{Facts: ctx}).Context
	facts = UnifiedFacts(resolved)
	if facts.Anime.Group != "Sakurato" {
		t.Fatalf("effective group = %q, want the moved source group", facts.Anime.Group)
	}
	if value, ok := facts.Anibt.Get("GroupSlug"); !ok || value != "samplegroup" {
		t.Fatalf("anibt.groupslug = %q, %v; want samplegroup", value, ok)
	}
}

// TestSourceGroupIdentityIsFrozenEvidence: the group identity is part of the
// facts the file effect records in its frozen render snapshot, exactly like
// Source.GroupName, so a maintenance re-render cannot silently drop it.
func TestSourceGroupIdentityIsFrozenEvidence(t *testing.T) {
	ctx := sourceGroupContext("provider_group", providerdomain.GroupRef{Provider: providerdomain.ProviderMikan, ExternalID: "58"}, "SampleGroup")
	recorded := ReplayFactValues(ctx)
	for path, want := range map[string]string{"Source.GroupName": "SampleGroup", "Source.Group": "mikan:58"} {
		if got := recorded[path]; got != want {
			t.Fatalf("recorded %s = %q, want %q", path, got, want)
		}
	}
	// A source without a provider identity records none: the guard then has
	// nothing to refuse. Its configured name still rides into anime.group.
	empty := sourceGroupContext("custom_rss", providerdomain.GroupRef{}, "示例字幕组")
	values := ReplayFactValues(empty)
	if values["Source.Group"] != "" {
		t.Fatalf("custom source recorded a provider identity: %q", values["Source.Group"])
	}
	if values["Source.GroupName"] != "示例字幕组" {
		t.Fatalf("custom source recorded its group name: %q", values["Source.GroupName"])
	}
}

// TestSourceGroupIdentityInShippedSamples: the fixed catalog samples carry a
// group identity, so the editor's sample preview shows the provider group id
// and the configured display group instead of a null for every field.
func TestSourceGroupIdentityInShippedSamples(t *testing.T) {
	for ref, want := range map[string]map[string]string{
		"sample-tv":    {"anibt.groupslug": "shilizimuazu", "anime.group": "示例字幕组"},
		"sample-movie": {"anime.group": "示例字幕组"},
	} {
		ctx, ok := ResolveSampleContext(ref)
		if !ok {
			t.Fatalf("sample %s is missing", ref)
		}
		for path, expected := range want {
			got, err := renderNamespaceTemplate(t, "${"+path+" ?? '-'}.${file.ext}", ctx)
			if err != nil {
				t.Fatalf("%s: render %s: %v", ref, path, err)
			}
			want := expected + "." + ctx.File.Extension
			if got != want {
				t.Fatalf("%s: %s = %q, want %q", ref, path, got, want)
			}
		}
	}
}

// TestSourceGroupCatalogFields: the group facts are advertised in the naming
// catalog as nullable strings with a sample and copy, so the editor offers
// them and the missing-fact check covers them.
func TestSourceGroupCatalogFields(t *testing.T) {
	fields := map[string]CatalogField{}
	for _, field := range GenerateCatalogDocument().Fields {
		fields[field.Path] = field
	}
	for _, path := range sourceGroupFieldPaths {
		field, ok := fields[path]
		if !ok {
			t.Fatalf("naming catalog is missing %s", path)
		}
		if !field.Nullable || field.Type != "string" || field.SampleValue == "" || field.Description == "" {
			t.Fatalf("catalog field %s = %+v", path, field)
		}
		if field.Phase != string(templatefacts.PhaseRelease) {
			t.Fatalf("catalog field %s phase = %q", path, field.Phase)
		}
		if !strings.HasPrefix(field.LabelKey, "facts."+path+".") {
			t.Fatalf("catalog field %s label key = %q", path, field.LabelKey)
		}
	}
}

// withoutAniBTTags drops the release's AniBT tag values: a non-AniBT release
// has no anibt namespace at all.
func withoutAniBTTags(ctx NamingContext) NamingContext {
	ctx.AniBTTags = nil
	return ctx
}
