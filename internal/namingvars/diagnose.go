package namingvars

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/dop251/goja"
	"github.com/dop251/goja/parser"
)

// Diagnostic codes. The code names the kind of finding; Message is the
// engine's own wording, and Start/End locate it in the canonical source.
const (
	DiagSourceEmpty        = "source_empty"
	DiagSourceTooLarge     = "source_too_large"
	DiagSyntax             = "syntax_error"
	DiagUnboundedExpansion = "unbounded_expansion"
	DiagDynamicExecution   = "dynamic_execution"
	DiagTooManyAssignments = "too_many_assignments"
	DiagEvaluationFailed   = "evaluation_failed"
	DiagOutputMissing      = "output_missing"
	DiagOutputNotScalar    = "output_not_scalar"
	DiagOutputInvalid      = "output_invalid"
	DiagInterrupted        = "interrupted"
)

// Diagnostic is one finding about a program source. Start and End are byte
// offsets into the canonical source (CanonicalSource: LF line endings), or -1
// when the finding is not tied to a place.
type Diagnostic struct {
	Code       string
	Message    string
	Start, End int
}

// strictPrefix is what evaluation puts before the program; offsets reported
// by the engine against the prefixed text are shifted back by one line.
const strictPrefix = "\"use strict\";\n"

var compileLocation = regexp.MustCompile(`at naming-vars:(\d+):(\d+)`)

// Diagnose statically checks a program source without running it: size,
// forbidden constructs, assignment budget and syntax. An empty result means
// the source may be evaluated.
func Diagnose(source string) []Diagnostic {
	switch {
	case len(source) == 0:
		return []Diagnostic{{Code: DiagSourceEmpty, Message: "naming vars source is empty", Start: -1, End: -1}}
	case len(source) > MaxSourceBytes:
		return []Diagnostic{{Code: DiagSourceTooLarge, Message: fmt.Sprintf("naming vars source exceeds %d bytes", MaxSourceBytes), Start: -1, End: -1}}
	}
	canonicalBytes, err := CanonicalSource(source)
	if err != nil {
		return []Diagnostic{{Code: DiagSourceEmpty, Message: err.Error(), Start: -1, End: -1}}
	}
	canonical := string(canonicalBytes)
	var out []Diagnostic
	const perPattern = 10
	for _, spec := range []struct {
		pattern *regexp.Regexp
		code    string
		message string
	}{
		{unboundedExpansionPattern, DiagUnboundedExpansion, "naming vars unbounded expansion is not allowed"},
		{unboundedExecutionPattern, DiagDynamicExecution, "naming vars dynamic execution is not allowed"},
	} {
		for _, span := range spec.pattern.FindAllStringIndex(canonical, perPattern) {
			out = append(out, Diagnostic{Code: spec.code, Message: spec.message, Start: span[0], End: span[1]})
		}
	}
	if assignments := varsAssignmentPattern.FindAllStringIndex(canonical, -1); len(assignments) > MaxCollectionLen {
		span := assignments[MaxCollectionLen]
		out = append(out, Diagnostic{Code: DiagTooManyAssignments, Message: fmt.Sprintf("naming vars exceeds %d assignments", MaxCollectionLen), Start: span[0], End: span[1]})
	}
	if len(out) > 0 {
		return out
	}
	prefixed := strictPrefix + canonical
	if _, err := parser.ParseFile(nil, "naming-vars", prefixed, 0); err != nil {
		var list parser.ErrorList
		if errors.As(err, &list) && len(list) > 0 {
			for i, item := range list {
				if i >= perPattern {
					break
				}
				start, end := lineSpan(canonical, item.Position.Line-1, item.Position.Column-1)
				out = append(out, Diagnostic{Code: DiagSyntax, Message: item.Message, Start: start, End: end})
			}
			return out
		}
		return []Diagnostic{{Code: DiagSyntax, Message: err.Error(), Start: -1, End: -1}}
	}
	if _, err := goja.Compile("naming-vars", prefixed, false); err != nil {
		message := err.Error()
		start, end := -1, -1
		if match := compileLocation.FindStringSubmatch(message); match != nil {
			line, _ := strconv.Atoi(match[1])
			column, _ := strconv.Atoi(match[2])
			start, end = lineSpan(canonical, line-2, column-1)
			message = strings.TrimSpace(strings.TrimSuffix(message, match[0]))
		}
		message = strings.TrimPrefix(message, "SyntaxError: ")
		return []Diagnostic{{Code: DiagSyntax, Message: message, Start: start, End: end}}
	}
	return nil
}

// EvaluationDiagnostic explains a failed evaluation of source and, where the
// engine says where, locates it.
func EvaluationDiagnostic(source string, err error) Diagnostic {
	canonicalBytes, _ := CanonicalSource(source)
	canonical := string(canonicalBytes)
	if IsInterrupted(err) {
		return Diagnostic{Code: DiagInterrupted, Message: err.Error(), Start: -1, End: -1}
	}
	var output *OutputError
	if errors.As(err, &output) {
		code := DiagOutputInvalid
		switch {
		case strings.Contains(output.message, "missing"):
			code = DiagOutputMissing
		case strings.Contains(output.message, "scalar"):
			code = DiagOutputNotScalar
		}
		start, end := outputSpan(canonical, output.Group, output.Key)
		return Diagnostic{Code: code, Message: output.Error(), Start: start, End: end}
	}
	var exception *goja.Exception
	if errors.As(err, &exception) {
		message := exception.Value().String()
		start, end := -1, -1
		for _, frame := range exception.Stack() {
			position := frame.Position()
			if position.Line >= 2 {
				start, end = lineSpan(canonical, position.Line-2, -1)
				break
			}
		}
		return Diagnostic{Code: DiagEvaluationFailed, Message: message, Start: start, End: end}
	}
	return Diagnostic{Code: DiagEvaluationFailed, Message: strings.TrimPrefix(err.Error(), "naming vars execution failed: "), Start: -1, End: -1}
}

// outputSpan finds the assignment that produced vars.<group>.<key>.
func outputSpan(canonical, group, key string) (int, int) {
	if group == "" || !keyPattern.MatchString(group) {
		return -1, -1
	}
	groupExpr := regexp.MustCompile(`\bvars\s*\.\s*` + regexp.QuoteMeta(group) + `\b`)
	loc := groupExpr.FindStringIndex(canonical)
	if loc == nil {
		return -1, -1
	}
	if key != "" && keyPattern.MatchString(key) {
		keyExpr := regexp.MustCompile(`\b` + regexp.QuoteMeta(key) + `\s*[:=]`)
		if inner := keyExpr.FindStringIndex(canonical[loc[0]:]); inner != nil {
			return loc[0] + inner[0], loc[0] + inner[0] + len(key)
		}
	}
	return loc[0], loc[1]
}

// lineSpan converts a zero-based line (and, when column >= 0, a zero-based
// byte column) of source into byte offsets. A column of -1 spans the line's
// code. Out-of-range input is clamped to the source.
func lineSpan(source string, line, column int) (int, int) {
	if line < 0 {
		line = 0
	}
	start := 0
	for i := 0; i < line; i++ {
		next := strings.IndexByte(source[start:], '\n')
		if next < 0 {
			start = len(source)
			break
		}
		start += next + 1
	}
	end := len(source)
	if next := strings.IndexByte(source[start:], '\n'); next >= 0 {
		end = start + next
	}
	if column >= 0 {
		if start+column < end {
			start += column
		}
	} else {
		for start < end && (source[start] == ' ' || source[start] == '\t') {
			start++
		}
	}
	if end <= start {
		if start < len(source) {
			end = start + 1
		} else if start > 0 {
			start, end = start-1, start
		}
	}
	return start, end
}
