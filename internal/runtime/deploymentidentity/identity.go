// Package deploymentidentity models the three-layer release identity of a
// running instance (task Sub B Step 5, design §6): the immutable base image
// digest, the selected overlay release (source=base|overlay) with its payload
// digest, and the durable update operation that produced the selection.
//
// BuildInfo keeps answering "which binary is this process running" untouched;
// this identity answers "where did that binary's tree come from". The values
// are injected once by the bootstrap wrapper through BANSOKO_DEPLOYMENT_* /
// BANSOKO_BASE_IMAGE_DIGEST environment variables before exec; base launches carry
// no payload identity by design ("缺失时留空").
package deploymentidentity

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Environment variable names injected by the unified binary's bootstrap mode before exec of the
// target server. They are read once at process start.
const (
	EnvBaseImageDigest      = "BANSOKO_BASE_IMAGE_DIGEST"
	EnvUpdatesRoot          = "BANSOKO_UPDATES_ROOT"
	EnvSelectedReleaseID    = "BANSOKO_DEPLOYMENT_SELECTED_RELEASE_ID"
	EnvSource               = "BANSOKO_DEPLOYMENT_SOURCE"
	EnvPayloadDigest        = "BANSOKO_DEPLOYMENT_PAYLOAD_DIGEST"
	EnvUpdateOperationID    = "BANSOKO_DEPLOYMENT_OPERATION_ID"
	EnvExpectedProcessBuild = "BANSOKO_DEPLOYMENT_EXPECTED_BUILD_ID"
	// EnvBootstrapVersion carries the release identity of the bootstrap wrapper
	// that launched this process. the unified binary's bootstrap mode always writes it from its own
	// source constant, so an inherited value can never claim a bootstrap the
	// running base image does not contain. The value is not part of Identity:
	// readiness must keep treating a dev process without bootstrap facts as
	// not_provided, while the updater compares the version itself.
	EnvBootstrapVersion     = "BANSOKO_BOOTSTRAP_VERSION"
	DefaultUpdatesRoot      = "/var/lib/bansoko/updates"
	currentPointerFileName  = "current"
	payloadDigestHexPattern = `^[0-9a-f]{64}$`
	imageDigestPattern      = `^sha256:[0-9a-f]{64}$`
	releaseIDPattern        = `^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`
)

var (
	// SourceBase marks serving from the immutable base image binaries.
	SourceBase = "base"
	// SourceOverlay marks serving from an overlay release on the updates volume.
	SourceOverlay = "overlay"

	payloadDigestRE = regexp.MustCompile(payloadDigestHexPattern)
	imageDigestRE   = regexp.MustCompile(imageDigestPattern)
	releaseIDRE     = regexp.MustCompile(releaseIDPattern)
)

// Identity is the three-layer deployment identity snapshot. Fields stay empty
// when unknown; a base launch legitimately carries no payload fields.
type Identity struct {
	BaseImageDigest   string `json:"base_image_digest,omitempty"`
	SelectedReleaseID string `json:"selected_release_id,omitempty"`
	SelectedSource    string `json:"selected_source,omitempty"` // "base" | "overlay"
	PayloadDigest     string `json:"payload_digest,omitempty"`
	UpdateOperationID string `json:"update_operation_id,omitempty"`
	// ProcessBuildID is the BuildInfo build id of the running binary. The
	// bootstrap never injects it; the server fills it when reporting so the
	// identity view stays the single combined surface.
	ProcessBuildID string `json:"process_build_id,omitempty"`
	// ExpectedProcessBuildID is bootstrap provenance used to validate the
	// running BuildInfo against the signed overlay metadata. It is deliberately
	// not part of the HTTP identity card.
	ExpectedProcessBuildID string `json:"-"`
}

// FromEnv reads the identity from environment lookup (injectable for tests).
func FromEnv(getenv func(string) string) Identity {
	trim := func(key string) string { return strings.TrimSpace(getenv(key)) }
	return Identity{
		BaseImageDigest:        trim(EnvBaseImageDigest),
		SelectedReleaseID:      trim(EnvSelectedReleaseID),
		SelectedSource:         trim(EnvSource),
		PayloadDigest:          trim(EnvPayloadDigest),
		UpdateOperationID:      trim(EnvUpdateOperationID),
		ExpectedProcessBuildID: trim(EnvExpectedProcessBuild),
	}
}

// Provided reports whether this process was launched with any update-aware
// bootstrap identity at all. Bare-metal dev servers report none: readiness
// treats that as absent rather than inconsistent.
func (i Identity) Provided() bool {
	return i.BaseImageDigest != "" || i.SelectedReleaseID != "" ||
		i.SelectedSource != "" || i.PayloadDigest != "" || i.UpdateOperationID != "" || i.ExpectedProcessBuildID != ""
}

// Consistency breach classes surfaced as readyz failure detail. Each maps to
// one row of the ora-2 §f consistency rule set implemented in Validate.
var (
	ErrSourceMissing         = errors.New("deployment identity carries overlay/base facts without BANSOKO_DEPLOYMENT_SOURCE")
	ErrSourceInvalid         = errors.New("deployment identity source is neither base nor overlay")
	ErrPayloadOnBase         = errors.New("base launch must not carry overlay release or payload identity")
	ErrReleaseRequired       = errors.New("overlay launch is missing the selected release id")
	ErrPayloadDigestFormat   = errors.New("payload digest is not a lowercase sha256 hex digest")
	ErrBaseImageDigestFormat = errors.New("base image digest is not a sha256:<64hex> digest")
	ErrProcessBuildMismatch  = errors.New("running process build id does not match signed overlay metadata")
	ErrPointerMismatch       = errors.New("updates volume current pointer no longer matches the injected release selection")
	ErrPointerOnBase         = errors.New("base launch is active while the updates volume still selects an overlay release")
	ErrPointerMissing        = errors.New("overlay identity has no current release pointer")
	ErrPointerCorrupt        = errors.New("updates volume current pointer is corrupt")
	ErrBaseImageRequired     = errors.New("deployment identity is missing the immutable base image digest")
	ErrProcessBuildRequired  = errors.New("deployment identity is missing the expected process build id")
	ErrRuntimeSourceRequired = errors.New("production deployment identity requires an explicit source")
)

// PointerState describes what the live current pointer showed during
// validation. An absent pointer is allowed only for a base launch; an overlay
// identity requires an explicit current selection.
type PointerState struct {
	Token     string // validated pointer token when readable
	Readable  bool
	Presented bool // file existed; false means the pointer is absent
	Corrupt   bool // present but unreadable or not a valid release token
}

// UpdatesRoot is the single updates-volume contract shared by bootstrap,
// server, updater and readiness. There is no bootstrap-specific override.
func UpdatesRoot(getenv func(string) string) string {
	if root := strings.TrimSpace(getenv(EnvUpdatesRoot)); root != "" {
		return root
	}
	return DefaultUpdatesRoot
}

// Validate checks the intra-identity consistency rules plus the live pointer
// agreement rule. A nil pointer state means "not probed".
func (i Identity) Validate(pointer *PointerState) error {
	if i.BaseImageDigest != "" && !imageDigestRE.MatchString(i.BaseImageDigest) {
		return ErrBaseImageDigestFormat
	}
	switch {
	case i.SelectedSource == "":
		if i.SelectedReleaseID != "" || i.PayloadDigest != "" || i.UpdateOperationID != "" || i.ExpectedProcessBuildID != "" {
			return ErrSourceMissing
		}
		// Only a base image digest was provided (image-only deployments):
		// nothing else to contradict.
		return nil
	case i.SelectedSource != SourceBase && i.SelectedSource != SourceOverlay:
		return ErrSourceInvalid
	}
	if i.SelectedSource == SourceBase {
		if i.BaseImageDigest == "" {
			return ErrBaseImageRequired
		}
		if i.SelectedReleaseID != "" || i.PayloadDigest != "" || i.UpdateOperationID != "" || i.ExpectedProcessBuildID != "" {
			return ErrPayloadOnBase
		}
	} else {
		if i.BaseImageDigest == "" {
			return ErrBaseImageRequired
		}
		if i.SelectedReleaseID == "" {
			return ErrReleaseRequired
		}
		if !releaseIDRE.MatchString(i.SelectedReleaseID) {
			return ErrReleaseRequired
		}
		if !payloadDigestRE.MatchString(i.PayloadDigest) {
			return ErrPayloadDigestFormat
		}
		if i.UpdateOperationID == "" || !releaseIDRE.MatchString(i.UpdateOperationID) {
			return ErrReleaseRequired
		}
		if i.ExpectedProcessBuildID == "" {
			return ErrProcessBuildRequired
		}
		if i.ProcessBuildID != i.ExpectedProcessBuildID {
			return ErrProcessBuildMismatch
		}
	}
	if pointer == nil {
		return nil
	}
	if pointer.Corrupt {
		return ErrPointerCorrupt
	}
	if i.SelectedSource == SourceOverlay && !pointer.Presented {
		return ErrPointerMissing
	}
	if !pointer.Readable {
		return nil
	}
	if i.SelectedSource == SourceOverlay && pointer.Token != "" && pointer.Token != i.SelectedReleaseID {
		return ErrPointerMismatch
	}
	if i.SelectedSource == SourceBase && pointer.Token != "" {
		return ErrPointerOnBase
	}
	return nil
}

// ValidateForRuntime applies the stricter production/bootstrap contract. A
// native development process may remain entirely not_provided, but a process
// carrying only a base digest or expected build facts is not a complete runtime
// identity and must not be reported ready by a production composition root.
func (i Identity) ValidateForRuntime(pointer *PointerState, requireSource bool) error {
	if requireSource && i.SelectedSource == "" {
		return ErrRuntimeSourceRequired
	}
	return i.Validate(pointer)
}

// ReadCurrentPointer performs one live read of the updates-volume current
// pointer used for the consistency check in readyz.
func ReadCurrentPointer(updatesRoot string) PointerState {
	raw, err := os.ReadFile(filepath.Join(updatesRoot, currentPointerFileName))
	if err != nil {
		if os.IsNotExist(err) {
			return PointerState{Readable: false, Presented: false}
		}
		return PointerState{Readable: false, Presented: true, Corrupt: true}
	}
	token := strings.TrimSpace(string(raw))
	if token == "" || !releaseIDRE.MatchString(token) {
		return PointerState{Readable: false, Presented: true, Corrupt: true}
	}
	return PointerState{Token: token, Readable: true, Presented: true}
}
