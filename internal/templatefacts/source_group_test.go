package templatefacts

import (
	"reflect"
	"testing"
)

// TestSourceGroupIdentityValuesPerProvider is the projection matrix: a provider
// identity fills only its own root with the stable external id under its own
// field key, a source without one fills neither (there is no custom root; the
// configured display group name reads anime.group, never a namespace field).
func TestSourceGroupIdentityValuesPerProvider(t *testing.T) {
	cases := []struct {
		name     string
		identity SourceGroupIdentity
		want     map[string]Values
	}{
		{
			name:     "anibt provider group",
			identity: SourceGroupIdentity{Provider: "anibt", GroupRef: "samplegroup", GroupName: "SampleGroup"},
			want:     map[string]Values{"anibt": {"GroupSlug": "samplegroup"}},
		},
		{
			name:     "mikan provider group",
			identity: SourceGroupIdentity{Provider: "mikan", GroupRef: "58", GroupName: "SampleGroup"},
			want:     map[string]Values{"mikan": {"GroupID": "58"}},
		},
		{
			// A source without a provider identity projects neither root: its
			// configured name is anime.group, not a guessed provider id.
			name:     "custom rss source",
			identity: SourceGroupIdentity{GroupName: "示例字幕组"},
			want:     map[string]Values{},
		},
		{
			// A provider_group row without recorded provenance has no provider
			// identity and projects neither root either.
			name:     "provider group without provenance",
			identity: SourceGroupIdentity{GroupName: "Some Group"},
			want:     map[string]Values{},
		},
		{
			// A provider identity without a configured name still exposes the
			// stable id.
			name:     "mikan group without a name",
			identity: SourceGroupIdentity{Provider: "mikan", GroupRef: "58"},
			want:     map[string]Values{"mikan": {"GroupID": "58"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			anibt, mikan := tc.identity.Values()
			for root, got := range map[string]Values{"anibt": anibt, "mikan": mikan} {
				want := tc.want[root]
				if len(want) == 0 && len(got) == 0 {
					continue
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("Values()[%s] = %#v, want %#v (anibt/mikan = %#v / %#v)", root, got, want, anibt, mikan)
				}
			}
		})
	}
}

// TestSourceGroupFactsAreRegisteredNullableStrings: the stable identity facts
// exist under their provider roots, are nullable strings in naming, and carry
// catalog copy.
func TestSourceGroupFactsAreRegisteredNullableStrings(t *testing.T) {
	for _, path := range []string{
		"anibt.groupslug",
		"mikan.groupid",
	} {
		fact, ok := Lookup(path)
		if !ok {
			t.Fatalf("%s is not registered", path)
		}
		if fact.Type != TypeString {
			t.Fatalf("%s type = %s", path, fact.Type)
		}
		if !fact.In(ScenarioNaming) || !fact.NullableIn(ScenarioNaming) {
			t.Fatalf("%s must be a nullable naming fact (scenarios %b nullable %b)", path, fact.Scenarios, fact.Nullable)
		}
	}
	// The stable external id of each provider is its own field key.
	if field, ok := lookupSourceGroupField(AniBTSourceGroupFields, "GroupSlug"); !ok || field.Name != "groupslug" {
		t.Fatalf("anibt group fields = %#v", AniBTSourceGroupFields)
	}
	if field, ok := lookupSourceGroupField(MikanSourceGroupFields, "GroupID"); !ok || field.Name != "groupid" {
		t.Fatalf("mikan group fields = %#v", MikanSourceGroupFields)
	}
	// The AniBT source group fields share the anibt root with the AniBT tag
	// fields without colliding with any of them.
	for _, tag := range AniBTTagFields {
		for _, group := range AniBTSourceGroupFields {
			if tag.Key == group.Key || tag.Name == group.Name {
				t.Fatalf("anibt root collision: %q", group.Name)
			}
		}
	}
}

// TestAnimeGroupAndReleaseSrcRegistration pins the template face of the
// group-provenance ruling: the display name lives only at anime.group (every
// naming and workflow scenario, always nullable, no fallback), release.src
// stays a media/release-workflow fact that never reaches the naming language,
// and the withdrawn src.* and per-provider display aliases are gone.
func TestAnimeGroupAndReleaseSrcRegistration(t *testing.T) {
	group, ok := Lookup("anime.group")
	if !ok {
		t.Fatal("anime.group is not registered")
	}
	if group.Type != TypeString {
		t.Fatalf("anime.group type = %s", group.Type)
	}
	// anime.group rides scenarioAll|scenarioWorkflow: every listed scenario,
	// nullable everywhere (the display name reads empty when unconfigured).
	for _, scenario := range Scenarios {
		if !group.In(scenario) || !group.NullableIn(scenario) {
			t.Fatalf("anime.group must be registered and nullable in scenario %b (scenarios %b nullable %b)", scenario, group.Scenarios, group.Nullable)
		}
	}

	src, ok := Lookup("release.src")
	if !ok {
		t.Fatal("release.src is not registered")
	}
	if src.Type != TypeString {
		t.Fatalf("release.src type = %s", src.Type)
	}
	if src.In(ScenarioNaming) {
		t.Fatalf("release.src must not be registered for the naming language (scenarios %b)", src.Scenarios)
	}
	for _, scenario := range []Scenario{ScenarioReleaseDiscovered, ScenarioDownloadFailed} {
		if !src.In(scenario) || !src.NullableIn(scenario) {
			t.Fatalf("release.src missing in release workflow scenario %b (scenarios %b nullable %b)", scenario, src.Scenarios, src.Nullable)
		}
	}

	// The withdrawn leaves must stay withdrawn: the group name is consumed
	// internally as anime.group and never re-exposed under a source root.
	for _, path := range []string{
		"src.kind", "src.group_name", "release.group", "release.group_source",
		"anibt.groupname", "mikan.groupname", "custom.groupname",
	} {
		if _, ok := Lookup(path); ok {
			t.Fatalf("%s is withdrawn from the face but still registered", path)
		}
	}
}

func lookupSourceGroupField(fields []Field, key string) (Field, bool) {
	for _, field := range fields {
		if field.Key == key {
			return field, true
		}
	}
	return Field{}, false
}
