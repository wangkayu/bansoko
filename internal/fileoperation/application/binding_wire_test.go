package application

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSubtitleBindingWireRejectsNoncanonicalConfidence(t *testing.T) {
	base := `{"schema_version":1,"state":"bound","bound_video_file_ref":"video","locked_directory":"/library","locked_basename":"name","locked_extension":".mkv","method":"episode_match","confidence":CONF}`
	for _, c := range []string{`"0"`, `"1"`, `"0.8"`, `"0.6"`, `"0.` + strings.Repeat("1", 64) + `"`} {
		if err := validateSubtitleRenderBindingV1(json.RawMessage(strings.Replace(base, "CONF", c, 1))); err != nil {
			t.Fatalf("%s %v", c, err)
		}
	}
	for _, c := range []string{`"0.80"`, `"1.0"`, `"8e-1"`, `"-0"`, `"01"`, `" 0.8"`, `"0."`, `0`, `1`, `0.8`, `1e0`, `-0`, `null`, `"0.` + strings.Repeat("1", 65) + `"`} {
		if err := validateSubtitleRenderBindingV1(json.RawMessage(strings.Replace(base, "CONF", c, 1))); err == nil {
			t.Fatalf("accepted %s", c)
		}
	}
	for _, raw := range []string{strings.Replace(base, `"schema_version":1,`, "", 1), strings.Replace(base, `"schema_version":1`, `"schema_version":2`, 1), strings.Replace(base, `"method":"episode_match"`, `"method":"unknown"`, 1), strings.Replace(base, `"state":"bound"`, `"state":"bound","extra":1`, 1), strings.Replace(base, `"state":"bound"`, `"state":"bound","state":"bound"`, 1)} {
		if err := validateSubtitleRenderBindingV1(json.RawMessage(strings.Replace(raw, "CONF", `"0.8"`, 1))); err == nil {
			t.Fatalf("accepted shape %s", raw)
		}
	}
}
