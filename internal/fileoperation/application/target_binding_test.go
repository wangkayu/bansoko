package application

import (
	"database/sql"
	"encoding/json"
	"testing"

	"bansoko/internal/postprocessing/naming"
)

// TestMergeFrozenSnapshotKeepsBothContracts pins the shape of the snapshots an
// operation carries when it is created from a persisted acquisition target. The
// target's own snapshot only describes the target contract (`operation`,
// `collision_policy`, `parser`); the operation additionally has to carry the
// frozen naming configuration, because a maintenance re-render decodes its
// naming rules from that snapshot. Copying the target snapshot verbatim left
// every such operation undecodable and silently excluded every placed file from
// alignment. Neither snapshot carries a library anchor: the target the naming
// render produced is already absolute.
func TestMergeFrozenSnapshotKeepsBothContracts(t *testing.T) {
	targetConfig := json.RawMessage(`{"parser":{},"operation":"move","collision_policy":"block"}`)
	operationConfig, err := marshalConfigSnapshot(effectiveConfig{
		naming:   naming.DefaultNamingRules(),
		versions: map[string]int64{"naming_rules": 3},
	}, nil, nil)
	if err != nil {
		t.Fatalf("marshal the operation config snapshot: %v", err)
	}

	merged, err := mergeFrozenSnapshot(operationConfig, targetConfig)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	var decoded struct {
		NamingRules     naming.NamingRules `json:"naming_rules"`
		Operation       string             `json:"operation"`
		CollisionPolicy string             `json:"collision_policy"`
		Rules           int64              `json:"rules_version"`
	}
	if err := json.Unmarshal(merged, &decoded); err != nil {
		t.Fatalf("decode merged config snapshot: %v", err)
	}
	if decoded.NamingRules.Language != naming.Language {
		t.Fatalf("the merge must keep the frozen naming rules decodable, got %+v", decoded)
	}
	if decoded.Operation != "move" || decoded.CollisionPolicy != "block" {
		t.Fatalf("the merge must keep the target contract, got %+v", decoded)
	}
	if decoded.Rules != 3 {
		t.Fatalf("the operation snapshot must win on its own keys, got rules_version=%d", decoded.Rules)
	}

	renderSnapshot, err := mergeFrozenSnapshot(map[string]any{"target": "/library/Show/Season 01/E01.mkv", "binding_facts": map[string]any{"season": 1}}, json.RawMessage(`{"renderer":"naming","target":"/library/Show/Season 01/E01.mkv"}`))
	if err != nil {
		t.Fatalf("merge render snapshot: %v", err)
	}
	var render struct {
		Target       string         `json:"target"`
		Renderer     string         `json:"renderer"`
		BindingFacts map[string]any `json:"binding_facts"`
	}
	if err := json.Unmarshal(renderSnapshot, &render); err != nil {
		t.Fatalf("decode merged render snapshot: %v", err)
	}
	if render.BindingFacts == nil || render.Renderer != "naming" {
		t.Fatalf("the merge lost a contract: %+v", render)
	}
}

// TestArtifactBindingFactsReadsThePreparedMetadata pins that the binding the
// effect renders with is recorded even when the target came from a persisted
// acquisition target: the maintenance re-render has no other source for a
// binding set after the release media-fact snapshot was frozen.
func TestArtifactBindingFactsReadsThePreparedMetadata(t *testing.T) {
	artifact := artifactContext{
		tmdbWorkID:    sql.NullString{String: "work-1", Valid: true},
		tmdbMediaKind: sql.NullString{String: "tv", Valid: true},
		tmdbSeason:    sql.NullInt64{Int64: 2, Valid: true},
		metadataBGM:   sql.NullString{String: "bgm-1", Valid: true},
	}
	facts := artifactBindingFacts(artifact)
	if facts["media_kind"] != "tv" || facts["season"] != 2 || facts["tmdb_work_id"] != "work-1" || facts["bgm_work_id"] != "bgm-1" {
		t.Fatalf("binding facts = %+v", facts)
	}
	empty := artifactBindingFacts(artifactContext{})
	if empty["media_kind"] != "" || empty["season"] != 0 {
		t.Fatalf("an unbound artifact must record empty facts, got %+v", empty)
	}
}
