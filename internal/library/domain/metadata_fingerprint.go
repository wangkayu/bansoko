package domain

import (
	"strconv"
	"strings"
)

// MetadataFingerprintPrefix marks a path-level file identity: the file's size
// plus its device/inode when the platform provides them. It is what the
// library scan records and what every comparer of a scan-derived fingerprint
// (calibration, alignment planning and execution, offset migration) matches
// against. It is produced from metadata only; the file content is never read.
const MetadataFingerprintPrefix = "meta:"

// MetadataFacts is the parsed form of a metadata fingerprint. Device and Inode
// are zero when the platform exposes no file identity.
type MetadataFacts struct {
	SizeBytes int64
	Device    uint64
	Inode     uint64
}

// MetadataFingerprint renders the deterministic metadata identity
// "meta:size=<n>[;dev=<d>;ino=<i>]". The device/inode pair is included only
// when both are known.
func MetadataFingerprint(sizeBytes int64, identity FileIdentity) string {
	var b strings.Builder
	b.WriteString(MetadataFingerprintPrefix)
	b.WriteString("size=")
	b.WriteString(strconv.FormatInt(sizeBytes, 10))
	if identity.Device != 0 && identity.Inode != 0 {
		b.WriteString(";dev=")
		b.WriteString(strconv.FormatUint(identity.Device, 10))
		b.WriteString(";ino=")
		b.WriteString(strconv.FormatUint(identity.Inode, 10))
	}
	return b.String()
}

// IsMetadataFingerprint reports whether value is a metadata fingerprint.
func IsMetadataFingerprint(value string) bool {
	return strings.HasPrefix(strings.TrimSpace(value), MetadataFingerprintPrefix)
}

// ParseMetadataFingerprint decodes a metadata fingerprint. Anything else
// (a content digest, a typed digest envelope, a malformed value) is not ok.
func ParseMetadataFingerprint(value string) (MetadataFacts, bool) {
	trimmed := strings.TrimSpace(value)
	if !strings.HasPrefix(trimmed, MetadataFingerprintPrefix) {
		return MetadataFacts{}, false
	}
	var facts MetadataFacts
	seenSize := false
	for _, part := range strings.Split(strings.TrimPrefix(trimmed, MetadataFingerprintPrefix), ";") {
		key, raw, ok := strings.Cut(part, "=")
		if !ok {
			return MetadataFacts{}, false
		}
		switch key {
		case "size":
			size, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || size < 0 || seenSize {
				return MetadataFacts{}, false
			}
			facts.SizeBytes, seenSize = size, true
		case "dev":
			device, err := strconv.ParseUint(raw, 10, 64)
			if err != nil || facts.Device != 0 {
				return MetadataFacts{}, false
			}
			facts.Device = device
		case "ino":
			inode, err := strconv.ParseUint(raw, 10, 64)
			if err != nil || facts.Inode != 0 {
				return MetadataFacts{}, false
			}
			facts.Inode = inode
		default:
			return MetadataFacts{}, false
		}
	}
	if !seenSize || (facts.Device == 0) != (facts.Inode == 0) {
		return MetadataFacts{}, false
	}
	return facts, true
}

// MetadataFactsMatch is the path-level "same file" rule: equal size, and equal
// inode when both sides recorded one. The device number is deliberately not
// compared: a network mount's client-side device number can change across a
// remount while the server inode (NFS fileid) stays, so a remount alone must
// not read as a content change. A same-size, same-inode content swap is not
// detected.
func MetadataFactsMatch(left, right MetadataFacts) bool {
	if left.SizeBytes != right.SizeBytes {
		return false
	}
	if left.Inode != 0 && right.Inode != 0 {
		return left.Inode == right.Inode
	}
	return true
}

// MetadataFingerprintsMatch applies MetadataFactsMatch to two metadata
// fingerprints; it is false unless both parse.
func MetadataFingerprintsMatch(left, right string) bool {
	l, lok := ParseMetadataFingerprint(left)
	r, rok := ParseMetadataFingerprint(right)
	return lok && rok && MetadataFactsMatch(l, r)
}
