package releasecontract

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

	hardeningdomain "bansoko/internal/hardening/domain"
)

// The test lane publishes every green main build to its own signed channel.
// Its identity is a SemVer prerelease of the next formal version:
//
//	<X.Y.Z>-test.<run>
//
// <run> is the workflow run number, which is also the release sequence, so
// two test builds of the same core order by run, and every test build orders
// below the formal release of its core. The core is the VERSION file's core
// when that is newer than every released version, otherwise the patch after
// the newest released version: a test build made after a formal release is
// always newer than that release by both SemVer and sequence, without anyone
// having to remember to bump VERSION first.
const TestPrereleaseLabel = "test"

var testVersionPattern = regexp.MustCompile(`^((?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*))-test\.([1-9][0-9]*)$`)

// ParseTestVersion splits a test-lane version into its core and run number.
func ParseTestVersion(version string) (core string, run int64, ok bool) {
	match := testVersionPattern.FindStringSubmatch(version)
	if match == nil {
		return "", 0, false
	}
	run, err := strconv.ParseInt(match[2], 10, 64)
	if err != nil {
		return "", 0, false
	}
	return match[1], run, true
}

// IsTestVersion reports a test-lane version <X.Y.Z>-test.<run>.
func IsTestVersion(version string) bool {
	_, _, ok := ParseTestVersion(version)
	return ok
}

// TestVersion derives the test-lane version of one main build from the
// VERSION file, the released-version index and the run number.
func TestVersion(versionFile, notesDir string, run int64) (string, error) {
	if run < 1 {
		return "", errors.New("test version requires a positive run number")
	}
	data, err := os.ReadFile(versionFile)
	if err != nil {
		return "", fmt.Errorf("read VERSION: %w", err)
	}
	declared, err := hardeningdomain.ParseSemVer(strings.TrimSpace(string(data)))
	if err != nil {
		return "", fmt.Errorf("VERSION %q is not a semantic version: %w", strings.TrimSpace(string(data)), err)
	}
	core := hardeningdomain.SemVer{Major: declared.Major, Minor: declared.Minor, Patch: declared.Patch}
	index, err := readNotesIndex(notesDir)
	if err != nil {
		return "", err
	}
	if n := len(index); n > 0 {
		last := index[n-1].semver
		if hardeningdomain.CompareSemVer(core, last) <= 0 {
			core = hardeningdomain.SemVer{Major: last.Major, Minor: last.Minor, Patch: last.Patch + 1}
		}
	}
	return fmt.Sprintf("%d.%d.%d-%s.%d", core.Major, core.Minor, core.Patch, TestPrereleaseLabel, run), nil
}
