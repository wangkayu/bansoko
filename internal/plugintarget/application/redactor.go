package application

import (
	"encoding/json"
	"strings"
	"unicode/utf8"

	"bansoko/internal/redact"
)

type secretRedactor struct {
	values []string
}

func newSecretRedactor(raw string) secretRedactor {
	values := []string{}
	if raw != "" {
		values = append(values, raw)
		var parsed any
		if json.Unmarshal([]byte(raw), &parsed) == nil {
			collectSecretStrings(parsed, &values)
		}
	}
	return secretRedactor{values: values}
}

func collectSecretStrings(value any, values *[]string) {
	switch item := value.(type) {
	case map[string]any:
		for _, child := range item {
			collectSecretStrings(child, values)
		}
	case []any:
		for _, child := range item {
			collectSecretStrings(child, values)
		}
	case string:
		if item != "" {
			*values = append(*values, item)
		}
	}
}

func (r secretRedactor) text(value string, limit int) string {
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	for _, secret := range r.values {
		value = strings.ReplaceAll(value, secret, "[redacted]")
	}
	value = strings.TrimSpace(value)
	if _, sensitive := redact.Apply(value, 4096, redact.Reject, ""); sensitive {
		value = "delivery failed with a redacted error"
	}
	return truncateUTF8(value, limit)
}

func truncate(value string, limit int) string {
	return truncateUTF8(value, limit)
}

func truncateUTF8(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(value) <= limit {
		return value
	}
	value = value[:limit]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}
