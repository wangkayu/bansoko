package domain

import (
	protocol "bansoko/internal/acquisition/terminal/domain"
	provider "bansoko/internal/provider/domain"
	"strings"
	"testing"
	"time"
)

func observationFixture(t *testing.T) (provider.GroupSnapshot, provider.GroupReadResult) {
	t.Helper()
	now := time.Date(2026, 10, 5, 10, 0, 0, 123000000, time.UTC)
	yes, no := true, false
	groups := []provider.GroupSnapshotEntry{}
	for i, ref := range []string{"20", "10"} {
		active := &yes
		if i == 1 {
			active = &no
		}
		r := provider.GroupRef{Provider: provider.ProviderMikan, ExternalID: ref}
		groups = append(groups, provider.GroupSnapshotEntry{Ref: r, DisplayName: "字幕 <組> & " + ref, ObservedAt: now, Active: active, FeedLocator: provider.FeedLocator{Provider: r.Provider, CatalogEntryRef: "123", Group: r}})
	}
	s := provider.GroupSnapshot{ID: "snapshot-1", Provider: provider.ProviderMikan, CatalogEntryRef: "123", ObservedAt: now, SchemaVersion: "mikan-detail-groups-v1", Version: 1, Groups: groups}
	r := provider.GroupReadResult{Provider: s.Provider, CatalogEntryRef: s.CatalogEntryRef, ObservedAt: now, SchemaVersion: s.SchemaVersion, Complete: true, CompletenessEvidence: "mikan-detail-group-blocks-v1", Groups: groups}
	return s, r
}
func TestWholeObservationCanonicalAndEvidence(t *testing.T) {
	s, r := observationFixture(t)
	o, err := FreezeGroupObservation(s, r)
	if err != nil {
		t.Fatal(err)
	}
	raw, digest, err := o.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeGroupObservation(raw, digest)
	if err != nil {
		t.Fatal(err)
	}
	if !decoded.Authoritative() || decoded.Groups[0].GroupRef != "10" || *decoded.Groups[0].Available.Value {
		t.Fatalf("lost known false/order: %+v", decoded)
	}
	if strings.Contains(string(raw), string(rune(92))+"u003c") || strings.Contains(string(raw), "feed_locator") || decoded.SourceVersion.State != "absent" {
		t.Fatalf("not protocol canonical: %s", raw)
	}
	s.Groups[0], s.Groups[1] = s.Groups[1], s.Groups[0]
	again, _ := FreezeGroupObservation(s, r)
	b, d, err := again.Canonical()
	if err != nil || string(b) != string(raw) || d != digest {
		t.Fatalf("adapter order changed observation: %s %v", b, err)
	}
	r.Complete = false
	incomplete, err := FreezeGroupObservation(s, r)
	if err != nil || incomplete.Authoritative() || incomplete.Completeness.State != "known" {
		t.Fatalf("incomplete lost: %+v %v", incomplete, err)
	}
	r.Complete = true
	r.CompletenessEvidence = ""
	unknown, err := FreezeGroupObservation(s, r)
	if err != nil || unknown.Authoritative() || unknown.Completeness.State != "unavailable" {
		t.Fatalf("missing evidence inferred: %+v %v", unknown, err)
	}
	r.CompletenessEvidence = "mikan-detail-group-blocks-v1"
	s.Groups[0].Active = nil
	unknown, err = FreezeGroupObservation(s, r)
	if err != nil || unknown.Authoritative() {
		t.Fatalf("missing status inferred: %+v %v", unknown, err)
	}
	s.Groups = nil
	r.Groups = nil
	empty, err := FreezeGroupObservation(s, r)
	if err != nil || !empty.Authoritative() || len(empty.Groups) != 0 {
		t.Fatalf("valid empty: %+v %v", empty, err)
	}
}
func TestObservationClosedDecoderRejectsMutations(t *testing.T) {
	s, r := observationFixture(t)
	o, _ := FreezeGroupObservation(s, r)
	raw, digest, _ := o.Canonical()
	for _, bad := range []string{
		strings.Replace(string(raw), `"schema_version":1`, `"schema_version":1,"extra":true`, 1),
		strings.Replace(string(raw), `"schema_version":1`, `"schema_version":1,"schema_version":1`, 1),
		strings.Replace(string(raw), `"schema_version":1,`, "", 1),
		strings.Replace(string(raw), `"reason_code":null,`, "", 1),
		strings.Replace(string(raw), `"complete":true`, `"complete":null`, 1),
		strings.Replace(string(raw), `"value":false`, `"value":null`, 1),
		strings.Replace(string(raw), `"group_ref":"20"`, `"group_ref":"10"`, 1),
		strings.Replace(string(raw), `"catalog_entry_ref":"123"`, `"catalog_entry_ref":"0123"`, 1),
	} {
		v, err := protocol.ParseStrict([]byte(bad))
		if err != nil {
			continue
		}
		b := protocol.Canonical(v)
		d := protocol.Digest(GroupObservationDigestDomain, b)
		if _, err := DecodeGroupObservation(b, d); err == nil {
			t.Errorf("mutation accepted: %s", b)
		}
	}
	if _, err := DecodeGroupObservation(raw, strings.Replace(digest, "sha256:", "other:", 1)); err == nil {
		t.Fatal("wrong digest accepted")
	}
}

func TestObservationPreservesLiteralBackslashAndUnicode(t *testing.T) {
	s, r := observationFixture(t)
	s.Groups[0].DisplayName = `字面 \u0010 and "quotes"`
	o, err := FreezeGroupObservation(s, r)
	if err != nil {
		t.Fatal(err)
	}
	raw, digest, err := o.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeGroupObservation(raw, digest)
	if err != nil || got.Groups[1].DisplayName != s.Groups[0].DisplayName {
		t.Fatalf("literal escaped text changed: %+v %v", got, err)
	}
}
