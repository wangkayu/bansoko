package domain

import (
	"bytes"
	"encoding/json"
)

// businessDigestBytes is the exact byte form the business digest hashes. It
// must equal anime_config_candidate_canonical_json in the database and the
// client canonicalJSON: object keys in byte order, no HTML escaping of < > &,
// and U+2028/U+2029 kept literal. A user value containing any of them (a
// regex named group, an "&" in a keyword) must not make a draft unsaveable.
func businessDigestBytes(raw []byte) ([]byte, error) {
	ordered, err := canonicalBusinessJSON(raw)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(ordered))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return literalLineSeparators(bytes.TrimSuffix(buffer.Bytes(), []byte{'\n'})), nil
}

// literalLineSeparators replaces the \u2028 and \u2029 escapes encoding/json
// always writes with the literal characters, leaving an escaped backslash
// followed by "u2028" text untouched.
func literalLineSeparators(value []byte) []byte {
	result := make([]byte, 0, len(value))
	inString, escaped := false, false
	for index := 0; index < len(value); index++ {
		current := value[index]
		switch {
		case !inString:
			if current == '"' {
				inString = true
			}
		case escaped:
			escaped = false
			if current == 'u' && index+4 < len(value) {
				switch string(value[index+1 : index+5]) {
				case "2028":
					result = append(result[:len(result)-1], "\u2028"...)
					index += 4
					continue
				case "2029":
					result = append(result[:len(result)-1], "\u2029"...)
					index += 4
					continue
				}
			}
		case current == '\\':
			escaped = true
		case current == '"':
			inString = false
		}
		result = append(result, current)
	}
	return result
}

func canonicalBusinessJSON(raw []byte) ([]byte, error) {
	var object map[string]json.RawMessage
	if len(raw) > 0 && raw[0] == '{' {
		if err := json.Unmarshal(raw, &object); err != nil {
			return nil, err
		}
		for key, value := range object {
			canonical, err := canonicalBusinessJSON(value)
			if err != nil {
				return nil, err
			}
			object[key] = canonical
		}
		return json.Marshal(object)
	}
	var array []json.RawMessage
	if len(raw) > 0 && raw[0] == '[' {
		if err := json.Unmarshal(raw, &array); err != nil {
			return nil, err
		}
		for i, value := range array {
			canonical, err := canonicalBusinessJSON(value)
			if err != nil {
				return nil, err
			}
			array[i] = canonical
		}
		return json.Marshal(array)
	}
	return raw, nil
}

// CanonicalJSON returns the canonical byte form of one JSON document: object
// keys in byte order, no whitespace, no HTML escaping. It is byte-equal to
// anime_config_candidate_canonical_json for the same value, so a digest over
// it can be recomputed in SQL.
func CanonicalJSON(raw []byte) ([]byte, error) { return businessDigestBytes(raw) }
