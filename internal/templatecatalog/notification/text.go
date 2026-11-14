package notification

import (
	"path/filepath"
	"strings"
	"unicode"

	"bansoko/internal/redact"
)

const maxTextFactLength = 512

// Text projects untrusted provider or owner text onto a notification fact. It
// applies the same bounds, redaction and rejection rules to titles frozen by an
// owner and titles resolved later by notification admission.
func Text(value string) any {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > maxTextFactLength {
		return nil
	}
	value, _ = redact.Apply(value, maxTextFactLength, redact.Mask, redact.Redacted)
	value = strings.TrimSpace(value)
	if value == "" || len(value) > maxTextFactLength || filepath.IsAbs(value) || strings.Contains(value, "://") {
		return nil
	}
	for _, marker := range []string{"password=", "passwd=", "token=", "secret=", "credential=", "authorization=", "cookie="} {
		if strings.Contains(strings.ToLower(value), marker) {
			return nil
		}
	}
	for _, char := range value {
		if unicode.IsControl(char) {
			return nil
		}
	}
	return value
}

// TextValue applies Text to a string and maps every other JSON type to null.
func TextValue(value any) any {
	stringValue, ok := value.(string)
	if !ok {
		return nil
	}
	return Text(stringValue)
}
