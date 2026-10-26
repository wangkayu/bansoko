package domain

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Release-note bounds. They are part of the signed manifest contract: the
// publisher refuses to sign more, and a verifier refuses a manifest that
// carries more, so a hostile or accidental oversize body never reaches the UI.
const (
	// MaxReleaseNotesEntries is how many versions of notes one manifest may
	// carry: the release itself plus the versions before it, so an instance
	// that skips versions still sees what changed in between.
	MaxReleaseNotesEntries = 10
	// MaxReleaseNotesLanguageBytes bounds one language body of one version.
	MaxReleaseNotesLanguageBytes = 16 << 10
	// MaxReleaseNotesTotalBytes bounds all bodies of all entries together.
	MaxReleaseNotesTotalBytes = 64 << 10
)

// ReleaseNotesText is one version's notes in every required language. Both
// languages are mandatory; strict decoding refuses any other language key.
type ReleaseNotesText struct {
	ZhCN string `json:"zh-CN"`
	En   string `json:"en"`
}

// ReleaseNotesEntry is one version's bilingual notes inside a signed manifest.
type ReleaseNotesEntry struct {
	Version     string           `json:"version"`
	PublishedAt time.Time        `json:"published_at"`
	Notes       ReleaseNotesText `json:"notes"`
}

// ValidateReleaseNotes checks the structural release-note contract of a
// manifest whose version is manifestVersion. An absent list is valid (test
// builds and historical manifests carry none). A present list names the
// manifest's own version first, then strictly older versions, newest first.
func ValidateReleaseNotes(manifestVersion string, entries []ReleaseNotesEntry) error {
	if len(entries) == 0 {
		return nil
	}
	if len(entries) > MaxReleaseNotesEntries {
		return fmt.Errorf("release notes carry %d versions; at most %d are allowed", len(entries), MaxReleaseNotesEntries)
	}
	if entries[0].Version != manifestVersion {
		return errors.New("release notes must start with the manifest's own version")
	}
	total := 0
	var previous SemVer
	for index, entry := range entries {
		version, err := ParseSemVer(entry.Version)
		if err != nil {
			return fmt.Errorf("release notes version %q: %w", entry.Version, err)
		}
		if index > 0 && CompareSemVer(version, previous) >= 0 {
			return errors.New("release notes must list strictly older versions, newest first")
		}
		previous = version
		if entry.PublishedAt.IsZero() {
			return fmt.Errorf("release notes of %s lack published_at", entry.Version)
		}
		for _, text := range [][2]string{{"zh-CN", entry.Notes.ZhCN}, {"en", entry.Notes.En}} {
			language, body := text[0], text[1]
			if strings.TrimSpace(body) == "" {
				return fmt.Errorf("release notes of %s lack the %s text", entry.Version, language)
			}
			if len(body) > MaxReleaseNotesLanguageBytes {
				return fmt.Errorf("release notes of %s exceed %d bytes in %s", entry.Version, MaxReleaseNotesLanguageBytes, language)
			}
			total += len(body)
		}
	}
	if total > MaxReleaseNotesTotalBytes {
		return fmt.Errorf("release notes exceed %d bytes in total", MaxReleaseNotesTotalBytes)
	}
	return nil
}

// SemVer is a parsed semantic version (https://semver.org, build metadata
// rejected). Only precedence matters here, so the raw prerelease identifiers
// are kept as written.
type SemVer struct {
	Major, Minor, Patch uint64
	Prerelease          []string
}

// ParseSemVer parses MAJOR.MINOR.PATCH with an optional -prerelease. A leading
// "v" is not accepted: release versions are written without it and the tag
// adds it.
func ParseSemVer(value string) (SemVer, error) {
	if value == "" || strings.ContainsAny(value, " \t\r\n+") {
		return SemVer{}, errors.New("not a semantic version")
	}
	core, prerelease, hasPrerelease := strings.Cut(value, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return SemVer{}, errors.New("not a semantic version")
	}
	var numbers [3]uint64
	for index, part := range parts {
		number, err := parseSemVerNumber(part)
		if err != nil {
			return SemVer{}, err
		}
		numbers[index] = number
	}
	result := SemVer{Major: numbers[0], Minor: numbers[1], Patch: numbers[2]}
	if hasPrerelease {
		if prerelease == "" {
			return SemVer{}, errors.New("empty prerelease")
		}
		for _, identifier := range strings.Split(prerelease, ".") {
			if identifier == "" {
				return SemVer{}, errors.New("empty prerelease identifier")
			}
			for _, r := range identifier {
				if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r == '-') {
					return SemVer{}, errors.New("invalid prerelease identifier")
				}
			}
			if isNumeric(identifier) && len(identifier) > 1 && identifier[0] == '0' {
				return SemVer{}, errors.New("numeric prerelease identifier has a leading zero")
			}
			result.Prerelease = append(result.Prerelease, identifier)
		}
	}
	return result, nil
}

// IsRelease reports a plain MAJOR.MINOR.PATCH version without prerelease.
func (v SemVer) IsRelease() bool { return len(v.Prerelease) == 0 }

func parseSemVerNumber(part string) (uint64, error) {
	if part == "" || !isNumeric(part) || (len(part) > 1 && part[0] == '0') {
		return 0, errors.New("not a semantic version")
	}
	return strconv.ParseUint(part, 10, 64)
}

func isNumeric(value string) bool {
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return value != ""
}

// CompareSemVer orders two versions by semantic-version precedence and returns
// -1, 0 or 1.
func CompareSemVer(a, b SemVer) int {
	for _, pair := range [][2]uint64{{a.Major, b.Major}, {a.Minor, b.Minor}, {a.Patch, b.Patch}} {
		if pair[0] != pair[1] {
			if pair[0] < pair[1] {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(a.Prerelease) == 0 && len(b.Prerelease) == 0:
		return 0
	case len(a.Prerelease) == 0:
		return 1
	case len(b.Prerelease) == 0:
		return -1
	}
	for index := 0; index < len(a.Prerelease) && index < len(b.Prerelease); index++ {
		x, y := a.Prerelease[index], b.Prerelease[index]
		if x == y {
			continue
		}
		xNumeric, yNumeric := isNumeric(x), isNumeric(y)
		switch {
		case xNumeric && yNumeric:
			xn, _ := strconv.ParseUint(x, 10, 64)
			yn, _ := strconv.ParseUint(y, 10, 64)
			if xn < yn {
				return -1
			}
			return 1
		case xNumeric:
			return -1
		case yNumeric:
			return 1
		case x < y:
			return -1
		default:
			return 1
		}
	}
	switch {
	case len(a.Prerelease) < len(b.Prerelease):
		return -1
	case len(a.Prerelease) > len(b.Prerelease):
		return 1
	}
	return 0
}

// ReleaseNotesNewerThan returns the entries newer than the running version,
// newest first. A running version that is not a semantic version (a local
// development build) cannot be ordered, so every entry is returned.
func ReleaseNotesNewerThan(current string, entries []ReleaseNotesEntry) []ReleaseNotesEntry {
	running, err := ParseSemVer(current)
	result := make([]ReleaseNotesEntry, 0, len(entries))
	for _, entry := range entries {
		version, parseErr := ParseSemVer(entry.Version)
		if parseErr != nil {
			continue
		}
		if err == nil && CompareSemVer(version, running) <= 0 {
			continue
		}
		result = append(result, entry)
	}
	return result
}
