package fileidentity

import (
	"errors"
	"os"

	"bansoko/internal/library/domain"
)

// FromFileInfo returns the identity available in an already obtained
// FileInfo. Platform-specific path or descriptor metadata should be preferred
// when the observation needs the optional generation discriminator.
func FromFileInfo(info os.FileInfo) domain.FileIdentity {
	if info == nil {
		return domain.FileIdentity{}
	}
	return platformFromFileInfo(info)
}

// FromPath obtains an identity for the path without following a final symlink.
// Platform-specific implementations use the strongest available path metadata
// and fall back to the portable FileInfo identity when necessary.
func FromPath(path string) (domain.FileIdentity, error) {
	return platformFromPath(path)
}

// FromFile obtains identity from an open descriptor. Descriptor metadata avoids
// re-resolving a mutable pathname and is the identity used for hashed reads.
func FromFile(file *os.File) (domain.FileIdentity, error) {
	if file == nil {
		return domain.FileIdentity{}, errors.New("file is nil")
	}
	info, err := file.Stat()
	if err != nil {
		return domain.FileIdentity{}, err
	}
	return platformFromOpenFile(file, FromFileInfo(info)), nil
}

// FromOpenFile is the descriptor-oriented identity API. It is kept as an
// explicit alias for callers that need to make the open-descriptor boundary
// visible in their code.
func FromOpenFile(file *os.File) (domain.FileIdentity, error) {
	return FromFile(file)
}

// MatchesHandleEvidence compares opaque object handles only when both observations
// provide complete, non-empty evidence. A missing or unusable handle is
// intentionally inconclusive for compatibility.
func MatchesHandleEvidence(left, right *domain.HandleEvidence) bool {
	if left == nil || right == nil {
		return true
	}
	if left.MountID <= 0 || right.MountID <= 0 || left.Type <= 0 || right.Type <= 0 || left.Opaque == "" || right.Opaque == "" {
		return false
	}
	return left.MountID == right.MountID && left.Type == right.Type && left.Opaque == right.Opaque
}

// IsPresent reports whether the mandatory device/inode portion is complete.
// GenerationNanos is optional for legacy persisted observations and for
// filesystems that do not expose birth-time metadata.
func IsPresent(identity domain.FileIdentity) bool {
	return identity.Device != 0 && identity.Inode != 0
}

// IsEmpty reports whether no identity component is available.
func IsEmpty(identity domain.FileIdentity) bool {
	return identity.Device == 0 && identity.Inode == 0 && identity.GenerationNanos == 0
}

// IsValid reports whether an identity is either absent or has its mandatory
// device/inode pair. GenerationNanos is optional but cannot be negative.
func IsValid(identity domain.FileIdentity) bool {
	return IsEmpty(identity) || IsPresent(identity) && identity.GenerationNanos >= 0
}

// Matches compares two identities without treating an unavailable generation
// discriminator as a mismatch. Device and inode remain mandatory; when both
// generations are present they must agree.
func Matches(left, right domain.FileIdentity) bool {
	if !IsPresent(left) || !IsPresent(right) {
		return false
	}
	if left.Device != right.Device || left.Inode != right.Inode {
		return false
	}
	return left.GenerationNanos == 0 || right.GenerationNanos == 0 || left.GenerationNanos == right.GenerationNanos
}

func generationNanos(sec, nsec int64) int64 {
	if sec < 0 || nsec < 0 || nsec >= 1_000_000_000 || sec == 0 && nsec == 0 {
		return 0
	}
	// Keep zero as the unavailable sentinel and reject values that would
	// overflow the durable int64 representation.
	const maxSeconds = int64(9223372036)
	const maxNanosecondsAtMaxSeconds = int64(854775807)
	if sec > maxSeconds || sec == maxSeconds && nsec > maxNanosecondsAtMaxSeconds {
		return 0
	}
	return sec*1_000_000_000 + nsec
}
