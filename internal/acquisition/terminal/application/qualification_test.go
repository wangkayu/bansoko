package application

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	protocol "bansoko/internal/acquisition/terminal/domain"
	anime "bansoko/internal/anime/domain"
	"bansoko/internal/attrrules"
	catalog "bansoko/internal/catalog/domain"
	"bansoko/internal/namingvars"
	provider "bansoko/internal/provider/domain"
)

func qualificationFixture(t *testing.T, mode string) qualificationMaterial {
	t.Helper()
	rules := attrrules.Rules{Version: 1, Resolution: []string{"1080p"}, Quality: []string{"web"}, Language: []string{"jpn"}, SpecialMarkers: []string{"special"}, GroupBrackets: []string{"[", "]"}}
	attrDigest, err := attrrules.Fingerprint(rules)
	if err != nil {
		t.Fatal(err)
	}
	source := "vars.actual = {title: input.release_title};"
	varsDigest, err := namingvars.SourceFingerprint(source)
	if err != nil {
		t.Fatal(err)
	}
	b := anime.BusinessSnapshot{SchemaVersion: "business-snapshot-v1", Sections: map[string]json.RawMessage{}, SectionVersions: map[string]int64{}, SectionSchemas: map[string]string{}, Parser: anime.ParserPolicyEnvelope{SchemaVersion: anime.ParserPolicyEnvelopeSchemaVersion, Kind: "regex", SourceOwner: "global", Provenance: "global", PolicyVersion: 1, ProfileVersion: 1, RulesOwnerVersion: 1}, RetentionVersion: 1, FileEffectVersion: 1, UnclassifiedRoute: "skip"}
	for _, name := range anime.EffectiveSectionOrder {
		value, err := anime.DefaultSectionValue(name)
		if err != nil {
			t.Fatal(err)
		}
		b.Sections[name], err = json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		b.SectionVersions[name], b.SectionSchemas[name] = 1, anime.SectionSchemaVersions[name]
	}
	b.Retention = anime.RetentionValue{AdmissionMode: "all", QualityProfile: json.RawMessage(`{"version":"profile-v1","items":[{"key":"subgroup.score","ranks":[]},{"key":"release.lang","ranks":[["chs"],["sc"],["cht"],["tc"],["jpn"],["eng"]]},{"key":"release.resolution","ranks":[["2160p"],["1440p"],["1080p"],["720p"],["480p"]]},{"key":"release.revision","ranks":[]}]}`), CleanupPolicy: json.RawMessage(`{"version":"cleanup-v1","effect":"quarantine","grace_seconds":60}`)}
	b.Programs = anime.ProgramReferences{AttrRules: namingvars.ProgramRef{Digest: attrDigest, Version: attrrules.ProfileVersion(attrDigest)}, Vars: namingvars.ProgramRef{Digest: varsDigest, Version: namingvars.ProfileVersion(varsDigest)}}
	digest, err := b.Digest()
	if err != nil {
		t.Fatal(err)
	}
	program, err := namingvars.NewProgram(b.Programs.Vars, source)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	yes := true
	groupRef := provider.GroupRef{Provider: provider.ProviderMikan, ExternalID: "10"}
	result := provider.GroupReadResult{Provider: provider.ProviderMikan, CatalogEntryRef: "123", ObservedAt: now, SchemaVersion: "mikan-detail-groups-v1", Complete: true, CompletenessEvidence: "mikan-detail-group-blocks-v1", Groups: []provider.GroupSnapshotEntry{{Ref: groupRef, DisplayName: "字幕组", ObservedAt: now, Active: &yes, FeedLocator: provider.FeedLocator{Provider: provider.ProviderMikan, CatalogEntryRef: "123", Group: groupRef}}}}
	group, err := catalog.FreezeGroupObservation(provider.GroupSnapshot{ID: "group-1", Provider: result.Provider, CatalogEntryRef: result.CatalogEntryRef, ObservedAt: now, SchemaVersion: result.SchemaVersion, Version: 3, Groups: result.Groups}, result)
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("qualified torrent bytes")
	payloadDigest := catalog.DigestBytes(payload)
	file := catalog.TorrentFile{Ordinal: 0, SourcePath: "show/ep01.mkv", Filename: "ep01.mkv", SizeBytes: 1000, MediaKind: "video", Language: "jpn"}
	file.FileRef = catalog.TorrentFileRef(payloadDigest, file.SourcePath)
	projection, err := catalog.CanonicalTorrentProjectionDigest(payloadDigest, "parser-v1", []catalog.TorrentFile{file})
	if err != nil {
		t.Fatal(err)
	}
	feed := catalog.ProviderFeedSnapshot{ID: "feed-1", Provider: "mikan", ProviderEntryRef: "123", ProviderGroupRef: "10", Version: 2, FetchedAt: now, FreshUntil: now.Add(time.Minute), HashSchema: catalog.ProviderFeedSnapshotContentHashSchema, ProjectionSchema: catalog.TorrentManifestProjectionSchema, Seeds: []catalog.ProviderFeedSnapshotSeed{{StableSeedKey: "seed", Locator: "https://example.test/q.torrent", LocatorDisplay: "example.test/q.torrent", Title: "Qualified", Payload: payload, PayloadDigest: &payloadDigest, TorrentState: "available", Manifest: &catalog.TorrentManifest{PayloadDigest: payloadDigest, ParserVersion: "parser-v1", TorrentVersion: "v1", TorrentName: "show", InfoHashV1: strings.Repeat("a", 40), TotalSizeBytes: 1000, ProjectionDigest: projection, Files: []catalog.TorrentFile{file}}}}}
	feed.ContentHash, err = catalog.CanonicalFeedSnapshotContentHash(feed.Seeds)
	if err != nil {
		t.Fatal(err)
	}
	return qualificationMaterial{entryMode: mode, subgroupsOwnerVersion: 1, rulesOwnerVersion: 1, metadataVersion: 1, parserKind: "regex", parserVersion: 1, catalogKind: "provider_snapshot", config: configMaterial{kind: "owner", requestHash: catalog.DigestBytes([]byte("request")), managedID: "anime-1", version: 4, business: b, digest: digest}, management: managementMaterial{managedID: "anime-1", subscriptionID: "sub-1", subscriptionVersion: 5, lifecycleVersion: 6}, release: releaseMaterial{id: "rel-1", sourceID: "src-1", subscriptionID: "sub-1", version: 7, sourceVersion: 8}, payloadDigest: payloadDigest, catalog: &catalogMaterial{observation: group, feed: feed, seedKey: "seed"}, programs: programMaterial{attr: rules, vars: program}}
}

func TestQualificationConstructorBranchesAndCanonical(t *testing.T) {
	for _, kind := range []string{"owner", "saved_revision", "submitted"} {
		for _, hasCatalog := range []bool{true, false} {
			t.Run(kind+map[bool]string{true: "/catalog", false: "/direct"}[hasCatalog], func(t *testing.T) {
				m := qualificationFixture(t, "automatic_source")
				m.config.kind = kind
				if kind == "saved_revision" {
					m.config.candidateID, m.config.revision, m.config.revisionDigest = "candidate-1", 2, catalog.DigestBytes([]byte("revision"))
				}
				if !hasCatalog {
					m.catalog = nil
					m.catalogKind = "release_input"
				}
				capture, err := constructQualification(m)
				if err != nil {
					t.Fatal(err)
				}
				canonical := capture.qualification.Canonical()
				decoded, err := protocol.DecodeQualification(canonical)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(canonical, decoded.Canonical()) {
					t.Fatal("canonical changed after decode")
				}
				for i := 0; i < 10; i++ {
					next, err := constructQualification(m)
					if err != nil || !bytes.Equal(next.qualification.Canonical(), canonical) {
						t.Fatalf("unstable capture: %v", err)
					}
				}
				var q map[string]any
				if err := json.Unmarshal(canonical, &q); err != nil {
					t.Fatal(err)
				}
				cfg := q["config"].(map[string]any)
				if cfg["kind"] != kind || q["management"].(map[string]any)["expected_lifecycle_version"] != float64(6) {
					t.Fatal("lost config/lifecycle version")
				}
				if (q["feed"] != nil) != hasCatalog || q["placement"].(map[string]any)["kind"] != "none" {
					t.Fatal("unexpected branch")
				}
				// Mutating the source after construction cannot mutate the private value.
				m.config.business.Sections[anime.SectionParser] = json.RawMessage(`{}`)
				if !bytes.Equal(canonical, capture.qualification.Canonical()) {
					t.Fatal("capture aliases source")
				}
			})
		}
	}
}

func TestQualificationConstructorMissingMaterial(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*qualificationMaterial)
	}{
		{"business", func(m *qualificationMaterial) { m.config.business = anime.BusinessSnapshot{} }},
		{"digest", func(m *qualificationMaterial) { m.config.digest = "" }},
		{"owner version", func(m *qualificationMaterial) { m.config.version = 0 }},
		{"owner mismatch", func(m *qualificationMaterial) { m.config.managedID = "other" }},
		{"saved revision", func(m *qualificationMaterial) { m.config.kind = "saved_revision" }},
		{"submitted request", func(m *qualificationMaterial) { m.config.kind = "submitted"; m.config.requestHash = "" }},
		{"unknown config", func(m *qualificationMaterial) { m.config.kind = "seed" }},
		{"lifecycle", func(m *qualificationMaterial) { m.management.lifecycleVersion = 0 }},
		{"subscription", func(m *qualificationMaterial) { m.management.subscriptionID = "" }},
		{"source owner", func(m *qualificationMaterial) { m.release.subscriptionID = "other" }},
		{"source version", func(m *qualificationMaterial) { m.release.sourceVersion = 0 }},
		{"attr", func(m *qualificationMaterial) { m.programs.attr = attrrules.Rules{} }},
		{"vars", func(m *qualificationMaterial) { m.programs.vars = namingvars.Program{} }},
		{"group", func(m *qualificationMaterial) { m.catalog.observation = catalog.GroupObservation{} }},
		{"feed", func(m *qualificationMaterial) { m.catalog.feed = catalog.ProviderFeedSnapshot{} }},
		{"scope", func(m *qualificationMaterial) { m.catalog.feed.ProviderEntryRef = "456" }},
		{"selected group", func(m *qualificationMaterial) { m.catalog.feed.ProviderGroupRef = "20" }},
		{"seed", func(m *qualificationMaterial) { m.catalog.seedKey = "absent" }},
		{"payload", func(m *qualificationMaterial) { m.catalog.feed.Seeds[0].Payload = nil }},
		{"manifest", func(m *qualificationMaterial) { m.catalog.feed.Seeds[0].Manifest = nil }},
		{"direct payload", func(m *qualificationMaterial) { m.catalogKind = "release_input"; m.catalog = nil; m.payloadDigest = "" }},
		{"missing catalog", func(m *qualificationMaterial) { m.catalog = nil }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := qualificationFixture(t, "automatic_source")
			tc.mutate(&m)
			c, err := constructQualification(m)
			var typed *qualificationMaterialError
			if !errors.Is(err, errQualificationMaterial) || !errors.As(err, &typed) || typed.group == "" || len(c.qualification.Canonical()) != 4 {
				t.Fatalf("expected typed error and zero capture, got %v %s", err, c.qualification.Canonical())
			}
		})
	}
	if _, err := captureQualification(context.Background(), nil, nil, qualificationRequest{}); !errors.Is(err, errQualificationMaterial) {
		t.Fatalf("missing reader: %v", err)
	}
}
