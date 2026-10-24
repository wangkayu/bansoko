// Package parser provides the closed parser registry and side-effect-free media
// recognition capability. Exactly one registered parser kind owns the complete
// title-derived result for a parse input; there is no voting, fallback, or
// field-level fusion between parser implementations.
package parser

// ParserKind is the explicit instance policy value. The registered kinds are
// listed by Kinds ("regex", "reltitle"; reltitle is the creation default);
// future adapters register a ParserAdapter in the recognition registry.
type ParserKind string

// The parser kind value is intentionally the same string as the low-level
// parser selection id.

// IsRegistered reports whether a kind is present in the authoritative registry.
func IsRegistered(kind ParserKind) bool {
	_, err := DefaultRegistry().Resolve(string(kind))
	return err == nil
}
