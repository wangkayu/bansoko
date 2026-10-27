package releasecontract

import (
	"os"
	"path/filepath"
	"testing"

	hardeningdomain "bansoko/internal/hardening/domain"
)

func writeTestLaneInputs(t *testing.T, version, index string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	versionFile := filepath.Join(dir, "VERSION")
	if err := os.WriteFile(versionFile, []byte(version+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	notes := filepath.Join(dir, "release-notes")
	if err := os.Mkdir(notes, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(notes, ReleaseNotesIndexName), []byte(index), 0o644); err != nil {
		t.Fatal(err)
	}
	return versionFile, notes
}

func TestTestVersionIsAPrereleaseOfTheNextFormalVersion(t *testing.T) {
	cases := []struct {
		name, version, index, want string
	}{
		{"nothing released yet", "0.1.0-dev", "# header\n", "0.1.0-test.120"},
		{"VERSION ahead of the last release", "0.3.0-dev", "0.1.0 2026-10-28\n0.2.0 2026-10-29\n", "0.3.0-test.120"},
		{"VERSION not bumped after the release", "0.1.0-dev", "0.1.0 2026-10-28\n", "0.1.1-test.120"},
		{"plain VERSION equal to the release", "0.2.0", "0.2.0 2026-10-28\n", "0.2.1-test.120"},
		{"VERSION behind the release", "0.1.0-dev", "0.1.0 2026-10-28\n0.4.2 2026-10-29\n", "0.4.3-test.120"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			versionFile, notes := writeTestLaneInputs(t, tc.version, tc.index)
			got, err := TestVersion(versionFile, notes, 120)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("TestVersion = %q, want %q", got, tc.want)
			}
			if !IsTestVersion(got) || IsReleaseVersion(got) {
				t.Fatalf("%q is not shaped as a test version", got)
			}
			// Every test build orders below the formal release of its own
			// core and above every released version.
			parsed, err := hardeningdomain.ParseSemVer(got)
			if err != nil {
				t.Fatal(err)
			}
			core, _, _ := ParseTestVersion(got)
			formal, _ := hardeningdomain.ParseSemVer(core)
			if hardeningdomain.CompareSemVer(parsed, formal) >= 0 {
				t.Fatalf("%s does not order below %s", got, core)
			}
			index, err := readNotesIndex(notes)
			if err != nil {
				t.Fatal(err)
			}
			for _, released := range index {
				if hardeningdomain.CompareSemVer(parsed, released.semver) <= 0 {
					t.Fatalf("%s does not order above the released %s", got, released.version)
				}
			}
		})
	}
}

func TestTestVersionsOrderByRunWithinACore(t *testing.T) {
	versionFile, notes := writeTestLaneInputs(t, "0.1.0-dev", "")
	previous := hardeningdomain.SemVer{}
	for index, run := range []int64{9, 10, 99, 100, 1000} {
		version, err := TestVersion(versionFile, notes, run)
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := hardeningdomain.ParseSemVer(version)
		if err != nil {
			t.Fatal(err)
		}
		if index > 0 && hardeningdomain.CompareSemVer(parsed, previous) <= 0 {
			t.Fatalf("%s does not advance the previous test build", version)
		}
		if _, got, ok := ParseTestVersion(version); !ok || got != run {
			t.Fatalf("ParseTestVersion(%q) = %d, %v; want run %d", version, got, ok, run)
		}
		previous = parsed
	}
}

func TestTestVersionRefusesUnusableInputs(t *testing.T) {
	versionFile, notes := writeTestLaneInputs(t, "0.1.0-dev", "")
	if _, err := TestVersion(versionFile, notes, 0); err == nil {
		t.Fatal("a zero run number was accepted")
	}
	bad, notes2 := writeTestLaneInputs(t, "dev", "")
	if _, err := TestVersion(bad, notes2, 5); err == nil {
		t.Fatal("a non-SemVer VERSION was accepted")
	}
	broken, notes3 := writeTestLaneInputs(t, "0.1.0-dev", "0.2.0 2026-10-28\n0.1.0 2026-10-29\n")
	if _, err := TestVersion(broken, notes3, 5); err == nil {
		t.Fatal("a non-increasing release index was accepted")
	}
	if _, err := TestVersion(filepath.Join(t.TempDir(), "missing"), notes, 5); err == nil {
		t.Fatal("a missing VERSION was accepted")
	}
}

func TestParseTestVersionShape(t *testing.T) {
	for _, value := range []string{"0.1.0", "0.1.0-test", "0.1.0-test.0", "0.1.0-test.01", "0.1.0-rc.1", "v0.1.0-test.1", "0.1.0-test.1.2", "0.01.0-test.1"} {
		if IsTestVersion(value) {
			t.Fatalf("%q was accepted as a test version", value)
		}
	}
	core, run, ok := ParseTestVersion("12.3.40-test.517")
	if !ok || core != "12.3.40" || run != 517 {
		t.Fatalf("ParseTestVersion = %q %d %v", core, run, ok)
	}
}

// The repository's own VERSION and release index must always derive a test
// version, or every main build would fail at its test-lane preflight.
func TestRepositoryDerivesATestVersion(t *testing.T) {
	root := filepath.Join("..", "..")
	if _, err := TestVersion(filepath.Join(root, "VERSION"), filepath.Join(root, "release-notes"), 1); err != nil {
		t.Fatalf("the repository cannot derive a test version: %v", err)
	}
}
