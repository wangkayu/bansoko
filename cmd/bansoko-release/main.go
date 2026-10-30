package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
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
		return errors.New("usage: bansoko-release <keygen|sign|verify|validate-schema|verify-embedded|artifact-sign|artifact-verify|pack-payload|channel-sign|channel-verify|channel-gate|version-gate|test-version|release-notes> [flags]")
	}
	switch args[0] {
	case "keygen":
		return keygen(args[1:])
	case "artifact-sign":
		return artifactSign(args[1:])
	case "artifact-verify":
		return artifactVerify(args[1:])
	case "pack-payload":
		return packPayload(args[1:])
	case "sign":
		return sign(args[1:])
	case "verify":
		return verify(args[1:])
	case "validate-schema":
		return validateSchema(args[1:])
	case "verify-embedded":
		return verifyEmbedded(args[1:])
	case "channel-sign":
		return channelSign(args[1:])
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

func artifactSign(args []string) error {
	flags := flag.NewFlagSet("artifact-sign", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	digest := flags.String("digest", "", "sha256 artifact digest")
	privatePath := flags.String("private-key", "", "hex/base64 encoded Ed25519 private key")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*digest) == "" || strings.TrimSpace(*privatePath) == "" {
		return errors.New("--digest and --private-key are required")
	}
	privateKey, err := readKey(*privatePath, ed25519.PrivateKeySize)
	if err != nil {
		return fmt.Errorf("read private key: %w", err)
	}
	signature, err := hardeningdomain.SignArtifactDigest(ed25519.PrivateKey(privateKey), *digest)
	if err != nil {
		return err
	}
	fmt.Println(signature)
	return nil
}

func artifactVerify(args []string) error {
	flags := flag.NewFlagSet("artifact-verify", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	digest := flags.String("digest", "", "sha256 artifact digest")
	signature := flags.String("signature", "", "base64 artifact signature")
	publicPath := flags.String("public-key", "", "hex/base64 encoded Ed25519 public key")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*digest) == "" || strings.TrimSpace(*signature) == "" || strings.TrimSpace(*publicPath) == "" {
		return errors.New("--digest, --signature and --public-key are required")
	}
	publicKey, err := readKey(*publicPath, ed25519.PublicKeySize)
	if err != nil {
		return fmt.Errorf("read public key: %w", err)
	}
	if err := hardeningdomain.VerifyArtifactDigestSignature(ed25519.PublicKey(publicKey), *digest, *signature); err != nil {
		return err
	}
	fmt.Println("artifact signature verified")
	return nil
}

func keygen(args []string) error {
	flags := flag.NewFlagSet("keygen", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	privatePath := flags.String("private-key", "", "output path for the private key")
	publicPath := flags.String("public-key", "", "output path for the public key")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*privatePath) == "" || strings.TrimSpace(*publicPath) == "" {
		return errors.New("--private-key and --public-key are required")
	}
	publicKey, privateKey, err := hardeningdomain.GenerateSigningKey()
	if err != nil {
		return err
	}
	if err := os.WriteFile(*privatePath, []byte(hex.EncodeToString(privateKey)), 0o600); err != nil {
		return fmt.Errorf("write private key: %w", err)
	}
	if err := os.WriteFile(*publicPath, []byte(hex.EncodeToString(publicKey)), 0o644); err != nil {
		return fmt.Errorf("write public key: %w", err)
	}
	fingerprint := hardeningdomain.HashArtifact(publicKey)
	fmt.Printf("public_key_fingerprint=%s\n", fingerprint)
	return nil
}

func sign(args []string) error {
	flags := flag.NewFlagSet("sign", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	inputPath := flags.String("input", "", "unsigned manifest JSON")
	outputPath := flags.String("output", "", "signed manifest JSON")
	privatePath := flags.String("private-key", "", "hex/base64 encoded Ed25519 private key")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*inputPath) == "" || strings.TrimSpace(*outputPath) == "" || strings.TrimSpace(*privatePath) == "" {
		return errors.New("--input, --output and --private-key are required")
	}
	manifest, err := readManifest(*inputPath)
	if err != nil {
		return err
	}
	privateKey, err := readKey(*privatePath, ed25519.PrivateKeySize)
	if err != nil {
		return fmt.Errorf("read private key: %w", err)
	}
	if err := manifest.Sign(ed25519.PrivateKey(privateKey)); err != nil {
		return fmt.Errorf("sign release manifest: %w", err)
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("encode signed release manifest: %w", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(*outputPath, data, 0o644); err != nil {
		return fmt.Errorf("write signed release manifest: %w", err)
	}
	digest, err := manifest.CanonicalDigest()
	if err != nil {
		return err
	}
	fmt.Printf("manifest_digest=%s\n", digest)
	return nil
}

func verify(args []string) error {
	flags := flag.NewFlagSet("verify", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	manifestPath := flags.String("manifest", "", "signed manifest JSON")
	publicPath := flags.String("public-key", "", "hex/base64 encoded Ed25519 public key")
	artifactPath := flags.String("artifact", "", "optional artifact file to digest")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*manifestPath) == "" || strings.TrimSpace(*publicPath) == "" {
		return errors.New("--manifest and --public-key are required")
	}
	manifest, err := readManifest(*manifestPath)
	if err != nil {
		return err
	}
	publicKey, err := readKey(*publicPath, ed25519.PublicKeySize)
	if err != nil {
		return fmt.Errorf("read public key: %w", err)
	}
	if err := manifest.VerifySignature(ed25519.PublicKey(publicKey)); err != nil {
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
// update payload: manifest signature + embedded segment consistency +
// optional payload archive digest.
func verifyEmbedded(args []string) error {
	flags := flag.NewFlagSet("verify-embedded", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	manifestPath := flags.String("manifest", "", "signed manifest JSON")
	publicPath := flags.String("public-key", "", "hex/base64 encoded Ed25519 public key")
	payloadPath := flags.String("payload", "", "optional payload .tar.gz to digest against the declared payload_digest")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*manifestPath) == "" || strings.TrimSpace(*publicPath) == "" {
		return errors.New("--manifest and --public-key are required")
	}
	manifest, err := readManifest(*manifestPath)
	if err != nil {
		return err
	}
	publicKey, err := readKey(*publicPath, ed25519.PublicKeySize)
	if err != nil {
		return fmt.Errorf("read public key: %w", err)
	}
	if err := manifest.VerifySignature(ed25519.PublicKey(publicKey)); err != nil {
		return fmt.Errorf("manifest signature: %w", err)
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

func readKey(path string, size int) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	value := strings.TrimSpace(string(data))
	if decoded, err := hex.DecodeString(value); err == nil && len(decoded) == size {
		return decoded, nil
	}
	if decoded, err := base64.RawStdEncoding.DecodeString(value); err == nil && len(decoded) == size {
		return decoded, nil
	}
	if len(data) == size {
		return data, nil
	}
	return nil, fmt.Errorf("key must decode to %d bytes", size)
}

// channelSign builds and signs one release-discovery document from the exact
// published release bytes. Every identity field comes from the manifest that
// was read back out of the package registry, never from the caller's
// environment, so the channel can never name a release other than the one the
// deployment will actually verify.
func channelSign(args []string) error {
	flags := flag.NewFlagSet("channel-sign", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	manifestPath := flags.String("manifest", "", "published signed release manifest JSON")
	payloadPath := flags.String("payload", "", "published embedded update payload archive")
	manifestURL := flags.String("manifest-url", "", "immutable manifest URL the channel points at")
	channelName := flags.String("channel", "", "channel name (for example: test)")
	expectSequence := flags.Int64("expect-release-sequence", 0, "required release sequence (CI pipeline identity); 0 disables the check")
	publicPath := flags.String("public-key", "", "hex/base64 encoded Ed25519 public key")
	privatePath := flags.String("private-key", "", "hex/base64 encoded Ed25519 private key")
	outputPath := flags.String("output", "", "signed channel document output path")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*manifestPath) == "" || strings.TrimSpace(*payloadPath) == "" ||
		strings.TrimSpace(*manifestURL) == "" || strings.TrimSpace(*channelName) == "" ||
		strings.TrimSpace(*publicPath) == "" || strings.TrimSpace(*privatePath) == "" ||
		strings.TrimSpace(*outputPath) == "" {
		return errors.New("--manifest, --payload, --manifest-url, --channel, --public-key, --private-key and --output are required")
	}
	publicKey, err := readKey(*publicPath, ed25519.PublicKeySize)
	if err != nil {
		return fmt.Errorf("read public key: %w", err)
	}
	manifestData, err := os.ReadFile(*manifestPath)
	if err != nil {
		return fmt.Errorf("read published manifest: %w", err)
	}
	manifest, digest, err := updateapplication.VerifyManifestDocument(manifestData, ed25519.PublicKey(publicKey))
	if err != nil {
		return fmt.Errorf("published release manifest rejected: %w", err)
	}
	if manifest.EmbeddedUpdate == nil {
		return errors.New("published release manifest has no embedded update segment")
	}
	if expect := *expectSequence; expect > 0 && manifest.ReleaseSequence != expect {
		return fmt.Errorf("published release sequence %d does not match the expected pipeline identity %d", manifest.ReleaseSequence, expect)
	}
	if err := updateapplication.VerifyPayloadFile(*payloadPath, manifest.EmbeddedUpdate, ed25519.PublicKey(publicKey)); err != nil {
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
	privateKey, err := readKey(*privatePath, ed25519.PrivateKeySize)
	if err != nil {
		return fmt.Errorf("read private key: %w", err)
	}
	if err := document.Sign(ed25519.PrivateKey(privateKey)); err != nil {
		return fmt.Errorf("sign channel document: %w", err)
	}
	data, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return fmt.Errorf("encode signed channel document: %w", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(*outputPath, data, 0o644); err != nil {
		return fmt.Errorf("write signed channel document: %w", err)
	}
	identity, err := json.Marshal(channelIdentity(document))
	if err != nil {
		return err
	}
	fmt.Printf("%s\n", identity)
	return nil
}

// channelVerify proves a channel document against the deployment's trusted
// public key and prints its identity for scripting.
func channelVerify(args []string) error {
	flags := flag.NewFlagSet("channel-verify", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	documentPath := flags.String("document", "", "signed channel document JSON")
	publicPath := flags.String("public-key", "", "hex/base64 encoded Ed25519 public key")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*documentPath) == "" || strings.TrimSpace(*publicPath) == "" {
		return errors.New("--document and --public-key are required")
	}
	publicKey, err := readKey(*publicPath, ed25519.PublicKeySize)
	if err != nil {
		return fmt.Errorf("read public key: %w", err)
	}
	data, err := os.ReadFile(*documentPath)
	if err != nil {
		return fmt.Errorf("read channel document: %w", err)
	}
	document, err := updatechannel.Decode(data)
	if err != nil {
		return err
	}
	if err := document.VerifySignature(ed25519.PublicKey(publicKey)); err != nil {
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
// the signing key and the run counter, so the gate is where they stay apart.
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
	publicPath := flags.String("public-key", "", "hex/base64 encoded Ed25519 public key")
	laneName := flags.String("lane", "", "publication lane: stable (vX.Y.Z tags) or test (main builds)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	lane, err := parseReleaseLane(*laneName)
	if err != nil {
		return err
	}
	if strings.TrimSpace(*candidatePath) == "" || strings.TrimSpace(*publicPath) == "" {
		return errors.New("--candidate and --public-key are required")
	}
	publicKey, err := readKey(*publicPath, ed25519.PublicKeySize)
	if err != nil {
		return fmt.Errorf("read public key: %w", err)
	}
	candidateData, err := os.ReadFile(*candidatePath)
	if err != nil {
		return fmt.Errorf("read candidate channel: %w", err)
	}
	candidate, err := updatechannel.Decode(candidateData)
	if err != nil {
		return err
	}
	if err := candidate.VerifySignature(ed25519.PublicKey(publicKey)); err != nil {
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
	if err := published.VerifySignature(ed25519.PublicKey(publicKey)); err != nil {
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
// the one its lane's signed channel names. An absent channel (first
// publication) passes; a channel that does not verify fails closed.
func versionGate(args []string) error {
	flags := flag.NewFlagSet("version-gate", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	publishedPath := flags.String("published", "", "published channel document; empty when the channel does not exist yet")
	version := flags.String("version", "", "version about to be released (X.Y.Z, or X.Y.Z-test.<run> on the test lane)")
	publicPath := flags.String("public-key", "", "hex/base64 encoded Ed25519 public key")
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
	if strings.TrimSpace(*publicPath) == "" {
		return errors.New("--public-key is required")
	}
	publicKey, err := readKey(*publicPath, ed25519.PublicKeySize)
	if err != nil {
		return fmt.Errorf("read public key: %w", err)
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
	if err := published.VerifySignature(ed25519.PublicKey(publicKey)); err != nil {
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
// version's notes for the signed manifest or the GitHub Release text.
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
