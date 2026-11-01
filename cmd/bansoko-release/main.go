package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	hardeningdomain "bansoko/internal/hardening/domain"
	"bansoko/internal/releasecontract"
	updateapplication "bansoko/internal/update/application"
	updatechannel "bansoko/internal/update/channel"
	"bansoko/internal/update/packagemaker"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: bansoko-release <manifest-render|verify|validate-schema|verify-embedded|pack-payload|channel-write|channel-verify|channel-gate|version-gate|test-version|release-notes> [flags]")
	}
	switch args[0] {
	case "manifest-render":
		return manifestRender(args[1:])
	case "pack-payload":
		return packPayload(args[1:])
	case "verify":
		return verify(args[1:])
	case "validate-schema":
		return validateSchema(args[1:])
	case "verify-embedded":
		return verifyEmbedded(args[1:])
	case "channel-write":
		return channelWrite(args[1:])
	case "channel-verify":
		return channelVerify(args[1:])
	case "channel-gate":
		return channelGate(args[1:])
	case "version-gate":
		return versionGate(args[1:])
	case "test-version":
		return testVersion(args[1:])
	case "release-notes":
		return releaseNotes(args[1:])
	default:
		return fmt.Errorf("unsupported bansoko-release command %q", args[0])
	}
}

// packPayload builds the deterministic embedded-update payload archive and
// prints a JSON summary with the bare-hex sha256 digest and byte size.
func packPayload(args []string) error {
	flags := flag.NewFlagSet("pack-payload", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	serverBinary := flags.String("server-binary", "", "compiled server binary")
	migrationsDir := flags.String("migrations-dir", "", "db/migrations directory with forward-only SQL")
	frontendDist := flags.String("frontend-dist", "", "built frontend dist directory (index.html required)")
	defaultsDir := flags.String("defaults-dir", "", "release defaults directory (attr-rules.yaml required)")
	output := flags.String("output", "", "payload archive output path (.tar.gz)")
	sourceDateEpoch := flags.Int64("source-date-epoch", 0, "deterministic SOURCE_DATE_EPOCH")
	formatVersion := flags.String("format-version", hardeningdomain.EmbeddedUpdateFormatV1, "embedded update format version")
	releaseID := flags.String("release-id", "", "release identifier")
	version := flags.String("version", "", "release version")
	sourceCommit := flags.String("source-commit", "", "source commit")
	buildID := flags.String("build-id", "", "build id recorded in release-metadata.json")
	targetSchemaVersion := flags.String("target-schema-version", "", "target schema migration version")
	platform := flags.String("platform", "", "target platform (GOOS)")
	architecture := flags.String("architecture", "", "target architecture (GOARCH)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	result, err := packagemaker.BuildEmbeddedPayload(packagemaker.Options{
		ServerBinaryPath: *serverBinary,
		MigrationsDir:    *migrationsDir,
		FrontendDistDir:  *frontendDist,
		DefaultsDir:      *defaultsDir,
		OutputPath:       *output,
		SourceDateEpoch:  *sourceDateEpoch,
		Metadata: packagemaker.Metadata{
			FormatVersion:       *formatVersion,
			ReleaseID:           *releaseID,
			Version:             *version,
			SourceCommit:        *sourceCommit,
			BuildID:             *buildID,
			TargetSchemaVersion: *targetSchemaVersion,
			Platform:            *platform,
			Architecture:        *architecture,
		},
	})
	if err != nil {
		return err
	}
	data, err := json.Marshal(result)
	if err != nil {
		return err
	}
	fmt.Println(string(data))
	return nil
}

// manifest-render re-encodes one release manifest into its canonical JSON and
// prints the canonical digest. Releases are not signed (owner 2026-11-01): the
// digest is the manifest's identity, the channel document carries it, and the
// deployment proves the bytes it fetched are the bytes that were published.
func manifestRender(args []string) error {
	flags := flag.NewFlagSet("manifest-render", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	inputPath := flags.String("input", "", "release manifest JSON")
	outputPath := flags.String("output", "", "canonical manifest JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*inputPath) == "" || strings.TrimSpace(*outputPath) == "" {
		return errors.New("--input and --output are required")
	}
	manifest, err := readManifest(*inputPath)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("encode release manifest: %w", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(*outputPath, data, 0o644); err != nil {
		return fmt.Errorf("write release manifest: %w", err)
	}
	digest, err := manifest.CanonicalDigest()
	if err != nil {
		return err
	}
	fmt.Printf("manifest_digest=%s\n", digest)
	return nil
}

// verify proves a release manifest is exactly the bytes a deployment would
// accept under this build: strict schema (no unknown fields), and, when an
// artifact is given, that the artifact hashes to the declared digest.
func verify(args []string) error {
	flags := flag.NewFlagSet("verify", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	manifestPath := flags.String("manifest", "", "release manifest JSON")
	artifactPath := flags.String("artifact", "", "optional artifact file to digest")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*manifestPath) == "" {
		return errors.New("--manifest is required")
	}
	manifest, err := readManifest(*manifestPath)
	if err != nil {
		return err
	}
	if strings.TrimSpace(*artifactPath) != "" {
		payload, err := os.ReadFile(*artifactPath)
		if err != nil {
			return fmt.Errorf("read artifact: %w", err)
		}
		if digest := hardeningdomain.HashArtifact(payload); digest != manifest.ArtifactDigest {
			return fmt.Errorf("artifact digest mismatch: got %s want %s", digest, manifest.ArtifactDigest)
		}
	}
	digest, err := manifest.CanonicalDigest()
	if err != nil {
		return err
	}
	fmt.Printf("verified version=%s manifest_digest=%s\n", manifest.Version, digest)
	return nil
}

func validateSchema(args []string) error {
	flags := flag.NewFlagSet("validate-schema", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	manifestPath := flags.String("manifest", "", "manifest JSON")
	allowEmptyImages := flags.Bool("allow-empty-images", false, "rehearsal-only empty images exception")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*manifestPath) == "" {
		return errors.New("--manifest is required")
	}
	if *allowEmptyImages && os.Getenv("BANSOKO_REHEARSAL") != "1" {
		return errors.New("--allow-empty-images is rehearsal-only and requires BANSOKO_REHEARSAL=1")
	}
	if err := releasecontract.ValidateCurrent(*manifestPath, *allowEmptyImages); err != nil {
		return err
	}
	fmt.Println("current manifest schema verified")
	return nil
}

// verify-embedded validates a bundle manifest that declares an embedded
// update payload: embedded segment consistency + optional payload archive
// digest.
func verifyEmbedded(args []string) error {
	flags := flag.NewFlagSet("verify-embedded", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	manifestPath := flags.String("manifest", "", "release manifest JSON")
	payloadPath := flags.String("payload", "", "optional payload .tar.gz to digest against the declared payload_digest")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*manifestPath) == "" {
		return errors.New("--manifest is required")
	}
	manifest, err := readManifest(*manifestPath)
	if err != nil {
		return err
	}
	if err := manifest.ValidateForEmbeddedUpdate(); err != nil {
		return err
	}
	if strings.TrimSpace(*payloadPath) != "" {
		data, err := os.ReadFile(*payloadPath)
		if err != nil {
			return fmt.Errorf("read payload: %w", err)
		}
		digest := hardeningdomain.HashArtifact(data)
		if digest != manifest.EmbeddedUpdate.PayloadDigest {
			return fmt.Errorf("payload digest mismatch: got %s want %s", digest, manifest.EmbeddedUpdate.PayloadDigest)
		}
	}
	fmt.Printf("verified embedded payload release_id=%s sequence=%d\n", manifest.ReleaseID, manifest.ReleaseSequence)
	return nil
}

func readManifest(path string) (hardeningdomain.ReleaseManifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return hardeningdomain.ReleaseManifest{}, fmt.Errorf("read manifest: %w", err)
	}
	var manifest hardeningdomain.ReleaseManifest
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return hardeningdomain.ReleaseManifest{}, fmt.Errorf("decode manifest: %w", err)
	}
	return manifest, nil
}

// channel-write builds one release-discovery document from the exact published
// release bytes. Every identity field comes from the manifest that was read
// back out of the package registry, never from the caller's environment, so the
// channel can never name a release other than the one the deployment will
// actually fetch.
func channelWrite(args []string) error {
	flags := flag.NewFlagSet("channel-write", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	manifestPath := flags.String("manifest", "", "published release manifest JSON")
	payloadPath := flags.String("payload", "", "published embedded update payload archive")
	manifestURL := flags.String("manifest-url", "", "immutable manifest URL the channel points at")
	channelName := flags.String("channel", "", "channel name (for example: test)")
	expectSequence := flags.Int64("expect-release-sequence", 0, "required release sequence (CI pipeline identity); 0 disables the check")
	outputPath := flags.String("output", "", "channel document output path")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*manifestPath) == "" || strings.TrimSpace(*payloadPath) == "" ||
		strings.TrimSpace(*manifestURL) == "" || strings.TrimSpace(*channelName) == "" ||
		strings.TrimSpace(*outputPath) == "" {
		return errors.New("--manifest, --payload, --manifest-url, --channel and --output are required")
	}
	manifestData, err := os.ReadFile(*manifestPath)
	if err != nil {
		return fmt.Errorf("read published manifest: %w", err)
	}
	manifest, digest, err := updateapplication.VerifyManifestDocument(manifestData)
	if err != nil {
		return fmt.Errorf("published release manifest rejected: %w", err)
	}
	if manifest.EmbeddedUpdate == nil {
		return errors.New("published release manifest has no embedded update segment")
	}
	if expect := *expectSequence; expect > 0 && manifest.ReleaseSequence != expect {
		return fmt.Errorf("published release sequence %d does not match the expected pipeline identity %d", manifest.ReleaseSequence, expect)
	}
	if err := updateapplication.VerifyPayloadFile(*payloadPath, manifest.EmbeddedUpdate); err != nil {
		return fmt.Errorf("published payload rejected: %w", err)
	}
	// The URL must be one the deployment's own source policy accepts, so a
	// published channel can never point outside an installable package path.
	if _, err := updateapplication.ResolvePackageSource(*manifestURL, "", updateapplication.UpdateHTTPSourceConfig{}); err != nil {
		return fmt.Errorf("channel manifest url rejected: %w", err)
	}
	document := updatechannel.Document{
		SchemaVersion:   updatechannel.SchemaVersion,
		Channel:         strings.TrimSpace(*channelName),
		ReleaseID:       manifest.ReleaseID,
		Version:         manifest.Version,
		ReleaseSequence: manifest.ReleaseSequence,
		ManifestURL:     *manifestURL,
		ManifestDigest:  digest,
		SourceCommit:    manifest.SourceCommit,
		PublishedAt:     manifest.PublishedAt.UTC(),
	}
	if err := document.Validate(); err != nil {
		return fmt.Errorf("channel document rejected: %w", err)
	}
	data, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return fmt.Errorf("encode channel document: %w", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(*outputPath, data, 0o644); err != nil {
		return fmt.Errorf("write channel document: %w", err)
	}
	identity, err := json.Marshal(channelIdentity(document))
	if err != nil {
		return err
	}
	fmt.Printf("%s\n", identity)
	return nil
}

// channelVerify proves a channel document is one the deployment would accept
// and prints its identity for scripting.
func channelVerify(args []string) error {
	flags := flag.NewFlagSet("channel-verify", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	documentPath := flags.String("document", "", "channel document JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*documentPath) == "" {
		return errors.New("--document is required")
	}
	data, err := os.ReadFile(*documentPath)
	if err != nil {
		return fmt.Errorf("read channel document: %w", err)
	}
	document, err := updatechannel.Decode(data)
	if err != nil {
		return err
	}
	if err := document.Validate(); err != nil {
		return err
	}
	identity, err := json.Marshal(channelIdentity(document))
	if err != nil {
		return err
	}
	fmt.Printf("%s\n", identity)
	return nil
}

// channelIdentity is the machine-readable channel summary CI compares between
// the published document and the one it is about to publish.
func channelIdentity(document updatechannel.Document) map[string]any {
	return map[string]any{
		"schema_version":   document.SchemaVersion,
		"channel":          document.Channel,
		"release_id":       document.ReleaseID,
		"version":          document.Version,
		"release_sequence": document.ReleaseSequence,
		"manifest_digest":  document.ManifestDigest,
		"manifest_url":     document.ManifestURL,
	}
}

// releaseLane is one of the two publication lanes. Each lane owns its own
// channel branch, discovery-channel name and version shape, and the gates
// refuse any document that names the other lane's identity: the lanes share
// the run counter, so the gate is where they stay apart.
type releaseLane struct {
	name    string
	channel string
}

var (
	laneStable = releaseLane{name: "stable", channel: "stable"}
	laneTest   = releaseLane{name: "test", channel: "test"}
)

func parseReleaseLane(value string) (releaseLane, error) {
	switch strings.TrimSpace(value) {
	case laneStable.name:
		return laneStable, nil
	case laneTest.name:
		return laneTest, nil
	default:
		return releaseLane{}, fmt.Errorf("--lane must be %q or %q (got %q)", laneStable.name, laneTest.name, value)
	}
}

// versionFits reports whether a version has this lane's shape. A test
// version's run number is its release sequence, so the two cannot drift.
func (l releaseLane) versionFits(version string, sequence int64) error {
	if l == laneTest {
		_, run, ok := releasecontract.ParseTestVersion(version)
		if !ok {
			return fmt.Errorf("test lane version must be X.Y.Z-test.<run> (got %q)", version)
		}
		if sequence > 0 && run != sequence {
			return fmt.Errorf("test lane version %s names run %d, not release sequence %d", version, run, sequence)
		}
		return nil
	}
	if !releasecontract.IsReleaseVersion(version) {
		return fmt.Errorf("stable lane version must be X.Y.Z (got %q)", version)
	}
	return nil
}

// acceptsPublished reports whether a published channel document belongs to
// this lane. The stable lane still accepts the retired automatic "test"
// document its branch carried before the first formal release (the one-way
// rename below); the test lane accepts only its own channel.
func (l releaseLane) acceptsPublished(published string) bool {
	return published == l.channel || (l == laneStable && channelRenameAllowed(published, l.channel))
}

// channelGate refuses a candidate discovery document that would move the
// channel backwards or sideways. Publication is the only mutable step in the
// release path, so the rule is explicit and lives here instead of in shell
// arithmetic: the candidate must name its lane's channel and version shape,
// and either repeat the published release exactly (an idempotent retry) or
// advance both the release sequence and the version.
func channelGate(args []string) error {
	flags := flag.NewFlagSet("channel-gate", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	publishedPath := flags.String("published", "", "published channel document, if one exists")
	candidatePath := flags.String("candidate", "", "candidate channel document")
	laneName := flags.String("lane", "", "publication lane: stable (vX.Y.Z tags) or test (main builds)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	lane, err := parseReleaseLane(*laneName)
	if err != nil {
		return err
	}
	if strings.TrimSpace(*candidatePath) == "" {
		return errors.New("--candidate is required")
	}
	candidateData, err := os.ReadFile(*candidatePath)
	if err != nil {
		return fmt.Errorf("read candidate channel: %w", err)
	}
	candidate, err := updatechannel.Decode(candidateData)
	if err != nil {
		return err
	}
	if err := candidate.Validate(); err != nil {
		return fmt.Errorf("candidate channel rejected: %w", err)
	}
	if candidate.Channel != lane.channel {
		return fmt.Errorf("candidate channel %q is not the %s lane's channel %q", candidate.Channel, lane.name, lane.channel)
	}
	if err := lane.versionFits(candidate.Version, candidate.ReleaseSequence); err != nil {
		return fmt.Errorf("candidate channel rejected: %w", err)
	}
	publishedPathValue := strings.TrimSpace(*publishedPath)
	if publishedPathValue == "" {
		fmt.Printf("no published channel document; publishing release %s (sequence %d)\n", candidate.ReleaseID, candidate.ReleaseSequence)
		return nil
	}
	publishedData, err := os.ReadFile(publishedPathValue)
	if err != nil {
		return fmt.Errorf("read published channel: %w", err)
	}
	published, err := updatechannel.Decode(publishedData)
	if err != nil {
		return err
	}
	if err := published.Validate(); err != nil {
		return fmt.Errorf("published channel rejected: %w", err)
	}
	if !lane.acceptsPublished(published.Channel) {
		return fmt.Errorf("candidate channel %q does not match the published channel %q", candidate.Channel, published.Channel)
	}
	switch {
	case candidate.ReleaseSequence > published.ReleaseSequence:
		if err := requireNewerVersion(published.Version, candidate.Version); err != nil {
			return fmt.Errorf("refusing to move channel %q: %w", published.Channel, err)
		}
		fmt.Printf("channel advances %s (sequence %d) -> %s (sequence %d)\n",
			published.ReleaseID, published.ReleaseSequence, candidate.ReleaseID, candidate.ReleaseSequence)
		return nil
	case candidate.ReleaseSequence == published.ReleaseSequence &&
		bytes.Equal(candidateData, publishedData):
		fmt.Printf("channel already names %s (sequence %d); republishing the same release\n", candidate.ReleaseID, candidate.ReleaseSequence)
		return nil
	default:
		return fmt.Errorf("refusing to move channel %q from %s (sequence %d) to %s (sequence %d): a channel may only advance or repeat the published release",
			candidate.Channel, published.ReleaseID, published.ReleaseSequence, candidate.ReleaseID, candidate.ReleaseSequence)
	}
}

// channelRenameAllowed names the only discovery-channel rename the gate
// accepts: the one-way move from the retired automatic test channel that the
// update-channel branch carried to the formal stable channel, made by the
// first tagged release. Every other rename would let one channel's
// publication overwrite another's. The current test lane publishes to its own
// branch and never renames.
func channelRenameAllowed(published, candidate string) bool {
	return published == "test" && candidate == "stable"
}

// requireNewerVersion refuses a version that does not strictly advance by
// semantic-version precedence. An unparseable version cannot be ordered and
// fails closed.
func requireNewerVersion(published, candidate string) error {
	current, err := hardeningdomain.ParseSemVer(published)
	if err != nil {
		return fmt.Errorf("published version %q cannot be ordered: %w", published, err)
	}
	next, err := hardeningdomain.ParseSemVer(candidate)
	if err != nil {
		return fmt.Errorf("candidate version %q cannot be ordered: %w", candidate, err)
	}
	if hardeningdomain.CompareSemVer(next, current) <= 0 {
		return fmt.Errorf("version %s does not advance the published version %s", candidate, published)
	}
	return nil
}

// versionGate is the pre-publication check of a release: before any image,
// package or channel effect, the version being released must be newer than
// the one its lane's channel names. An absent channel (first publication)
// passes; a channel document that does not decode or validate fails closed.
func versionGate(args []string) error {
	flags := flag.NewFlagSet("version-gate", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	publishedPath := flags.String("published", "", "published channel document; empty when the channel does not exist yet")
	version := flags.String("version", "", "version about to be released (X.Y.Z, or X.Y.Z-test.<run> on the test lane)")
	sequence := flags.Int64("sequence", 0, "release sequence of this run (enables the idempotent rerun case; required on the test lane)")
	sourceCommit := flags.String("source-commit", "", "source commit of this run (enables the idempotent rerun case)")
	laneName := flags.String("lane", "", "publication lane: stable (vX.Y.Z tags) or test (main builds)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	lane, err := parseReleaseLane(*laneName)
	if err != nil {
		return err
	}
	if lane == laneTest && *sequence < 1 {
		return errors.New("--sequence is required on the test lane: the test version names its run")
	}
	if err := lane.versionFits(*version, *sequence); err != nil {
		return fmt.Errorf("--version: %w", err)
	}
	if strings.TrimSpace(*publishedPath) == "" {
		fmt.Printf("no published channel document; %s is the first publication\n", *version)
		return nil
	}
	data, err := os.ReadFile(*publishedPath)
	if err != nil {
		return fmt.Errorf("read published channel: %w", err)
	}
	published, err := updatechannel.Decode(data)
	if err != nil {
		return err
	}
	if err := published.Validate(); err != nil {
		return fmt.Errorf("published channel rejected: %w", err)
	}
	if !lane.acceptsPublished(published.Channel) {
		return fmt.Errorf("the published channel %q is not the %s lane's channel %q", published.Channel, lane.name, lane.channel)
	}
	// A rerun of the very run that already published this version (same
	// version, sequence and source commit) may continue: every later step is
	// idempotent, and the channel gate accepts only identical signed bytes.
	if *sequence > 0 && *sourceCommit != "" && published.Version == *version &&
		published.ReleaseSequence == *sequence && published.SourceCommit == *sourceCommit {
		fmt.Printf("channel already names %s from this run (sequence %d); continuing an idempotent rerun\n", *version, *sequence)
		return nil
	}
	if err := requireNewerVersion(published.Version, *version); err != nil {
		return err
	}
	fmt.Printf("version %s advances the published %s (sequence %d)\n", *version, published.Version, published.ReleaseSequence)
	return nil
}

// testVersion prints the test-lane version of one main build: a SemVer
// prerelease of the next formal version, numbered by the run (see
// releasecontract.TestVersion).
func testVersion(args []string) error {
	flags := flag.NewFlagSet("test-version", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	versionFile := flags.String("version-file", "VERSION", "VERSION file of the source tree")
	notesDir := flags.String("notes-dir", "release-notes", "release notes directory (versions.txt lists the released versions)")
	run := flags.Int64("run", 0, "workflow run number, which is also the release sequence")
	if err := flags.Parse(args); err != nil {
		return err
	}
	version, err := releasecontract.TestVersion(*versionFile, *notesDir, *run)
	if err != nil {
		return err
	}
	fmt.Println(version)
	return nil
}

// releaseNotes validates the repository's release notes and extracts one
// version's notes for the release manifest or the GitHub Release text.
func releaseNotes(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: bansoko-release release-notes <check|manifest|github> --dir <release-notes> [--version X.Y.Z --output <file>]")
	}
	mode := args[0]
	flags := flag.NewFlagSet("release-notes "+mode, flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	dir := flags.String("dir", "release-notes", "release notes directory")
	version := flags.String("version", "", "released version (X.Y.Z)")
	output := flags.String("output", "", "output file")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	switch mode {
	case "check":
		if err := releasecontract.CheckReleaseNotes(*dir); err != nil {
			return err
		}
		fmt.Println("release notes valid")
		return nil
	case "manifest", "github":
	default:
		return fmt.Errorf("unsupported release-notes mode %q", mode)
	}
	if strings.TrimSpace(*output) == "" {
		return errors.New("--output is required")
	}
	entries, err := releasecontract.BuildReleaseNotes(*dir, *version)
	if err != nil {
		return err
	}
	var data []byte
	if mode == "manifest" {
		data, err = json.Marshal(entries)
		if err != nil {
			return err
		}
		data = append(data, '\n')
	} else {
		data = []byte(releasecontract.GitHubReleaseBody(entries[0]))
	}
	return os.WriteFile(*output, data, 0o644)
}
