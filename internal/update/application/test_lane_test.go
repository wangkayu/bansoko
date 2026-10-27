package application

import (
	"context"
	"crypto/ed25519"
	"net/http"
	"net/url"
	"testing"
	"time"

	hardeningdomain "bansoko/internal/hardening/domain"
)

// The test update lane publishes `X.Y.Z-test.<run>` builds, numbered by the
// workflow run, on the update-channel-test branch; the formal lane publishes
// `X.Y.Z` on update-channel. Both share the run counter as release sequence
// and one signing key. The client compares no versions across lanes: it offers
// the signed candidate of the channel it is configured for, and admission
// refuses any candidate at or below the persisted sequence floor. These tests
// pin what that means for an instance moving between the two lanes.

// laneCandidate is one signed candidate served by its own fixture.
type laneCandidate struct {
	manifest  hardeningdomain.ReleaseManifest
	publicKey ed25519.PublicKey
	digest    string
}

func newLaneCandidate(t *testing.T, version string, channel hardeningdomain.ReleaseChannel, sequence int64, notes []hardeningdomain.ReleaseNotesEntry) laneCandidate {
	t.Helper()
	manifest, publicKey := disposableSignedManifestWith(t, "v"+version, sequence, "embedded-bootstrap-v1", "base-v1", func(m *hardeningdomain.ReleaseManifest) {
		m.Version, m.Channel, m.ReleaseNotes = version, channel, notes
		m.EmbeddedUpdate.TargetVersion = version
	})
	return laneCandidate{manifest: manifest, publicKey: publicKey, digest: disposableManifestDigest(t, manifest)}
}

// service returns an instance running runningVersion with the given persisted
// sequence floor, pointed at this candidate. With admit the payload request is
// held open so an admitted chain stays deterministically in flight.
func (c laneCandidate) service(t *testing.T, floor int64, runningVersion string, admit bool) (*Service, *disposableFixture) {
	t.Helper()
	fixture := newDisposableFixture(t, c.manifest, admit)
	return disposableService(t, Deps{
		DB:        disposableFloorDB(t, floor, nil),
		PublicKey: c.publicKey, UpdatesRoot: t.TempDir(), ManifestURL: fixture.manifestURL(),
		HTTPClient: fixture.client(), BuildReleaseID: "v" + runningVersion, BuildVersion: runningVersion,
		BuildCompatibilityVersion: "base-v1", BuildBootstrapVersion: "embedded-bootstrap-v1",
	}), fixture
}

func (c laneCandidate) admit(t *testing.T, service *Service, key string) (Status, bool, error) {
	t.Helper()
	return service.AdmitUpdate(context.Background(), AdmitInput{TargetReleaseID: c.manifest.ReleaseID, ManifestDigest: c.digest}, key)
}

func TestATestBuildIsOfferedWithItsVersionAndNoNotes(t *testing.T) {
	build := newLaneCandidate(t, "0.1.0-test.130", hardeningdomain.ReleaseChannelNightly, 130, nil)
	service, _ := build.service(t, 129, "0.1.0-test.129", false)
	check, err := service.CheckUpdate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if check.Status != "update_available" || check.Candidate == nil {
		t.Fatalf("check = %+v, want the test build offered", check)
	}
	if check.Candidate.Version != "0.1.0-test.130" || check.Candidate.ReleaseID != "v0.1.0-test.130" || check.Candidate.ReleaseSequence != 130 {
		t.Fatalf("candidate = %+v", check.Candidate)
	}
	if len(check.Candidate.ReleaseNotes) != 0 {
		t.Fatalf("a test build carried notes: %+v", check.Candidate.ReleaseNotes)
	}
	// The running test build itself is up to date.
	same, _ := build.service(t, 130, "0.1.0-test.130", false)
	if check, err := same.CheckUpdate(context.Background()); err != nil || check.Status != "up_to_date" {
		t.Fatalf("same build check = %+v, %v", check, err)
	}
}

func TestMovingBetweenLanesFollowsTheSequenceFloor(t *testing.T) {
	t.Run("an older formal release is refused over a newer test build", func(t *testing.T) {
		// An instance that installed test build 130 is pointed back at the
		// formal channel, whose newest release was published earlier
		// (sequence 125). The check still names it, but admission refuses it:
		// the instance waits for the next formal release.
		older := newLaneCandidate(t, "0.1.0", hardeningdomain.ReleaseChannelStable, 125, nil)
		service, _ := older.service(t, 130, "0.1.0-test.130", false)
		if check, err := service.CheckUpdate(context.Background()); err != nil || check.Status != "update_available" {
			t.Fatalf("check = %+v, %v", check, err)
		}
		_, created, err := older.admit(t, service, "lane-older-stable")
		requireDisposableBusinessError(t, err, http.StatusUnprocessableEntity, "UPDATE_TARGET_STALE")
		if created {
			t.Fatal("an older formal release was admitted over a newer test build")
		}
	})

	t.Run("the next formal release is admitted with its notes", func(t *testing.T) {
		// The next formal release carries a later run number; its notes are
		// shown to the test build of the same core version, because
		// 0.1.1-test.130 precedes 0.1.1.
		notes := []hardeningdomain.ReleaseNotesEntry{{
			Version: "0.1.1", PublishedAt: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC),
			Notes: hardeningdomain.ReleaseNotesText{ZhCN: "## 修复\n- 一项\n", En: "## Fixed\n- One\n"},
		}}
		next := newLaneCandidate(t, "0.1.1", hardeningdomain.ReleaseChannelStable, 140, notes)
		service, _ := next.service(t, 130, "0.1.1-test.130", false)
		check, err := service.CheckUpdate(context.Background())
		if err != nil || check.Status != "update_available" || check.Candidate == nil {
			t.Fatalf("check = %+v, %v", check, err)
		}
		if len(check.Candidate.ReleaseNotes) != 1 || check.Candidate.ReleaseNotes[0].Version != "0.1.1" {
			t.Fatalf("formal notes for the test build of the same version: %+v", check.Candidate.ReleaseNotes)
		}
		admitting, fixture := next.service(t, 130, "0.1.1-test.130", true)
		status, created, err := next.admit(t, admitting, "lane-next-stable")
		if err != nil || !created || status.Step != "accepted" {
			t.Fatalf("admission = %+v, created=%v, err=%v; want the later formal release accepted", status, created, err)
		}
		fixture.awaitPayloadRequest(t)
	})

	t.Run("a formal instance pointed at the test channel takes a later test build", func(t *testing.T) {
		build := newLaneCandidate(t, "0.1.2-test.141", hardeningdomain.ReleaseChannelNightly, 141, nil)
		service, fixture := build.service(t, 140, "0.1.1", true)
		status, created, err := build.admit(t, service, "lane-to-test")
		if err != nil || !created || status.Step != "accepted" {
			t.Fatalf("admission = %+v, created=%v, err=%v; want the later test build accepted", status, created, err)
		}
		fixture.awaitPayloadRequest(t)
	})
}

// Each channel's package root is its own branch directory, so a channel
// document of one lane can never point an instance at the other lane's files.
func TestTheTwoLanePackageRootsExcludeEachOther(t *testing.T) {
	parse := func(raw string) *url.URL {
		t.Helper()
		parsed, err := parseUpdateURL(raw)
		if err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		return parsed
	}
	const base = "https://raw.githubusercontent.com/wangkayu/bansoko/"
	stableRoot, testRoot := parse(base+"update-channel/"), parse(base+"update-channel-test/")
	stableManifest := parse(base + "update-channel/0.1.0/release-manifest.json")
	testManifest := parse(base + "update-channel-test/0.1.1-test.130/release-manifest.json")
	if err := packageRootContains(stableRoot, stableManifest); err != nil {
		t.Fatalf("formal manifest refused by the formal root: %v", err)
	}
	if err := packageRootContains(testRoot, testManifest); err != nil {
		t.Fatalf("test manifest refused by the test root: %v", err)
	}
	if err := packageRootContains(stableRoot, testManifest); err == nil {
		t.Fatal("the formal package root accepted a test-branch manifest")
	}
	if err := packageRootContains(testRoot, stableManifest); err == nil {
		t.Fatal("the test package root accepted a formal-branch manifest")
	}
}
