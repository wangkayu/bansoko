package domain

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	protocol "bansoko/internal/acquisition/terminal/domain"
	provider "bansoko/internal/provider/domain"
)

const GroupObservationDigestDomain = "acquisition-group-observation:v1"

var ErrGroupObservationUnverifiable = errors.New("group observation unverifiable")

// ObservationValue preserves absent/unavailable separately from known false.
type ObservationValue[T any] struct {
	State      string  `json:"state"`
	Value      *T      `json:"value"`
	ReasonCode *string `json:"reason_code"`
}

func observationKnown[T any](value T) ObservationValue[T] {
	return ObservationValue[T]{State: "known", Value: &value}
}
func observationUnavailable[T any]() ObservationValue[T] {
	reason := "OBSERVATION_EVIDENCE_UNAVAILABLE"
	return ObservationValue[T]{State: "unavailable", ReasonCode: &reason}
}

type GroupObservationScope struct {
	Provider        provider.Kind `json:"provider"`
	CatalogEntryRef string        `json:"catalog_entry_ref"`
}

type GroupCompleteness struct {
	Complete       bool   `json:"complete"`
	Evidence       string `json:"evidence"`
	AdapterVersion string `json:"adapter_version"`
}

type ObservedGroup struct {
	GroupRef    string                 `json:"group_ref"`
	DisplayName string                 `json:"display_name"`
	Available   ObservationValue[bool] `json:"available"`
	ObservedAt  string                 `json:"observed_at"`
}

// GroupObservation is the immutable whole observation, not selection intent.
// Feed locators and retention do not participate in its content identity.
type GroupObservation struct {
	SchemaVersion   int                                 `json:"schema_version"`
	Scope           GroupObservationScope               `json:"scope"`
	SnapshotRef     string                              `json:"snapshot_ref"`
	SnapshotVersion int64                               `json:"snapshot_version"`
	ObservedAt      string                              `json:"observed_at"`
	SourceVersion   ObservationValue[string]            `json:"source_version"`
	ProviderSchema  string                              `json:"provider_schema"`
	Completeness    ObservationValue[GroupCompleteness] `json:"completeness"`
	Groups          []ObservedGroup                     `json:"groups"`
}

func GroupEvidence(providerKind provider.Kind) (schema, evidence string) {
	switch providerKind {
	case provider.ProviderMikan:
		return "mikan-detail-groups-v1", "mikan-detail-group-blocks-v1"
	case provider.ProviderAniBT:
		return "anibt-anime-groups-v1", "anibt-entry-total-v1"
	default:
		return "", ""
	}
}

// FreezeGroupObservation copies the actual adapter result. Unregistered evidence
// is unavailable, never inferred from a row count, schema name or HTTP success.
func FreezeGroupObservation(snapshot provider.GroupSnapshot, result provider.GroupReadResult) (GroupObservation, error) {
	if snapshot.Provider != result.Provider || snapshot.CatalogEntryRef != result.CatalogEntryRef || snapshot.SchemaVersion != result.SchemaVersion {
		return GroupObservation{}, errors.New("group result scope/schema mismatch")
	}
	o := GroupObservation{
		SchemaVersion: 1, Scope: GroupObservationScope{Provider: snapshot.Provider, CatalogEntryRef: snapshot.CatalogEntryRef},
		SnapshotRef: snapshot.ID, SnapshotVersion: snapshot.Version,
		ObservedAt: snapshot.ObservedAt.UTC().Format(time.RFC3339Nano), ProviderSchema: snapshot.SchemaVersion,
		SourceVersion: ObservationValue[string]{State: "absent"},
		Completeness:  observationUnavailable[GroupCompleteness](), Groups: make([]ObservedGroup, 0, len(snapshot.Groups)),
	}
	if snapshot.SourceVersion != "" {
		o.SourceVersion = observationKnown(snapshot.SourceVersion)
	}
	schema, evidence := GroupEvidence(snapshot.Provider)
	if result.SchemaVersion == schema && result.CompletenessEvidence == evidence {
		o.Completeness = observationKnown(GroupCompleteness{Complete: result.Complete, Evidence: evidence, AdapterVersion: schema})
	}
	for _, g := range snapshot.Groups {
		available := observationUnavailable[bool]()
		if g.Active != nil {
			available = observationKnown(*g.Active)
		}
		o.Groups = append(o.Groups, ObservedGroup{GroupRef: g.Ref.ExternalID, DisplayName: g.DisplayName,
			Available: available, ObservedAt: g.ObservedAt.UTC().Format(time.RFC3339Nano)})
	}
	sort.Slice(o.Groups, func(i, j int) bool { return o.Groups[i].GroupRef < o.Groups[j].GroupRef })
	return o, o.Validate()
}

func observationText(s string, max int, nonempty bool) bool {
	return utf8.ValidString(s) && len(s) <= max && (!nonempty || strings.TrimSpace(s) != "") && strings.IndexFunc(s, unicode.IsControl) < 0
}

func validObservationValue[T any](v ObservationValue[T], allowAbsent bool) bool {
	switch v.State {
	case "known":
		return v.Value != nil && v.ReasonCode == nil
	case "absent":
		return allowAbsent && v.Value == nil && v.ReasonCode == nil
	case "unavailable":
		return v.Value == nil && v.ReasonCode != nil && *v.ReasonCode == "OBSERVATION_EVIDENCE_UNAVAILABLE"
	default:
		return false
	}
}

func observationTime(s string) bool {
	t, err := time.Parse(time.RFC3339Nano, s)
	return err == nil && !t.IsZero() && t.UTC().Format(time.RFC3339Nano) == s
}

func (o GroupObservation) Validate() error {
	entry, err := strconv.ParseUint(o.Scope.CatalogEntryRef, 10, 64)
	schema, evidence := GroupEvidence(o.Scope.Provider)
	if err != nil || entry == 0 || strconv.FormatUint(entry, 10) != o.Scope.CatalogEntryRef || schema == "" || o.SchemaVersion != 1 || o.SnapshotVersion < 1 ||
		!observationText(o.SnapshotRef, 128, true) || o.SnapshotRef != strings.TrimSpace(o.SnapshotRef) || !observationTime(o.ObservedAt) ||
		!observationText(o.ProviderSchema, 128, true) || !validObservationValue(o.SourceVersion, true) || !validObservationValue(o.Completeness, false) || o.Groups == nil {
		return errors.New("invalid group observation header")
	}
	if o.SourceVersion.Value != nil && !observationText(*o.SourceVersion.Value, 256, true) {
		return errors.New("invalid observation source version")
	}
	if o.Completeness.Value != nil && (o.ProviderSchema != schema || o.Completeness.Value.Evidence != evidence || o.Completeness.Value.AdapterVersion != schema) {
		return errors.New("unregistered group completeness evidence")
	}
	for i, g := range o.Groups {
		ref, err := provider.NewGroupRef(o.Scope.Provider, g.GroupRef)
		if err != nil || ref.ExternalID != g.GroupRef || (i > 0 && o.Groups[i-1].GroupRef >= g.GroupRef) ||
			!observationText(g.DisplayName, 1024, true) || utf8.RuneCountInString(g.DisplayName) > 256 || !observationTime(g.ObservedAt) || !validObservationValue(g.Available, false) {
			return fmt.Errorf("invalid observed group at %d", i)
		}
	}
	return nil
}

func (o GroupObservation) Canonical() ([]byte, string, error) {
	if err := o.Validate(); err != nil {
		return nil, "", err
	}
	raw, err := json.Marshal(o)
	if err != nil {
		return nil, "", err
	}
	value, err := protocol.ParseStrict(raw)
	if err != nil {
		return nil, "", err
	}
	canonical := protocol.Canonical(value)
	return canonical, protocol.Digest(GroupObservationDigestDomain, canonical), nil
}

// DecodeGroupObservation rejects unknown/missing keys and duplicate keys by
// comparing the strict parsed value to the exact typed re-encoding.
func DecodeGroupObservation(raw []byte, digest string) (GroupObservation, error) {
	value, err := protocol.ParseStrict(raw)
	if err != nil {
		return GroupObservation{}, err
	}
	var o GroupObservation
	if err := json.Unmarshal(raw, &o); err != nil {
		return o, err
	}
	canonical, actual, err := o.Canonical()
	if err != nil {
		return o, err
	}
	if string(protocol.Canonical(value)) != string(canonical) || string(raw) != string(canonical) || digest != actual {
		return GroupObservation{}, errors.New("group observation identity mismatch")
	}
	return o, nil
}

// Authoritative requires actual complete evidence and an explicit availability
// fact for every observed group; known false remains a valid fact.
func (o GroupObservation) Authoritative() bool {
	if o.Validate() != nil || o.Completeness.State != "known" || o.Completeness.Value == nil || !o.Completeness.Value.Complete {
		return false
	}
	for _, g := range o.Groups {
		if g.Available.State != "known" {
			return false
		}
	}
	return true
}
