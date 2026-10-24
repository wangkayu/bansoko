package application

import (
	"encoding/json"
	"testing"
)

func TestTypedTargetRenderProjectionExcludesOnlyRowBinding(t *testing.T) {
	render := func(ref, id, confidence string) json.RawMessage {
		return json.RawMessage(`{"binding":{"state":"bound","bound_video_file_ref":"` + ref + `","bound_video_operation_id":"` + id + `","locked_directory":"/library","locked_basename":"a","locked_extension":".mkv","method":"episode_match","confidence":` + confidence + `},"target":"/library/a.ass"}`)
	}
	a, err := typedBusinessRender(render("v1", "a", "0.80"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := typedBusinessRender(render("v1", "b", "0.8"))
	if err != nil {
		t.Fatal(err)
	}
	ca, err := canonicalPlanValue(a)
	if err != nil {
		t.Fatal(err)
	}
	cb, err := canonicalPlanValue(b)
	if err != nil {
		t.Fatal(err)
	}
	if ca != cb {
		t.Fatal("equivalent confidence or row binding changed business digest")
	}
	for _, v := range []json.RawMessage{render("v2", "b", "0.8"), render("v1", "b", "0.6"), render("v1", "b", "0.80000000000000001")} {
		p, err := typedBusinessRender(v)
		if err != nil {
			t.Fatal(err)
		}
		cp, err := canonicalPlanValue(p)
		if err != nil {
			t.Fatal(err)
		}
		if ca == cp {
			t.Fatal("business confidence/ref identity erased")
		}
	}
}
