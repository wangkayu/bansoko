//go:build admissionselftest

package application

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"bansoko/internal/apperrors"
	hardeningdomain "bansoko/internal/hardening/domain"
	"bansoko/internal/testutil/disposabledb"
)

// TestRealAdmissionSelfTest is an executable acceptance probe. It drives
// Service.Admit through signed-manifest HTTP verification, then proves the
// compatibility refusal occurs before any durable update effect.
func TestRealAdmissionSelfTest(t *testing.T) {
	if os.Getenv("BANSOKO_ADMISSION_SELF_TEST") != "1" {
		t.Skip("admission self-test runner only")
	}
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	updatesRoot := t.TempDir()
	publicKey, privateKey, err := hardeningdomain.GenerateSigningKey()
	if err != nil {
		t.Fatal(err)
	}
	payloadBytes := []byte("signed embedded payload")
	payloadDigest := hardeningdomain.HashArtifact(payloadBytes)
	payloadSignature, err := hardeningdomain.SignArtifactDigest(privateKey, payloadDigest)
	if err != nil {
		t.Fatal(err)
	}
	manifest := hardeningdomain.ReleaseManifest{
		SchemaVersion: "release-manifest-1", ReleaseSequence: 7, ReleaseID: "release-b6-target",
		Version: "rc-b6", Channel: hardeningdomain.ReleaseChannelStable,
		SourceCommit: "commit-b6", APIContractVersion: "v2", TargetMigrationVersion: "001",
		RequiredDowntime: true, BackupRequired: true, Rollback: hardeningdomain.RestoreRequired,
		Immutable: true, ArtifactDigest: hardeningdomain.HashArtifact([]byte("release artifact")),
		Artifacts: []hardeningdomain.ArtifactRef{{Platform: "linux", Architecture: "amd64", Digest: hardeningdomain.HashArtifact([]byte("artifact")), Size: 1, Signature: "artifact-signature"}},
		SBOMRef:   "sbom", ProvenanceRef: "provenance",
		EmbeddedUpdate: &hardeningdomain.EmbeddedUpdateSegment{
			FormatVersion: hardeningdomain.EmbeddedUpdateFormatV1, Platform: "linux", Architecture: "amd64",
			PayloadDigest: payloadDigest, PayloadSize: int64(len(payloadBytes)), PayloadSignature: payloadSignature,
			BootstrapMinVersion: "embedded-bootstrap-v1", MinImageVersion: "base-v2", TargetReleaseID: "release-b6-target",
			TargetVersion: "rc-b6", TargetSchemaVersion: "001", ReleaseSequence: 7,
		},
	}
	if err := manifest.Sign(privateKey); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(manifest)
	}))
	defer server.Close()
	service, err := NewService(Deps{
		UpdateMode: "embedded", DB: db, PublicKey: publicKey, UpdatesRoot: updatesRoot,
		BuildReleaseID: "running-build", BuildCompatibilityVersion: "base-v1", BuildBootstrapVersion: "embedded-bootstrap-v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _, admitErr := service.Admit(context.Background(), server.URL+"/manifest.json", "ac-b6-self-test")
	var compatibilityErr *apperrors.ImageCompatibilityError
	if !errors.As(admitErr, &compatibilityErr) {
		t.Fatalf("Admit error = %v, want ImageCompatibilityError", admitErr)
	}
	entries, err := os.ReadDir(updatesRoot)
	if err != nil {
		t.Fatal(err)
	}
	passed := compatibilityErr.Required == "base-v2" && compatibilityErr.Current == "base-v1" && len(entries) == 0
	if evidencePath := os.Getenv("BANSOKO_ADMISSION_SELF_TEST_EVIDENCE_FILE"); evidencePath != "" {
		data := map[string]any{"acceptance": "min-image-version", "scope": "application-real", "passed": passed, "error_code": "UPDATE_REQUIRES_IMAGE", "required_min_image_version": compatibilityErr.Required, "current_base_image_version": compatibilityErr.Current, "staging_effect": len(entries) != 0}
		encoded, marshalErr := json.MarshalIndent(data, "", "  ")
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if writeErr := os.WriteFile(evidencePath, append(encoded, '\n'), 0o600); writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	if !passed {
		t.Fatalf("admission self-test effect assertion failed: staging_entries=%d", len(entries))
	}
}
