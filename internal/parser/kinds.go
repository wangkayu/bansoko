package parser

// The closed parser-kind catalog. Every mirror of this vocabulary (OpenAPI
// enum, generated Go/TS, live-policy CHECKs) is conformance-tested against
// Kinds; adding a kind means adding it here, registering its adapter, and
// forward-migrating the CHECKs.
const (
	// ParserRegex is the ordered episode-pattern parser.
	ParserRegex = "regex"
	// ParserReltitle is the reltitle episode parser.
	ParserReltitle = "reltitle"
)

// Kinds returns every parser kind in display order.
func Kinds() []ParserKind {
	return []ParserKind{ParserKind(ParserRegex), ParserKind(ParserReltitle)}
}
