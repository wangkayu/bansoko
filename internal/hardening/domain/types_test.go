package domain

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestUpdateRequiresBackupAndMaintenance(t *testing.T) {
	plan := UpdatePlan{Manifest: ReleaseManifest{Version: "v2", ArtifactDigest: "0123456789abcdef", Signature: "sig", SchemaVersion: "011", Immutable: true}, Rollback: CodeOnly}
	if err := plan.Admit(); err == nil {
		t.Fatal("unsafe update was admitted")
	}
	plan.BackupVerified = true
	plan.Maintenance = true
	if err := plan.Admit(); err != nil {
		t.Fatal(err)
	}
}

func TestArtifactDigestSignatureBindsDigest(t *testing.T) {
	publicKey, privateKey, err := GenerateSigningKey()
	if err != nil {
		t.Fatal(err)
	}
	digest := HashArtifact([]byte("release-artifact"))
	signature, err := SignArtifactDigest(privateKey, digest)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyArtifactDigestSignature(publicKey, digest, signature); err != nil {
		t.Fatal(err)
	}
	if err := VerifyArtifactDigestSignature(publicKey, HashArtifact([]byte("tampered")), signature); err == nil {
		t.Fatal("artifact signature was not bound to the digest")
	}
}

// legacyManifestJSON predates the embedded update program: no release_sequence
// and no embedded_update segment. Canonical bytes must stay byte-identical so
// historical signatures keep verifying.
const legacyManifestJSON = `{"schema_version":"release-manifest-1","release_id":"release-1","version":"0.9.0","channel":"stable","source_commit":"abc","api_contract_version":"v2","target_migration_version":"010_x","rollback_class":"restore_required","artifacts":[{"platform":"linux","architecture":"amd64","digest":"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef","size":10,"signature":"sig"}],"sbom_ref":"sha256:aa","provenance_ref":"sha256:bb","artifact_digest":"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef","signature":"","immutable":true}`

func TestLegacyManifestCanonicalBytesUnchanged(t *testing.T) {
	var manifest ReleaseManifest
	if err := json.Unmarshal([]byte(legacyManifestJSON), &manifest); err != nil {
		t.Fatal(err)
	}
	canonical, err := manifest.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(canonical, []byte("release_sequence")) || bytes.Contains(canonical, []byte("embedded_update")) {
		t.Fatalf("legacy canonical bytes gained new fields: %s", canonical)
	}
	if err := manifest.ValidateForRelease(); err != nil {
		t.Fatalf("ValidateForRelease must tolerate manifests without release_sequence: %v", err)
	}
}

func TestEmbeddedUpdateValidation(t *testing.T) {
	base := ReleaseManifest{
		SchemaVersion:          "release-manifest-1",
		ReleaseSequence:        7,
		ReleaseID:              "release-20260827-b1",
		Version:                "0.9.0-rc.10",
		Channel:                ReleaseChannelStable,
		SourceCommit:           "abc",
		APIContractVersion:     "v2",
		TargetMigrationVersion: "037_x",
		Rollback:               RestoreRequired,
		Immutable:              true,
		Artifacts: []ArtifactRef{{Platform: "linux", Architecture: "amd64",
			Digest:    strings.Repeat("ab", 32),
			Size:      1,
			Signature: "sig"}},
		SBOMRef:        "sha256:aa",
		ProvenanceRef:  "sha256:bb",
		ArtifactDigest: strings.Repeat("cd", 32),
	}

	digest := strings.Repeat("ef", 32)
	segment := EmbeddedUpdateSegment{
		FormatVersion:       EmbeddedUpdateFormatV1,
		Platform:            "linux",
		Architecture:        "amd64",
		PayloadDigest:       digest,
		PayloadSize:         1234,
		PayloadSignature:    "payload-sig",
		BootstrapMinVersion: "embedded-bootstrap-v1",
		MinImageVersion:     "2026-08",
		TargetReleaseID:     base.ReleaseID,
		TargetVersion:       base.Version,
		TargetSchemaVersion: base.TargetMigrationVersion,
		ReleaseSequence:     7,
	}

	manifest := base
	if err := manifest.ValidateForEmbeddedUpdate(); err == nil {
		t.Fatal("missing embedded_update segment must be rejected")
	}

	manifest.EmbeddedUpdate = &segment
	if err := manifest.ValidateForEmbeddedUpdate(); err != nil {
		t.Fatalf("consistent segment rejected: %v", err)
	}

	publicKey, privateKey, err := GenerateSigningKey()
	if err != nil {
		t.Fatal(err)
	}
	payloadSignature, err := SignArtifactDigest(privateKey, digest)
	if err != nil {
		t.Fatal(err)
	}
	manifest.EmbeddedUpdate.PayloadSignature = payloadSignature
	if err := manifest.VerifyEmbeddedPayloadSignature(publicKey); err != nil {
		t.Fatalf("payload signature verification failed: %v", err)
	}
	otherDigest := strings.Repeat("01", 32)
	tamperedSig, err := SignArtifactDigest(privateKey, otherDigest)
	if err != nil {
		t.Fatal(err)
	}
	wrongDigest := segment
	wrongDigest.PayloadSignature = tamperedSig
	broken := manifest
	broken.EmbeddedUpdate = &wrongDigest
	if err := broken.VerifyEmbeddedPayloadSignature(publicKey); err == nil {
		t.Fatal("payload signature bound to another digest must be rejected")
	}

	tampered := segment
	tampered.TargetVersion = "0.9.0-rc.9"
	broken = manifest
	broken.EmbeddedUpdate = &tampered
	if err := broken.ValidateForEmbeddedUpdate(); err == nil {
		t.Fatal("target_version divergence from the outer manifest must be rejected")
	}

	wrongSeq := segment
	wrongSeq.ReleaseSequence = 8
	broken = manifest
	broken.EmbeddedUpdate = &wrongSeq
	if err := broken.ValidateForEmbeddedUpdate(); err == nil {
		t.Fatal("segment sequence divergence from the outer manifest must be rejected")
	}

	noSeq := manifest
	noSeq.ReleaseSequence = 0
	if err := noSeq.ValidateForEmbeddedUpdate(); err == nil {
		t.Fatal("missing outer release_sequence must be rejected for embedded manifests")
	}
}

func TestEffectiveMinImageVersion(t *testing.T) {
	cases := []struct {
		name, current, want string
		ok                  bool
	}{
		{name: "set", current: " base-2026-08 ", want: "base-2026-08", ok: true},
		{name: "blank", current: "  "},
		{name: "missing"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			manifest := ReleaseManifest{EmbeddedUpdate: &EmbeddedUpdateSegment{MinImageVersion: tc.current}}
			got, err := manifest.EffectiveMinImageVersion()
			if tc.ok {
				if err != nil || got != tc.want {
					t.Fatalf("got %q, %v; want %q", got, err, tc.want)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected rejection, got %q", got)
			}
		})
	}
}
