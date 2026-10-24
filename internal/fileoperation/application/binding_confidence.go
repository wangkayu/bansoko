package application

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

var sourceConfidenceSyntax = regexp.MustCompile(`^(0|1)(\.[0-9]+)?$`)
var wireConfidenceSyntax = regexp.MustCompile(`^(0|1|0\.[0-9]{0,63}[1-9])$`)

func projectConfidenceNumber(raw json.RawMessage) (string, error) {
	text := string(raw)
	if len(text) > 128 || !sourceConfidenceSyntax.MatchString(text) {
		return "", fmt.Errorf("BINDING_CONFIDENCE_INVALID at binding/confidence")
	}
	if strings.Contains(text, ".") {
		text = strings.TrimRight(text, "0")
		text = strings.TrimSuffix(text, ".")
	}
	if !wireConfidenceSyntax.MatchString(text) {
		return "", fmt.Errorf("BINDING_CONFIDENCE_INVALID at binding/confidence")
	}
	return text, nil
}

// Source tokens remain numbers on execution/API snapshots; no float roundtrip.
func bindingConfidenceToken(raw []byte) (json.RawMessage, error) {
	if err := validateBindingJSON(raw); err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	if string(fields["version"]) != `"subtitle-binding-1"` {
		return nil, fmt.Errorf("BINDING_RENDER_SOURCE_UNSUPPORTED at binding/version")
	}
	var method string
	if err := json.Unmarshal(fields["method"], &method); err != nil {
		return nil, err
	}
	if method != bindingMethodBasenameMatch && method != bindingMethodEpisodeMatch && method != bindingMethodUniqueVideo {
		return nil, fmt.Errorf("BINDING_RENDER_SOURCE_UNSUPPORTED at binding/method")
	}
	if _, err := projectConfidenceNumber(fields["confidence"]); err != nil {
		return nil, err
	}
	return fields["confidence"], nil
}
