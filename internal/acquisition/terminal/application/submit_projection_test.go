package application

import (
	"bansoko/internal/workitems"
	"bytes"
	"encoding/json"
	"testing"
)

func TestSubmitProjectionPreservesEncoderBytes(t *testing.T) {
	ref := &workitems.OneShotRef{MarkerID: "marker-1", CorrelationID: "corr-1", AdmissionEpoch: 1}
	for _, one := range []*workitems.OneShotRef{nil, ref} {
		p := submitProjection{frozenInput: json.RawMessage(`{ "legacy": "input" }`), frozenPolicy: json.RawMessage(`{"legacy":"policy"}`), effectKey: "qb:rel-1", correlation: "corr-1", causationType: "release", causationID: "rel-1", trigger: "one_shot_acquire", oneShot: one}
		got, snapshot, err := projectSubmit(p)
		if err != nil {
			t.Fatal(err)
		}
		want, err := workitems.SubmitInputSnapshot(p.trigger, one)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(snapshot, want) || !bytes.Equal(got.frozenInput, p.frozenInput) || !bytes.Equal(got.frozenPolicy, p.frozenPolicy) {
			t.Fatal("submit projection bytes changed")
		}
		p.frozenInput[0] = 'x'
		if got.frozenInput[0] == 'x' {
			t.Fatal("projection aliases caller")
		}
	}
}
