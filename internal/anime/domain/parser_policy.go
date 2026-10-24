package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// ParserPolicyEnvelopeSchemaVersion pins the frozen parser-policy envelope
// shape. A revision stores exactly this shape so a later reader can recompute
// the same parser digest without re-reading any current global policy.
const ParserPolicyEnvelopeSchemaVersion = "parser-policy-envelope-v1"

// ParserPolicyEnvelope is the parser-policy authority frozen into one saved
// configuration revision. The rules/config sections own the episode patterns;
// this envelope owns the selected parser kind, plus the version/provenance
// evidence needed to prove where it came from at save time. Preview, activation
// and one-shot build their parser selection from this value and never from the
// execution-time latest global parser default. It carries no episode offset:
// an offset belongs only to an RSS link or a one-shot input.
type ParserPolicyEnvelope struct {
	Kind              string `json:"kind"`
	PolicyVersion     int64  `json:"policy_version"`
	Provenance        string `json:"provenance"`
	ProfileVersion    int64  `json:"profile_version"`
	RulesOwnerVersion int64  `json:"rules_owner_version"`
	SchemaVersion     string `json:"schema_version"`
	SourceOwner       string `json:"source_owner"`
}

// Validate rejects an envelope that cannot be a frozen parser authority: an
// unknown schema or an empty kind. The kind's
// registration is checked by the caller that owns the parser registry, because
// this package must not import the parser implementation.
func (e ParserPolicyEnvelope) Validate() error {
	if e.SchemaVersion != ParserPolicyEnvelopeSchemaVersion {
		return fmt.Errorf("parser policy envelope schema must be %q", ParserPolicyEnvelopeSchemaVersion)
	}
	if e.Kind == "" {
		return fmt.Errorf("parser policy envelope kind is required")
	}
	if e.SourceOwner == "" {
		return fmt.Errorf("parser policy envelope source owner is required")
	}
	return nil
}

// Digest returns the canonical sha256 over the envelope's logical content. The
// schema version is part of the payload so a future shape can never collide
// with this one.
func (e ParserPolicyEnvelope) Digest() (string, error) {
	encoded, err := json.Marshal(e)
	if err != nil {
		return "", fmt.Errorf("encode parser policy envelope: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// DecodeParserPolicyEnvelope reads a stored envelope and validates its shape.
func DecodeParserPolicyEnvelope(raw []byte) (ParserPolicyEnvelope, error) {
	var envelope ParserPolicyEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return ParserPolicyEnvelope{}, fmt.Errorf("decode parser policy envelope: %w", err)
	}
	if err := envelope.Validate(); err != nil {
		return ParserPolicyEnvelope{}, err
	}
	return envelope, nil
}
