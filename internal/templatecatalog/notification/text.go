package notification

import (
	"path/filepath"
	"strings"
	"unicode"

	"bansoko/internal/redact"
)

const (
	maxTextFactLength = 512
	// maxPathFactLength bounds an output path fact. A path is reported as it was
	// placed, so it is not run through the free-text mask that would reject its
	// leading slash; the structural checks below still apply.
	maxPathFactLength = 4096
)

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

// Path projects an output path fact (out.dir, out.path) exactly as the
// placement reported it. Unlike Text, an absolute path is the value the owner
// asked for, so it is kept rather than treated as private free text. The
// projection still bounds the value and rejects control characters, URL
// schemes and relative paths. A legitimate name may contain "=" or a word such
// as "token", so the credential-shaped text mask does not apply here; the SQL
// twin is notification_path_text (migration 136).
func Path(value string) any {
	if value == "" || len(value) > maxPathFactLength || !filepath.IsAbs(value) {
		return nil
	}
	if strings.Contains(value, "://") {
		return nil
	}
	for _, char := range value {
		if unicode.IsControl(char) {
			return nil
		}
	}
	return value
}

// Name projects a placed file name (out.name and each out.subtitles entry)
// exactly as the placement reported it. It is a single component, not a path:
// separators, URL schemes and control characters are rejected, while a
// legitimate name containing "=" or a word such as "token" is kept. The SQL
// twin is notification_name_text (migration 136).
func Name(value string) any {
	if value == "" || len(value) > maxPathFactLength || strings.Contains(value, "/") {
		return nil
	}
	if strings.Contains(value, "://") {
		return nil
	}
	for _, char := range value {
		if unicode.IsControl(char) {
			return nil
		}
	}
	return value
}
