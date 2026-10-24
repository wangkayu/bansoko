package domain

import (
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"bansoko/internal/filekinds"
)

const (
	RegexFilterEngine         = "go-re2-v1"
	MaxFilterPatternBytes     = 4096
	MaxFilterTitleBytes       = 8192 // Align with the selected parser input budget.
	MaxFilterPathBytes        = 4096
	MaxFilterInventoryEntries = 10000
	MaxFilterInventoryBytes   = 8 << 20
)

// RegexFilter is the next filter contract, not a conversion of legacy keyword rules.
// Release and file policies are independent instances of this shape.
type RegexFilter struct {
	MustContain string `json:"must_contain"`
	NotIn       string `json:"not_in"`
}

// ValidateRegexFilterJSON rejects missing/null fields and all legacy inputs.
func ValidateRegexFilterJSON(raw json.RawMessage) error {
	var value struct {
		MustContain *string `json:"must_contain"`
		NotIn       *string `json:"not_in"`
	}
	if err := decodeStrictJSON(raw, &value); err != nil {
		return fmt.Errorf("filter must contain only must_contain and not_in: %w", err)
	}
	if value.MustContain == nil || value.NotIn == nil {
		return fmt.Errorf("must_contain and not_in are required strings")
	}
	_, err := CompileRegexFilter(RegexFilter{MustContain: *value.MustContain, NotIn: *value.NotIn})
	return err
}

// ErrCodeFileRulePathSeparator is the typed validation code for a file rule
// that contains "/". File rules match only the filename of a first-level
// video, and a manifest filename never contains "/", so such a pattern can
// never match. "\" stays allowed: it is the regex escape (`\.`, `\d`).
const ErrCodeFileRulePathSeparator = "FILE_RULE_PATH_SEPARATOR"

// FileRuleError is the typed save-time refusal of a file rule.
type FileRuleError struct {
	Code  string
	Field string
}

func (e *FileRuleError) Error() string {
	return fmt.Sprintf("file rule %s must not contain \"/\": file rules match the filename only", e.Field)
}

// ValidateFileRulesJSON is ValidateRegexFilterJSON for the file-rules section:
// besides the shared shape it refuses any rule containing `/`.
func ValidateFileRulesJSON(raw json.RawMessage) error {
	if err := ValidateRegexFilterJSON(raw); err != nil {
		return err
	}
	var value RegexFilter
	if err := decodeStrictJSON(raw, &value); err != nil {
		return err
	}
	for _, field := range []struct{ name, pattern string }{{"must_contain", value.MustContain}, {"not_in", value.NotIn}} {
		if strings.Contains(field.pattern, "/") {
			return &FileRuleError{Code: ErrCodeFileRulePathSeparator, Field: field.name}
		}
	}
	return nil
}

type CompiledRegexFilter struct {
	must, exclude *regexp.Regexp
	valid         bool
}

type RegexFilterDecision struct {
	Allowed bool
	Reason  string
	Input   string // Exact release title, or the filename a file rule matched.
}

func CompileRegexFilter(policy RegexFilter) (CompiledRegexFilter, error) {
	result := CompiledRegexFilter{}
	for _, field := range []struct {
		name, pattern string
		target        **regexp.Regexp
	}{
		{"must_contain", policy.MustContain, &result.must}, {"not_in", policy.NotIn, &result.exclude},
	} {
		if !utf8.ValidString(field.pattern) || len(field.pattern) > MaxFilterPatternBytes {
			return CompiledRegexFilter{}, fmt.Errorf("%s: invalid UTF-8 or pattern exceeds %d bytes", field.name, MaxFilterPatternBytes)
		}
		if field.pattern == "" {
			continue
		}
		compiled, err := regexp.Compile(field.pattern)
		if err != nil {
			return CompiledRegexFilter{}, fmt.Errorf("%s: invalid Go RE2 pattern: %w", field.name, err)
		}
		*field.target = compiled
	}
	result.valid = true
	return result, nil
}

func (f CompiledRegexFilter) match(input string) RegexFilterDecision {
	d := RegexFilterDecision{Input: input}
	switch {
	case !f.valid:
		d.Reason = "filter_policy_invalid"
	case f.exclude != nil && f.exclude.MatchString(input):
		d.Reason = "not_in_matched"
	case f.must != nil && !f.must.MatchString(input):
		d.Reason = "must_contain_not_matched"
	default:
		d.Allowed = true
		d.Reason = "allowed"
	}
	return d
}

func (f CompiledRegexFilter) MatchReleaseTitle(title string) RegexFilterDecision {
	if !utf8.ValidString(title) || len(title) > MaxFilterTitleBytes {
		return RegexFilterDecision{Reason: "filter_input_invalid"}
	}
	return f.match(title)
}

// NormalizeFilterRelativePath validates BEFORE normalization; it never cleans
// traversal away. Separators are protocol-defined, not host-OS-dependent.
func NormalizeFilterRelativePath(raw string) (string, error) {
	if raw == "" || !utf8.ValidString(raw) || len(raw) > MaxFilterPathBytes {
		return "", fmt.Errorf("invalid or oversized relative path")
	}
	for _, r := range raw {
		if unicode.IsControl(r) {
			return "", fmt.Errorf("control character in relative path")
		}
	}
	normalized := strings.ReplaceAll(raw, `\`, "/")
	if strings.HasPrefix(normalized, "/") {
		return "", fmt.Errorf("absolute relative path")
	}
	for _, segment := range strings.Split(normalized, "/") {
		if segment == "" || segment == "." || segment == ".." || strings.Contains(segment, ":") {
			return "", fmt.Errorf("unsafe relative path segment")
		}
	}
	return normalized, nil
}

// MatchFilename applies the file rules to the filename of one first-level
// video. The rules never see a directory or the torrent name: only
// path.Base of the manifest path is matched.
func (f CompiledRegexFilter) MatchFilename(manifestPath string) RegexFilterDecision {
	normalized, err := NormalizeFilterRelativePath(manifestPath)
	if err != nil {
		return RegexFilterDecision{Reason: "filter_path_invalid"}
	}
	return f.match(path.Base(normalized))
}

// OrganizationPathSelection preserves full inventory decisions and selects only
// organization ordinals. DownloadAllowed means download the WHOLE inventory;
// it never means applying per-file qB priorities. Owners must map ordinals back
// to immutable manifest file_refs when freezing the decision.
type OrganizationPathSelection struct {
	Decisions        []RegexFilterDecision
	SelectedOrdinals []int
	DownloadAllowed  bool
	Reason           string
}

// SelectOrganizationFiles decides every manifest path of one torrent. A file
// below the first level is ignored_nested and any file that is neither video
// nor subtitle (under the frozen kinds version) is ignored_kind; both are
// read-only and never parsed. File rules then apply to first-level videos
// only, by filename; first-level subtitles are not subject to file rules and
// follow a selected video by name prefix. DownloadAllowed needs at least one
// selected video.
func (f CompiledRegexFilter) SelectOrganizationFiles(fileKindsVersion string, paths []string) (OrganizationPathSelection, error) {
	if !f.valid {
		return OrganizationPathSelection{}, fmt.Errorf("invalid compiled filter")
	}
	if len(paths) > MaxFilterInventoryEntries {
		return OrganizationPathSelection{}, fmt.Errorf("inventory entry budget exceeded")
	}
	result := OrganizationPathSelection{Decisions: make([]RegexFilterDecision, 0, len(paths)), SelectedOrdinals: []int{}, Reason: "no_applicable_files"}
	seen := make(map[string]struct{}, len(paths))
	total := 0
	for index, raw := range paths {
		total += len(raw)
		if total > MaxFilterInventoryBytes {
			return OrganizationPathSelection{}, fmt.Errorf("inventory byte budget exceeded")
		}
		normalized, err := NormalizeFilterRelativePath(raw)
		if err != nil {
			return OrganizationPathSelection{}, fmt.Errorf("entry %d: %w", index, err)
		}
		if _, exists := seen[normalized]; exists {
			return OrganizationPathSelection{}, fmt.Errorf("entry %d: duplicate normalized relative path", index)
		}
		seen[normalized] = struct{}{}
		kind, ignored := filekinds.Eligibility(fileKindsVersion, normalized)
		var d RegexFilterDecision
		switch {
		case ignored != "":
			d = RegexFilterDecision{Reason: ignored, Input: path.Base(normalized)}
		case kind == filekinds.KindSubtitle:
			d = RegexFilterDecision{Allowed: true, Reason: "allowed", Input: path.Base(normalized)}
		default:
			d = f.MatchFilename(normalized)
			if d.Allowed {
				result.DownloadAllowed = true
				result.Reason = "allowed"
			}
		}
		if d.Allowed {
			result.SelectedOrdinals = append(result.SelectedOrdinals, index)
		}
		result.Decisions = append(result.Decisions, d)
	}
	return result, nil
}
