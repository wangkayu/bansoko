package application

import "time"

// AdmitInput identifies the signed release candidate selected by the caller.
type AdmitInput struct {
	TargetReleaseID string
	ManifestDigest  string
}

// Status is the transport-neutral view of embedded update admission or pending-marker handoff.
type Status struct {
	OperationID         string
	Channel             string
	Step                string
	TargetReleaseID     string
	TargetVersion       *string
	TargetSchemaVersion *string
	LastError           *string
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

// Check is the transport-neutral result of a side-effect-free update check.
type Check struct {
	ActiveOperation *Status
	Candidate       *Candidate
	CheckedAt       time.Time
	Compatibility   Compatibility
	Current         Current
	Status          CheckStatus
}

// CheckStatus describes the outcome of checking the published candidate.
type CheckStatus string

// Candidate is the signed release candidate exposed by an update check.
type Candidate struct {
	ReleaseID              string
	Version                string
	ReleaseSequence        int64
	PublishedAt            time.Time
	TargetMigrationVersion string
	RequiredDowntime       bool
	BackupRequired         bool
	RollbackClass          string
	ManifestDigest         string
	PayloadSize            int64
}

// Compatibility describes whether the candidate can run on the current image.
type Compatibility struct {
	Kind            CompatibilityKind
	MinImageVersion string
	Reason          string
}

// CompatibilityKind identifies the update compatibility path.
type CompatibilityKind string

// Current describes the running release and immutable image compatibility.
type Current struct {
	ReleaseID          string
	Version            string
	ImageCompatibility string
}
