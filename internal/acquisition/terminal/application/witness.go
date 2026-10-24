package application

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"time"

	protocol "bansoko/internal/acquisition/terminal/domain"
	anime "bansoko/internal/anime/domain"
	catalogreader "bansoko/internal/catalog/application"
	catalog "bansoko/internal/catalog/domain"
	selectionapp "bansoko/internal/selection/application"
	selection "bansoko/internal/selection/domain"
)

const witnessDigestDomain = "acquisition-preparation-witness:v1"
const policyDigestDomain = "acquisition-group-policy:v1"

var errWitnessMismatch = errors.New("preparation witness invariant mismatch")
var errWitnessLimit = errors.New("preparation witness limit exceeded or unconfigured")

// These bounds must come from the approved backend policy when A3/B1 is wired.
// No production capacity value is selected here. Zero never means unlimited.
type witnessLimits struct{ bytes, unionRows int }

// Native witnesses and their inputs are package-owned. In particular inputDigest
// is the actual frozen AcquisitionInput admission digest supplied by Prepare,
// not the evaluation-input digest or a client-provided expected hash.
type preparationWitness struct{ canonical, digest string }

type witnessValue struct {
	SchemaVersion     int                      `json:"schema_version"`
	Kind              string                   `json:"kind"`
	EvaluatorVersion  string                   `json:"evaluator_version"`
	RequestHash       string                   `json:"request_hash"`
	InputDigest       string                   `json:"input_digest"`
	ConfigDigest      string                   `json:"config_digest"`
	Observation       catalog.GroupObservation `json:"observation"`
	ObservationDigest string                   `json:"observation_digest"`
	Policy            json.RawMessage          `json:"policy"`
	PolicyDigest      string                   `json:"policy_digest"`
	Translation       *struct{}                `json:"translation"` // null-only; there is no translation producer.
	Decisions         []groupDecision          `json:"decisions"`
	SelectedRefs      []string                 `json:"selected_refs"`
}
type witnessText struct {
	State      string  `json:"state"`
	Value      *string `json:"value"`
	ReasonCode *string `json:"reason_code"`
}
type groupDecision struct {
	GroupRef     string                          `json:"group_ref"`
	DisplayName  witnessText                     `json:"display_name"`
	Available    bool                            `json:"available"`
	CanonicalKey witnessText                     `json:"canonical_key"`
	Mode         selection.SelectionMode         `json:"mode"`
	Score        int64                           `json:"score"`
	CountInLimit bool                            `json:"count_in_limit"`
	PolicyOrigin groupPolicyOrigin               `json:"policy_origin"`
	Rank         *int                            `json:"rank"`
	Outcome      selection.GroupSelectionOutcome `json:"outcome"`
	ReasonCode   string                          `json:"reason_code"`
}
type groupPolicyOrigin struct {
	Layer         string `json:"layer"`
	ConfigVersion int64  `json:"config_version"`
}

// canonicalPolicy closes the legacy omitempty representation to DV's nullable
// keys without recalculating effective_selected or sorting submitted entries.
func canonicalPolicy(raw []byte) (string, error) {
	v, err := protocol.ParseStrict(raw)
	if err != nil || v.Kind != protocol.KindObject {
		return "", materialError("policy", err)
	}
	for _, key := range []string{"name_keywords", "provider_defaults"} {
		nested, ok := v.Object[key]
		if !ok {
			v.Object[key] = protocol.Value{Kind: protocol.KindNull}
			continue
		}
		if nested.Kind == protocol.KindObject {
			for _, provider := range []string{"anibt", "mikan"} {
				if _, ok := nested.Object[provider]; !ok {
					nested.Object[provider] = protocol.Value{Kind: protocol.KindNull}
				}
			}
		}
	}
	if err := protocol.Validate("SubgroupsSectionV1", v); err != nil {
		return "", materialError("policy", err)
	}
	return string(protocol.Canonical(v)), nil
}

func constructWitness(c admissionCapture, inputDigest string, limits witnessLimits) (preparationWitness, error) {
	if limits.bytes <= 0 || limits.unionRows <= 0 {
		return preparationWitness{}, errWitnessLimit
	}
	if !validMaterialDigest(inputDigest) || !validMaterialDigest(c.requestHash) || !validMaterialDigest(c.configDigest) || c.policyVersion < 0 {
		return preparationWitness{}, errWitnessMismatch
	}
	var business anime.BusinessSnapshot
	if err := json.Unmarshal([]byte(c.businessJSON), &business); err != nil {
		return preparationWitness{}, errWitnessMismatch
	}
	actualConfig, err := business.Digest()
	if c.configKind == "owner" && c.material.ownerResolution != nil {
		resolved := captureSections(c.material)
		actualConfig, err = resolved.EffectiveDigest()
		business.Sections, business.SectionVersions, business.SectionSchemas = resolved.Sections, resolved.Versions, resolved.SchemaVersions
	}
	if err != nil || actualConfig != c.configDigest || business.SectionVersions[anime.SectionSubgroups] != c.policyVersion {
		return preparationWitness{}, errWitnessMismatch
	}
	frozenPolicy, err := canonicalPolicy(business.Sections[anime.SectionSubgroups])
	if err != nil || frozenPolicy != c.policyCanonical {
		return preparationWitness{}, errWitnessMismatch
	}
	configClaim, err := protocol.ParseStrict(c.qualification.Canonical())
	if err != nil || configClaim.Object["config"].Object["kind"].String != c.configKind {
		return preparationWitness{}, errWitnessMismatch
	}
	o, err := catalog.DecodeGroupObservation([]byte(c.observationCanonical), protocol.Digest(catalog.GroupObservationDigestDomain, []byte(c.observationCanonical)))
	if err != nil || !o.Authoritative() {
		return preparationWitness{}, materialError("observation", err)
	}
	q, err := protocol.ParseStrict(c.qualification.Canonical())
	if err != nil {
		return preparationWitness{}, errWitnessMismatch
	}
	claim := q.Object["observation"]
	if claim.Kind != protocol.KindObject || claim.Object["snapshot_ref"].String != o.SnapshotRef || claim.Object["snapshot_version"].Int != o.SnapshotVersion || claim.Object["observation_digest"].String != protocol.Digest(catalog.GroupObservationDigestDomain, []byte(c.observationCanonical)) || !bytes.Equal(protocol.Canonical(claim.Object["scope"]), mustCanonical(o.Scope)) {
		return preparationWitness{}, errWitnessMismatch
	}
	policyCanonical, err := canonicalPolicy([]byte(c.policyCanonical))
	if err != nil || policyCanonical != c.policyCanonical {
		return preparationWitness{}, materialError("policy", err)
	}
	var policySection anime.SubgroupsSection
	if err := json.Unmarshal([]byte(policyCanonical), &policySection); err != nil {
		return preparationWitness{}, materialError("policy", err)
	}
	if violations := anime.ValidateSubgroupsSection(policySection); len(violations) != 0 {
		return preparationWitness{}, materialError("policy", nil)
	}
	policy, err := selectionapp.DiscoveryPolicyFromSubgroupsSection([]byte(policyCanonical), c.policyVersion)
	if err != nil {
		return preparationWitness{}, materialError("policy", err)
	}
	groups := make([]selection.ObservedGroup, 0, len(o.Groups))
	names := make(map[string]witnessText, len(o.Groups))
	available := make(map[string]bool, len(o.Groups))
	for _, g := range o.Groups {
		name := g.DisplayName
		names[g.GroupRef] = witnessText{State: "known", Value: &name}
		available[g.GroupRef] = *g.Available.Value
		groups = append(groups, selection.ObservedGroup{ExternalID: g.GroupRef, Name: name, Available: *g.Available.Value})
	}
	// The union contains all same-provider explicit intents, including provider
	// default lists. Missing observations remain unavailable, never selected.
	add := func(ref, name string) {
		if _, exists := names[ref]; exists {
			return
		}
		fv := witnessText{State: "absent"}
		if name != "" {
			fv = witnessText{State: "known", Value: &name}
		}
		names[ref] = fv
		groups = append(groups, selection.ObservedGroup{ExternalID: ref, Name: name})
	}
	for _, entry := range policySection.Entries {
		if string(entry.Provider) == string(o.Scope.Provider) {
			add(entry.GroupRef, entry.DisplayName)
		}
	}
	for _, rule := range policy.AnimeRules {
		if rule.Target.ProviderRef != nil && rule.Target.ProviderRef.Provider == o.Scope.Provider {
			add(rule.Target.ProviderRef.ExternalID, "")
		}
	}
	if len(groups) > limits.unionRows {
		return preparationWitness{}, errWitnessLimit
	}
	ranked, err := selection.RankObservedGroups(o.Scope.Provider, groups, policy)
	if err != nil {
		return preparationWitness{}, materialError("ranking", err)
	}
	decisions := make([]groupDecision, 0, len(ranked))
	selected := make([]string, 0)
	for _, result := range ranked {
		layer := "provider_default"
		switch result.Group.PolicySource {
		case selection.SourceAnimeExact:
			if c.configKind == "submitted" {
				layer = "submitted_exact"
			} else {
				layer = "frozen_exact"
			}
		case selection.SourceProviderDefault:
		default:
			return preparationWitness{}, errWitnessMismatch // canonical requires translation.
		}
		if result.Group.Mode == selection.Never && result.Group.CountInLimit {
			return preparationWitness{}, errWitnessMismatch
		}
		decisions = append(decisions, groupDecision{GroupRef: result.Group.Ref.ExternalID, DisplayName: names[result.Group.Ref.ExternalID], Available: available[result.Group.Ref.ExternalID], CanonicalKey: witnessText{State: "absent"}, Mode: result.Group.Mode, Score: int64(result.Group.Score), CountInLimit: result.Group.CountInLimit, PolicyOrigin: groupPolicyOrigin{Layer: layer, ConfigVersion: result.Group.PolicyVersion}, Rank: result.Rank, Outcome: result.Outcome, ReasonCode: result.Reason})
		if result.Outcome == selection.GroupSelected {
			selected = append(selected, result.Group.Ref.ExternalID)
		}
	}
	sort.Strings(selected)
	value := witnessValue{SchemaVersion: 1, Kind: "provider_selection_v1", EvaluatorVersion: anime.EvaluatorVersion, RequestHash: c.requestHash, InputDigest: inputDigest, ConfigDigest: c.configDigest, Observation: o, ObservationDigest: claim.Object["observation_digest"].String, Policy: json.RawMessage(policyCanonical), PolicyDigest: protocol.Digest(policyDigestDomain, []byte(policyCanonical)), Decisions: decisions, SelectedRefs: selected}
	canonical := mustCanonical(value)
	if canonical == nil {
		return preparationWitness{}, errWitnessMismatch
	}
	if len(canonical) > limits.bytes {
		return preparationWitness{}, errWitnessLimit
	}
	return preparationWitness{canonical: string(canonical), digest: protocol.Digest(witnessDigestDomain, canonical)}, nil
}

func mustCanonical(v any) []byte {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	value, err := protocol.ParseStrict(raw)
	if err != nil {
		return nil
	}
	return protocol.Canonical(value)
}

// recheckWitnessTx is the finite A4 gate for the future fresh accepting branch.
// The enclosing key/replay and root writes belong to A3/B1. It never commits,
// writes a graph, exposes a token, or changes 128's witness consumption.
func recheckWitnessTx(ctx context.Context, tx *sql.Tx, c admissionCapture, inputDigest string, witness preparationWitness, limits witnessLimits) (time.Time, error) {
	if tx == nil {
		return time.Time{}, errWitnessMismatch
	}
	var at time.Time
	if err := tx.QueryRowContext(ctx, `SELECT recheck_acquisition_qualification($1::jsonb,$2,$3,$4)`, string(c.qualification.Canonical()), c.requestHash, c.configDigest, c.payloadDigest).Scan(&at); err != nil {
		return time.Time{}, materialError("qualification", err)
	}
	q, err := protocol.ParseStrict(c.qualification.Canonical())
	if err != nil {
		return time.Time{}, errWitnessMismatch
	}
	claim := q.Object["observation"]
	if claim.Kind != protocol.KindObject {
		return time.Time{}, errWitnessMismatch
	}
	locked, err := catalogreader.ReadGroupObservationTx(ctx, tx, claim.Object["snapshot_ref"].String)
	if err != nil {
		return time.Time{}, materialError("observation", err)
	}
	canonical, _, err := locked.Canonical()
	if err != nil || string(canonical) != c.observationCanonical {
		return time.Time{}, errWitnessMismatch
	}
	expected, err := constructWitness(c, inputDigest, limits)
	if err != nil {
		return time.Time{}, err
	}
	// Equality to a fully rebuilt value enforces closed keys, types, identities,
	// digests, complete union, rank, reasons and selected refs, including order.
	if witness != expected {
		return time.Time{}, errWitnessMismatch
	}
	var value witnessValue
	if err := json.Unmarshal([]byte(expected.canonical), &value); err != nil {
		return time.Time{}, errWitnessMismatch
	}
	group := q.Object["feed"].Object["group_ref"].String
	found := false
	for _, ref := range value.SelectedRefs {
		if ref == group {
			found = true
		}
	}
	if !found {
		return time.Time{}, materialError("group_not_selected", nil)
	}
	return at, nil
}
