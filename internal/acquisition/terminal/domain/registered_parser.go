package domain

import mediaparser "bansoko/internal/parser"

// Consult the single process registry at validation time, including adapters
// registered after package initialization. Unknown names remain closed.
var registeredParserKind = Custom(func(r reader, v Value) error {
	if v.Kind != KindString || !mediaparser.IsRegistered(mediaparser.ParserKind(v.String)) {
		return r.fail(CodeTypeInvalid)
	}
	return nil
})
