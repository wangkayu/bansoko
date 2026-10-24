package domain

import (
	"crypto/sha256"
	"encoding/hex"
)

// Digest is SHA256(ASCII(domain) || 0x00 || canonicalBytes), shown as
// "sha256:<hex>". The domain separates what is being hashed: two values with the
// same canonical bytes under different domains never share a digest, and a digest
// from one domain cannot be presented as another.
func Digest(domain string, canonicalBytes []byte) string {
	h := sha256.New()
	h.Write([]byte(domain))
	h.Write([]byte{0})
	h.Write(canonicalBytes)
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// DigestValue canonicalises v and hashes it under domain.
func DigestValue(domain string, v Value) string {
	return Digest(domain, Canonical(v))
}
