package naming

import (
	"regexp"
	"strings"

	"github.com/dop251/goja/ast"
)

// The template-compilation checks live in js_refs.go; this file keeps the
// pieces they share with the rest of the engine.

// oversizedStringPattern bounds padding widths that would expand a template
// into an unbounded string.
var oversizedStringPattern = regexp.MustCompile(`\.(?:padStart|padEnd)\s*\(\s*[0-9]{5,}`)

// regexPatternArgumentIndex maps a regex helper to the argument position that
// carries its pattern. A literal pattern is a static constant, so an invalid or
// oversized one is a defect of the template itself: it is refused while the
// template is validated instead of at the first render, using exactly the
// bounds the helper applies at render. A non-literal pattern cannot be judged
// statically and is left to the helper.
var regexPatternArgumentIndex = map[string]int{"replaceRegex": 1, "extract": 1, "match": 1}

func validateLiteralRegexArgument(kind TemplateKind, name string, args []ast.Expression) error {
	index, ok := regexPatternArgumentIndex[name]
	if !ok || index >= len(args) {
		return nil
	}
	literal, ok := args[index].(*ast.StringLiteral)
	if !ok {
		return nil
	}
	_, err := compileJSRegex(literal.Value.String())
	if err == nil {
		return nil
	}
	code := ErrCodeTemplateCompileError
	if strings.Contains(err.Error(), "exceeds the") {
		code = ErrCodeTemplateLimitExceeded
	}
	return &Error{Code: code, Field: string(kind), Message: err.Error()}
}
