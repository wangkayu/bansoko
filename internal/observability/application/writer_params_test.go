package application

import (
	"strings"
	"testing"

	observabilityport "bansoko/internal/observability/port"
)

// TestEncodeMessageParamsBounds pins the param contract of the writer: only
// declared names, of the declared scalar type, bounded, required ones present;
// an empty string is an absent optional param and no params encode as {}.
func TestEncodeMessageParamsBounds(t *testing.T) {
	ok := []struct {
		name   string
		code   observabilityport.MessageCode
		values map[string]any
		want   string
	}{
		{"no params", observabilityport.MessageArtifactVerified, nil, `{}`},
		{"optional string", observabilityport.MessageFileImported, map[string]any{"file_name": "a.mkv"}, `{"file_name":"a.mkv"}`},
		{"empty optional string dropped", observabilityport.MessageFileImported, map[string]any{"file_name": ""}, `{}`},
		{"required integer", observabilityport.MessageRSSNewContent, map[string]any{"count": 3}, `{"count":3}`},
		{"integer zero", observabilityport.MessageRSSNewContent, map[string]any{"count": int64(0)}, `{"count":0}`},
		{"string at the bound", observabilityport.MessageDownloadSubmitted, map[string]any{"name": strings.Repeat("a", observabilityport.MaxMessageParamBytes)}, `{"name":"` + strings.Repeat("a", observabilityport.MaxMessageParamBytes) + `"}`},
	}
	for _, tc := range ok {
		t.Run(tc.name, func(t *testing.T) {
			got, err := encodeMessageParams(tc.code, tc.values)
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			if string(got) != tc.want {
				t.Fatalf("encoded %s, want %s", got, tc.want)
			}
		})
	}
	bad := []struct {
		name   string
		code   observabilityport.MessageCode
		values map[string]any
	}{
		{"unregistered code", "bogus", nil},
		{"undeclared name", observabilityport.MessageArtifactVerified, map[string]any{"name": "x"}},
		{"name of another message", observabilityport.MessageFileImported, map[string]any{"name": "x"}},
		{"string given as integer", observabilityport.MessageFileImported, map[string]any{"file_name": 3}},
		{"integer given as string", observabilityport.MessageRSSNewContent, map[string]any{"count": "3"}},
		{"integer given as float", observabilityport.MessageRSSNewContent, map[string]any{"count": 3.0}},
		{"negative integer", observabilityport.MessageRSSNewContent, map[string]any{"count": -1}},
		{"missing required", observabilityport.MessageRSSNewContent, nil},
		{"string over the bound", observabilityport.MessageDownloadSubmitted, map[string]any{"name": strings.Repeat("a", observabilityport.MaxMessageParamBytes+1)}},
		{"NUL in string", observabilityport.MessageDownloadSubmitted, map[string]any{"name": "a\x00b"}},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := encodeMessageParams(tc.code, tc.values); err == nil {
				t.Fatalf("expected rejection, encoded %s", got)
			}
		})
	}
}
