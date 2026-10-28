package application

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"testing"

	"bansoko/internal/postprocessing/naming"
	"bansoko/internal/workitems"
)

// The hold lists the gate's items by name — the BGM binding, the TMDB binding,
// then the template items — drops items that block nothing, caps each count
// and keeps at most planConfigHoldItems items.
func TestConfigHoldOrdersAndBoundsTheItems(t *testing.T) {
	hold := configHold(map[string]int{
		"template_variable_undefined:rt.title": 2,
		"binding_missing:tmdb":                 3,
		"binding_missing:bgm":                  3,
		"template_variable_undefined:anibt.x":  0,
		"template_variable_undefined:vars.a":   2_000_000,
	})
	want := []configHoldItem{
		{Item: "binding_missing:bgm", Files: 3},
		{Item: "binding_missing:tmdb", Files: 3},
		{Item: "template_variable_undefined:rt.title", Files: 2},
		{Item: "template_variable_undefined:vars.a", Files: 999999},
	}
	if !reflect.DeepEqual(hold, want) {
		t.Fatalf("hold = %+v, want %+v", hold, want)
	}

	many := map[string]int{}
	for i := range planConfigHoldItems + 4 {
		many[fmt.Sprintf("template_variable_undefined:vars.v%02d", i)] = 1
	}
	if bounded := configHold(many); len(bounded) != planConfigHoldItems || bounded[0].Item != "template_variable_undefined:vars.v00" {
		t.Fatalf("bounded hold = %+v", bounded)
	}
	if empty := configHold(map[string]int{"binding_missing:bgm": 0}); len(empty) != 0 {
		t.Fatalf("an item that blocks nothing is no hold: %+v", empty)
	}
}

type refusingConfigGate struct{ t *testing.T }

func (g refusingConfigGate) CurrentConfigHold(context.Context, string, []naming.NamingContext) (map[string]int, int, error) {
	g.t.Fatal("the configuration gate ran")
	return nil, 0, nil
}

// A one-shot plan never waits for configuration: the gate is not consulted
// and the claim plans. Without a wired gate no plan holds anything.
func TestConfigurationGateExemptsOneShotPlans(t *testing.T) {
	service := &Service{}
	service.SetCurrentConfigGate(refusingConfigGate{t})
	plan := &postProcessingPlanRow{id: "plan", state: "queued", oneShotID: sql.NullString{String: "one-shot", Valid: true}}
	if out, err := service.configurationGate(context.Background(), workitems.Claim{}, plan); out != nil || err != nil {
		t.Fatalf("a one-shot plan waited: %+v %v", out, err)
	}

	unwired := &Service{}
	if hold, blocked, err := unwired.currentConfigHold(context.Background(), &postProcessingPlanRow{id: "plan", state: "queued"}); hold != nil || blocked != 0 || err != nil {
		t.Fatalf("an unwired gate held %+v (%d): %v", hold, blocked, err)
	}
}
