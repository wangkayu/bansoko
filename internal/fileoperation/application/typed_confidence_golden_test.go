package application

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
)

func TestTypedBindingConfidenceGoldenCanonicalBytes(t *testing.T) {
	raw := json.RawMessage(`{"binding":{"state":"bound","bound_video_file_ref":"v1","bound_video_operation_id":"row-id","locked_directory":"/library","locked_basename":"a","locked_extension":".mkv","method":"episode_match","confidence":0.80000000000000001},"target":"/library/a.ass"}`)
	value, err := typedBusinessRender(raw)
	if err != nil {
		t.Fatal(err)
	}
	got, err := canonicalPlanValue(value)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"binding":{"bound_video_file_ref":"v1","confidence":"0.80000000000000001","locked_basename":"a","locked_directory":"/library","locked_extension":".mkv","method":"episode_match","schema_version":1,"state":"bound"},"target":"/library/a.ass"}`
	if got != want {
		t.Fatalf("canonical bytes %s", got)
	}
	hash := sha256.Sum256(append([]byte("acquisition-targets:v1\x00"), []byte(got)...))
	if hex.EncodeToString(hash[:]) != "65be198deb4136dcfd8c7641d1da96caec6e5fd5ab8fe1ab3bbd4286d54599c4" {
		t.Fatalf("golden hash %x", hash)
	}
}
