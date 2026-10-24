package deploymentidentity

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func validDigest() string {
	const hex = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	return hex
}

func validBaseDigest() string {
	return "sha256:" + validDigest()
}

func TestNotProvidedWhenEnvironmentEmpty(t *testing.T) {
	if got := (Identity{}).Provided(); got {
		t.Error("zero identity must not be provided")
	}
	if got := FromEnv(func(string) string { return "" }).Provided(); got {
		t.Error("empty environment must not be provided")
	}
}

func TestBaseImageDigestAloneIsValid(t *testing.T) {
	identity := Identity{BaseImageDigest: "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}
	if err := identity.Validate(nil); err != nil {
		t.Fatalf("base image digest alone must validate: %v", err)
	}
}

func TestValidateForRuntimeRejectsPartialIdentityWhenSourceRequired(t *testing.T) {
	identity := Identity{BaseImageDigest: validBaseDigest()}
	if err := identity.ValidateForRuntime(nil, true); !errors.Is(err, ErrRuntimeSourceRequired) {
		t.Fatalf("partial runtime identity error = %v, want %v", err, ErrRuntimeSourceRequired)
	}
}

func TestValidateForRuntimeAllowsEmptyNativeIdentity(t *testing.T) {
	if err := (Identity{}).ValidateForRuntime(nil, false); err != nil {
		t.Fatalf("empty native identity error = %v", err)
	}
}

func TestValidateRules(t *testing.T) {
	cases := []struct {
		name     string
		identity Identity
		pointer  *PointerState
		wantErr  error
	}{
		{
			name:     "base with no overlay facts",
			identity: Identity{BaseImageDigest: validBaseDigest(), SelectedSource: SourceBase},
			wantErr:  nil,
		},
		{
			name:     "base with unknown image digest",
			identity: Identity{SelectedSource: SourceBase},
			wantErr:  ErrBaseImageRequired,
		},
		{
			name:     "base carrying payload digest is inconsistent",
			identity: Identity{BaseImageDigest: validBaseDigest(), SelectedSource: SourceBase, PayloadDigest: validDigest()},
			wantErr:  ErrPayloadOnBase,
		},
		{
			name:     "base carrying release id is inconsistent",
			identity: Identity{BaseImageDigest: validBaseDigest(), SelectedSource: SourceBase, SelectedReleaseID: "release-1"},
			wantErr:  ErrPayloadOnBase,
		},
		{
			name:     "overlay without release id is inconsistent",
			identity: Identity{BaseImageDigest: validBaseDigest(), SelectedSource: SourceOverlay},
			wantErr:  ErrReleaseRequired,
		},
		{
			name:     "overlay with malformed payload digest is inconsistent",
			identity: Identity{BaseImageDigest: validBaseDigest(), SelectedSource: SourceOverlay, SelectedReleaseID: "release-1", PayloadDigest: "not-hex"},
			wantErr:  ErrPayloadDigestFormat,
		},
		{
			name:     "overlay facts without source are inconsistent",
			identity: Identity{SelectedReleaseID: "release-1"},
			wantErr:  ErrSourceMissing,
		},
		{
			name:     "unknown source is inconsistent",
			identity: Identity{SelectedSource: "delta", SelectedReleaseID: "release-1"},
			wantErr:  ErrSourceInvalid,
		},
		{
			name:     "overlay release matches readable pointer",
			identity: Identity{BaseImageDigest: validBaseDigest(), SelectedSource: SourceOverlay, SelectedReleaseID: "release-1", PayloadDigest: validDigest(), UpdateOperationID: "update-1", ExpectedProcessBuildID: "build-1", ProcessBuildID: "build-1"},
			pointer:  &PointerState{Token: "release-1", Readable: true, Presented: true},
			wantErr:  nil,
		},
		{
			name:     "overlay release drifts from pointer",
			identity: Identity{BaseImageDigest: validBaseDigest(), SelectedSource: SourceOverlay, SelectedReleaseID: "release-1", PayloadDigest: validDigest(), UpdateOperationID: "update-1", ExpectedProcessBuildID: "build-1", ProcessBuildID: "build-1"},
			pointer:  &PointerState{Token: "release-2", Readable: true, Presented: true},
			wantErr:  ErrPointerMismatch,
		},
		{
			name:     "base serving while pointer selects overlay is inconsistent",
			identity: Identity{BaseImageDigest: validBaseDigest(), SelectedSource: SourceBase},
			pointer:  &PointerState{Token: "release-2", Readable: true, Presented: true},
			wantErr:  ErrPointerOnBase,
		},
		{
			name:     "missing pointer contradicts overlay selection",
			identity: Identity{BaseImageDigest: validBaseDigest(), SelectedSource: SourceOverlay, SelectedReleaseID: "release-1", PayloadDigest: validDigest(), UpdateOperationID: "update-1", ExpectedProcessBuildID: "build-1", ProcessBuildID: "build-1"},
			pointer:  &PointerState{Readable: false, Presented: false},
			wantErr:  ErrPointerMissing,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.identity.Validate(tc.pointer)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Validate() = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestFromEnvReadsAllKeys(t *testing.T) {
	env := map[string]string{
		EnvBaseImageDigest:   "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		EnvSelectedReleaseID: "release-9",
		EnvSource:            SourceOverlay,
		EnvPayloadDigest:     validDigest(),
		EnvUpdateOperationID: "update-1",
		"BANSOKO_UNRELATED":  "x",
	}
	identity := FromEnv(func(key string) string { return env[key] })
	if identity.BaseImageDigest != "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" || identity.SelectedReleaseID != "release-9" ||
		identity.SelectedSource != SourceOverlay || identity.PayloadDigest != validDigest() ||
		identity.UpdateOperationID != "update-1" {
		t.Fatalf("unexpected identity: %+v", identity)
	}
	if !identity.Provided() {
		t.Fatal("identity must be provided")
	}
}

func TestValidateRejectsInvalidBaseDigest(t *testing.T) {
	if err := (Identity{BaseImageDigest: "sha256:short"}).Validate(nil); !errors.Is(err, ErrBaseImageDigestFormat) {
		t.Fatalf("Validate() = %v, want %v", err, ErrBaseImageDigestFormat)
	}
}

func TestValidateRejectsBuildIDMismatch(t *testing.T) {
	identity := Identity{BaseImageDigest: "sha256:" + validDigest(), SelectedSource: SourceOverlay, SelectedReleaseID: "release-1", PayloadDigest: validDigest(), UpdateOperationID: "update-1", ExpectedProcessBuildID: "signed-build", ProcessBuildID: "running-build"}
	if err := identity.Validate(nil); !errors.Is(err, ErrProcessBuildMismatch) {
		t.Fatalf("Validate() = %v, want %v", err, ErrProcessBuildMismatch)
	}
}

func TestReadCurrentPointerDistinguishesCorrupt(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "current"), []byte("../escape"), 0o600); err != nil {
		t.Fatal(err)
	}
	state := ReadCurrentPointer(dir)
	if !state.Presented || !state.Corrupt || state.Readable {
		t.Fatalf("unexpected corrupt pointer state: %+v", state)
	}
}
