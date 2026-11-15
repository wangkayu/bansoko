package notification

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	maxTemplateBytes   = 4096
	maxExpressionDepth = 32

	// MaxRenderedTitleChars and MaxRenderedBodyChars bound the rendered
	// message the delivery row stores; a render above them is invalid output.
	MaxRenderedTitleChars = 4096
	MaxRenderedBodyChars  = 65536
)

// Field names the template a diagnostic belongs to.
type Field string

const (
	FieldTitle Field = "title_template"
	FieldBody  Field = "body_template"
)

// SourceError is one source-span diagnostic. Start/End are byte offsets into
// the template source of Field.
type SourceError struct {
	Code    string
	Message string
	Field   Field
	Start   int
	End     int
}

// Warning reports a known variable that rendered empty.
type Warning struct {
	Code     string
	Message  string
	Variable string
}

// Message is the rendered {title, body} plus every diagnostic. Valid reports
// whether the message may be sent.
type Message struct {
	Title    string
	Body     string
	Errors   []SourceError
	Warnings []Warning
}

// Valid reports whether the render produced no errors.
func (m Message) Valid() bool { return len(m.Errors) == 0 }

// RenderMessage is the single notification rendering core. Preview, test
// delivery and business delivery all call it with the trigger's catalog
// variable set; only the facts differ (fixed sample facts or frozen payload
// facts). facts are keyed by registry fact paths — the same paths the
// payload was stored under. An unknown trigger is a programming error and
// returns ok=false.
func RenderMessage(trigger, titleTemplate, bodyTemplate string, facts map[string]any) (Message, bool) {
	known, ok := knownVariables(trigger)
	if !ok {
		return Message{}, false
	}
	title := renderText(titleTemplate, facts, known, FieldTitle)
	body := renderText(bodyTemplate, facts, known, FieldBody)
	message := Message{
		Title:    title.text,
		Body:     body.text,
		Errors:   append(append([]SourceError{}, title.errors...), body.errors...),
		Warnings: mergeWarnings(title.warnings, body.warnings),
	}
	if len(message.Errors) == 0 {
		message.Errors = outputErrors(message.Title, message.Body, len(titleTemplate), len(bodyTemplate))
	}
	return message, true
}

// Compile validates title/body templates against the trigger's catalog
// without facts. It returns only source diagnostics (syntax, unknown
// variables/functions, arity, size); output checks need facts and belong to
// RenderMessage.
func Compile(trigger, titleTemplate, bodyTemplate string) ([]SourceError, bool) {
	known, ok := knownVariables(trigger)
	if !ok {
		return nil, false
	}
	title := renderText(titleTemplate, nil, known, FieldTitle)
	body := renderText(bodyTemplate, nil, known, FieldBody)
	return append(append([]SourceError{}, title.errors...), body.errors...), true
}

// outputErrors rejects a message that cannot be stored or sent: an entirely
// blank message, or a title/body above the stored bounds.
func outputErrors(title, body string, titleSourceLen, bodySourceLen int) []SourceError {
	var errors []SourceError
	if utf8.RuneCountInString(title) > MaxRenderedTitleChars {
		errors = append(errors, SourceError{Code: "rendered_too_large", Message: fmt.Sprintf("渲染后的标题超过 %d 字符。", MaxRenderedTitleChars), Field: FieldTitle, End: titleSourceLen})
	}
	if utf8.RuneCountInString(body) > MaxRenderedBodyChars {
		errors = append(errors, SourceError{Code: "rendered_too_large", Message: fmt.Sprintf("渲染后的正文超过 %d 字符。", MaxRenderedBodyChars), Field: FieldBody, End: bodySourceLen})
	}
	if strings.TrimSpace(title) == "" && strings.TrimSpace(body) == "" {
		errors = append(errors, SourceError{Code: "rendered_empty", Message: "渲染后的标题和正文都为空。", Field: FieldBody, End: bodySourceLen})
	}
	return errors
}

// varsWildcard marks a known set that admits every vars.<group>.<name>.
const varsWildcard = "vars.*"

// showIfFunction is the closed line-visibility directive. Written at the start
// of a line, it shows the rest of that line when at least one argument has a
// value and drops the whole line when every argument is empty. It renders no
// text itself. The directive is per line by construction: there is no block,
// no closing tag and no nesting.
const showIfFunction = "showIf"

func knownVariables(trigger string) (map[string]bool, bool) {
	variables, ok := Variables(trigger)
	if !ok {
		return nil, false
	}
	known := make(map[string]bool, len(variables)+1)
	for _, variable := range variables {
		known[variable.Name] = true
	}
	known[varsWildcard] = true
	return known, true
}

// LookupFact returns one fact by its dotted name. Payloads store facts under
// flat dotted keys ("anime.title"); a nested object path is accepted as well.
func LookupFact(facts map[string]any, name string) (any, bool) {
	if value, ok := facts[name]; ok {
		return value, true
	}
	var current any = facts
	for _, part := range strings.Split(name, ".") {
		object, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = object[part]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

// FormatFact renders one fact value as plain text. JSON numbers decoded as
// float64 keep their integer form.
func FormatFact(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case json.Number:
		return typed.String()
	case float64:
		if typed == math.Trunc(typed) && math.Abs(typed) < 1e15 {
			return strconv.FormatInt(int64(typed), 10)
		}
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case float32:
		return FormatFact(float64(typed))
	case bool:
		return strconv.FormatBool(typed)
	case []any:
		// A list renders one entry per line; an entry object renders as the
		// line its own fields compose (see listEntryText).
		lines := make([]string, 0, len(typed))
		for _, item := range typed {
			if entry, ok := item.(map[string]any); ok {
				item = listEntryText(entry)
			}
			if text := FormatFact(item); text != "" {
				lines = append(lines, text)
			}
		}
		return strings.Join(lines, "\n")
	default:
		return fmt.Sprint(typed)
	}
}

// listEntryText renders one object entry of a list fact as a single line: its
// path, followed in parentheses by the version identity the entry carries
// (group and title, each only when present). An entry with no path but an
// identity still renders the identity.
func listEntryText(entry map[string]any) string {
	text := FormatFact(entry["path"])
	parts := make([]string, 0, 2)
	for _, key := range []string{"group", "title"} {
		if part := FormatFact(entry[key]); part != "" {
			parts = append(parts, part)
		}
	}
	if len(parts) == 0 {
		return text
	}
	identity := strings.Join(parts, " · ")
	if text == "" {
		return identity
	}
	return text + " (" + identity + ")"
}

type renderResult struct {
	text     string
	errors   []SourceError
	warnings []Warning
}

type templateExpression interface {
	evaluate(facts map[string]any) (string, bool, []Warning)
}

type literalExpression struct{ value string }

func (e literalExpression) evaluate(map[string]any) (string, bool, []Warning) {
	return e.value, true, nil
}

type variableExpression struct{ name string }

func (e variableExpression) evaluate(facts map[string]any) (string, bool, []Warning) {
	if e.name == ImageVariable || isImageVariable(e.name) {
		return "", true, nil
	}
	if isLinkVariable(e.name) {
		text := LinkValue(facts, e.name)
		if text == "" {
			return "", true, []Warning{{Code: "null_variable", Message: "变量没有值，已渲染为空：" + e.name, Variable: e.name}}
		}
		return text, true, nil
	}
	value, present := LookupFact(facts, e.name)
	text := FormatFact(value)
	if !present || value == nil || text == "" {
		return "", true, []Warning{{Code: "null_variable", Message: "变量没有值，已渲染为空：" + e.name, Variable: e.name}}
	}
	return text, true, nil
}

type callExpression struct {
	name string
	args []templateExpression
}

func (e callExpression) evaluate(facts map[string]any) (string, bool, []Warning) {
	if e.name == ImageVariable || e.name == "link" {
		var warnings []Warning
		if len(e.args) > 0 {
			if target, ok := e.args[0].(variableExpression); ok {
				_, _, warnings = target.evaluate(facts)
			}
		}
		return "", true, warnings
	}
	var warnings []Warning
	values := make([]string, 0, len(e.args))
	present := make([]bool, 0, len(e.args))
	for _, arg := range e.args {
		value, ok, childWarnings := arg.evaluate(facts)
		warnings = append(warnings, childWarnings...)
		values = append(values, value)
		present = append(present, ok)
	}
	return applyFunction(e.name, values, present, warnings)
}

func applyFunction(name string, values []string, present []bool, warnings []Warning) (string, bool, []Warning) {
	switch name {
	case "coalesce":
		for i := range values {
			if present[i] && strings.TrimSpace(values[i]) != "" {
				return values[i], true, warnings
			}
		}
		return "", true, warnings
	case "defaultIfEmpty":
		if present[0] && strings.TrimSpace(values[0]) != "" {
			return values[0], true, warnings
		}
		return values[1], true, warnings
	case "lower":
		return strings.ToLower(values[0]), present[0], warnings
	case "upper":
		return strings.ToUpper(values[0]), present[0], warnings
	case "replace":
		if !(present[0] && present[1] && present[2]) {
			return "", true, warnings
		}
		return strings.ReplaceAll(values[0], values[1], values[2]), true, warnings
	case "if":
		if !present[0] {
			return "", true, warnings
		}
		if isTruthy(values[0]) {
			return values[1], true, warnings
		}
		return values[2], true, warnings
	case "eq":
		if !(present[0] && present[1]) {
			return "", true, warnings
		}
		if values[0] == values[1] {
			return "true", true, warnings
		}
		return "false", true, warnings
	case showIfFunction:
		return "", true, warnings
	default:
		return "", false, warnings
	}
}

func isTruthy(value string) bool {
	trimmed := strings.TrimSpace(value)
	return strings.EqualFold(trimmed, "true") || trimmed == "1"
}

type templateSourceSegment struct {
	text          string
	originalStart int
}

func renderText(source string, facts map[string]any, known map[string]bool, field Field) renderResult {
	var builder strings.Builder
	var errors []SourceError
	var warnings []Warning
	if len(source) > maxTemplateBytes {
		errors = append(errors, SourceError{
			Code:    "template_too_large",
			End:     len(source),
			Field:   field,
			Message: fmt.Sprintf("模板超过 %d 字节限制。", maxTemplateBytes),
		})
		return renderResult{errors: errors}
	}
	semanticErrors, semanticWarnings := validateStandaloneSemanticDirectives(source, facts, known, field)
	errors = append(errors, semanticErrors...)
	warnings = append(warnings, semanticWarnings...)
	errors = append(errors, validateLineDirectives(source, known, field)...)
	for _, segment := range semanticSourceSegments(source) {
		result := renderTextSegment(source, segment, facts, known, field)
		builder.WriteString(result.text)
		errors = append(errors, result.errors...)
		warnings = append(warnings, result.warnings...)
	}
	sort.SliceStable(errors, func(i, j int) bool { return errors[i].Start < errors[j].Start })
	return renderResult{text: builder.String(), errors: errors, warnings: warnings}
}

func validateStandaloneSemanticDirectives(source string, facts map[string]any, known map[string]bool, field Field) ([]SourceError, []Warning) {
	var errors []SourceError
	var warnings []Warning
	for lineStart := 0; lineStart < len(source); {
		lineEnd := strings.IndexByte(source[lineStart:], '\n')
		if lineEnd < 0 {
			lineEnd = len(source)
		} else {
			lineEnd += lineStart + 1
		}
		line := source[lineStart:lineEnd]
		if isSemanticDirectiveLine(line) {
			start := strings.Index(line, "{{")
			afterOpen := line[start+2:]
			end := strings.Index(afterOpen, "}}")
			_, exprWarnings, exprError := evaluateExpression(afterOpen[:end], facts, known)
			if exprError != nil {
				errors = append(errors, SourceError{Code: exprError.code, Message: exprError.message, Field: field, Start: lineStart + start, End: lineStart + start + 2 + end + 2})
			} else {
				warnings = append(warnings, exprWarnings...)
			}
		}
		lineStart = lineEnd
	}
	return errors, warnings
}

// expressionSpan locates one {{...}} expression in a template source.
type expressionSpan struct {
	start  int
	end    int
	source string
}

// expressionSpans lists every expression of a field with its byte offsets,
// using the same scan the renderer performs.
func expressionSpans(source string) []expressionSpan {
	spans := make([]expressionSpan, 0, 8)
	for offset := 0; offset < len(source); {
		start := strings.Index(source[offset:], "{{")
		if start < 0 {
			break
		}
		start += offset
		afterOpen := source[start+2:]
		end := strings.Index(afterOpen, "}}")
		if end < 0 {
			break
		}
		spans = append(spans, expressionSpan{start: start, end: start + 2 + end + 2, source: afterOpen[:end]})
		offset = start + 2 + end + 2
	}
	return spans
}

// validateLineDirectives enforces the showIf placement contract and validates
// its arguments. The line renderer removes the directive before rendering, so
// these checks cannot ride on the ordinary expression pass.
func validateLineDirectives(source string, known map[string]bool, field Field) []SourceError {
	var errors []SourceError
	gatedLines := map[int]bool{}
	for _, span := range expressionSpans(source) {
		expression, err := parseExpression(strings.TrimSpace(span.source))
		if err != nil || !mentionsShowIf(expression) {
			continue
		}
		report := func(message string) {
			errors = append(errors, SourceError{Code: "invalid_expression", Message: message, Field: field, Start: span.start, End: span.end})
		}
		call, ok := expression.(callExpression)
		if !ok || call.name != showIfFunction {
			report("showIf 只能单独写在行首。")
			continue
		}
		lineStart := strings.LastIndexByte(source[:span.start], '\n') + 1
		if gatedLines[lineStart] {
			report("每行只能有一个 showIf。")
			continue
		}
		if strings.TrimSpace(source[lineStart:span.start]) != "" {
			report("showIf 只能写在行首。")
			continue
		}
		if strings.Contains(span.source, "\n") {
			report("showIf 必须写在一行内。")
			continue
		}
		nested := false
		for _, arg := range call.args {
			nested = nested || mentionsShowIf(arg)
		}
		if nested {
			report("showIf 不能作为参数使用。")
			continue
		}
		gatedLines[lineStart] = true
		if err := validateExpression(call, known); err != nil {
			report(err.Error())
		}
	}
	return errors
}

func mentionsShowIf(expression templateExpression) bool {
	call, ok := expression.(callExpression)
	if !ok {
		return false
	}
	if call.name == showIfFunction {
		return true
	}
	for _, arg := range call.args {
		if mentionsShowIf(arg) {
			return true
		}
	}
	return false
}

func renderTextSegment(source string, segment templateSourceSegment, facts map[string]any, known map[string]bool, field Field) renderResult {
	var builder strings.Builder
	var errors []SourceError
	var warnings []Warning
	remaining := segment.text
	cursor := segment.originalStart
	for len(remaining) > 0 {
		consumed := strings.IndexByte(remaining, '\n')
		if consumed < 0 {
			consumed = len(remaining)
		} else {
			consumed++
		}
		line := remaining[:consumed]
		// A string literal may contain a newline. Keep a multiline expression
		// in one scanner chunk; standalone-list detection still requires its
		// expression to begin and end on the same physical line.
		for search := 0; search < consumed; {
			start := strings.Index(remaining[search:], "{{")
			if start < 0 {
				break
			}
			start += search
			if start >= consumed {
				break
			}
			end := strings.Index(remaining[start+2:], "}}")
			if end < 0 {
				break
			}
			expressionEnd := start + 2 + end + 2
			if expressionEnd > consumed {
				consumed = expressionEnd
				if next := strings.IndexByte(remaining[consumed:], '\n'); next >= 0 {
					consumed += next + 1
				} else {
					consumed = len(remaining)
				}
				line = remaining[:consumed]
				break
			}
			search = expressionEnd
		}
		final := consumed == len(remaining)
		if gate, restOffset, gated := lineGate(line); gated {
			// A showIf line's directive renders no text: either the gate passes
			// and the rest of the line renders as its own line, or the whole
			// line disappears.
			rest := line[restOffset:]
			switch {
			case facts != nil && !gateHasValue(gate, facts):
				dropLine(&builder, final)
			case isAttachmentOnlyLine(rest):
				dropLine(&builder, final)
			default:
				renderLine(&builder, source, rest, cursor+restOffset, final, facts, known, field, &errors, &warnings)
			}
		} else {
			renderLine(&builder, source, line, cursor, final, facts, known, field, &errors, &warnings)
		}
		remaining = remaining[consumed:]
		cursor += consumed
	}
	return renderResult{text: builder.String(), errors: errors, warnings: warnings}
}

func renderPlainLine(source, remaining string, cursor int, facts map[string]any, known map[string]bool, field Field) renderResult {
	var builder strings.Builder
	var errors []SourceError
	var warnings []Warning
	for len(remaining) > 0 {
		start := strings.Index(remaining, "{{")
		if start < 0 {
			builder.WriteString(remaining)
			break
		}
		if start > 0 {
			builder.WriteString(remaining[:start])
		}
		expressionStart := cursor + start
		afterOpen := remaining[start+2:]
		end := strings.Index(afterOpen, "}}")
		if end < 0 {
			errors = append(errors, SourceError{
				Code:    "unterminated_expression",
				End:     len(source),
				Field:   field,
				Message: "表达式未正确闭合。",
				Start:   expressionStart,
			})
			break
		}
		inner := afterOpen[:end]
		expressionEnd := expressionStart + 2 + end + 2
		text, exprWarnings, exprError := evaluateExpression(inner, facts, known)
		if exprError != nil {
			errors = append(errors, SourceError{
				Code:    exprError.code,
				End:     expressionEnd,
				Field:   field,
				Message: exprError.message,
				Start:   expressionStart,
			})
		} else {
			builder.WriteString(text)
			warnings = append(warnings, exprWarnings...)
		}
		cursor = expressionEnd
		remaining = afterOpen[end+2:]
	}
	return renderResult{text: builder.String(), errors: errors, warnings: warnings}
}

// renderLine renders one line chunk: a standalone list variable repeats its
// prefix per item, everything else renders as plain text. A line that renders
// nothing is dropped; a dropped final line must not leave its line break behind.
func renderLine(builder *strings.Builder, source, line string, cursor int, final bool, facts map[string]any, known map[string]bool, field Field, errors *[]SourceError, warnings *[]Warning) {
	if text, _, handled, exprWarnings := renderStandaloneListLine(line, facts, known); handled {
		if text == "" {
			dropLine(builder, final)
		}
		builder.WriteString(text)
		*warnings = append(*warnings, exprWarnings...)
		return
	}
	result := renderPlainLine(source, line, cursor, facts, known, field)
	builder.WriteString(result.text)
	*errors = append(*errors, result.errors...)
	*warnings = append(*warnings, result.warnings...)
}

// lineGate returns the showIf directive a line begins with, ignoring leading
// spaces and tabs, and the offset where the rest of the line starts. The
// directive must stay on the line's first physical line; any other placement
// is left to the ordinary renderer, which reports it.
func lineGate(line string) (callExpression, int, bool) {
	offset := 0
	for offset < len(line) && (line[offset] == ' ' || line[offset] == '\t') {
		offset++
	}
	if !strings.HasPrefix(line[offset:], "{{") {
		return callExpression{}, 0, false
	}
	afterOpen := line[offset+2:]
	end := strings.Index(afterOpen, "}}")
	if end < 0 {
		return callExpression{}, 0, false
	}
	directive := afterOpen[:end]
	if strings.Contains(directive, "\n") {
		return callExpression{}, 0, false
	}
	expression, err := parseExpression(strings.TrimSpace(directive))
	if err != nil {
		return callExpression{}, 0, false
	}
	call, ok := expression.(callExpression)
	if !ok || call.name != showIfFunction {
		return callExpression{}, 0, false
	}
	return call, offset + 2 + end + 2, true
}

// gateHasValue reports whether any gated expression has a value. Arguments are
// evaluated silently: an empty gated variable is the case this directive
// exists for, not a warning.
func gateHasValue(gate callExpression, facts map[string]any) bool {
	for _, arg := range gate.args {
		if text, _, _ := arg.evaluate(facts); strings.TrimSpace(text) != "" {
			return true
		}
	}
	return false
}

// dropLine removes a line that rendered nothing. A line break between two
// shown lines stays out of the output; a dropped final line must not leave the
// previous line's break behind.
func dropLine(builder *strings.Builder, final bool) {
	if !final {
		return
	}
	plain := strings.TrimSuffix(builder.String(), "\n")
	plain = strings.TrimSuffix(plain, "\r")
	builder.Reset()
	builder.WriteString(plain)
}

// isAttachmentOnlyLine reports whether a line carries nothing but attachment
// directives and whitespace, so it renders no text line.
func isAttachmentOnlyLine(line string) bool {
	rest := line
	for {
		start := strings.Index(rest, "{{")
		if start < 0 {
			break
		}
		afterOpen := rest[start+2:]
		end := strings.Index(afterOpen, "}}")
		if end < 0 {
			return false
		}
		expression, err := parseExpression(strings.TrimSpace(afterOpen[:end]))
		if err != nil || !isAttachmentExpression(expression) {
			return false
		}
		rest = rest[:start] + afterOpen[end+2:]
	}
	return strings.TrimSpace(rest) == ""
}

// renderStandaloneListLine repeats a line's literal prefix for every list item
// and omits the whole line for an empty list. Only a direct list variable is
// special; functions and scalar variables retain the normal rendering rules.
func renderStandaloneListLine(line string, facts map[string]any, known map[string]bool) (string, int, bool, []Warning) {
	if facts == nil {
		return "", 0, false, nil
	}
	consumed := strings.IndexByte(line, '\n')
	if consumed < 0 {
		consumed = len(line)
	} else {
		consumed++
	}
	content := line[:consumed]
	start := strings.Index(content, "{{")
	if start < 0 {
		return "", 0, false, nil
	}
	afterOpen := content[start+2:]
	end := strings.Index(afterOpen, "}}")
	if end < 0 || strings.Contains(afterOpen[end+2:], "{{") {
		return "", 0, false, nil
	}
	expression, err := parseExpression(strings.TrimSpace(afterOpen[:end]))
	if err != nil {
		return "", 0, false, nil
	}
	variable, ok := expression.(variableExpression)
	if !ok || (!known[variable.name] && !(known[varsWildcard] && IsVarsPath(variable.name))) {
		return "", 0, false, nil
	}
	value, present := LookupFact(facts, variable.name)
	var items []string
	switch typed := value.(type) {
	case []string:
		items = append(items, typed...)
	case []any:
		for _, item := range typed {
			if text := FormatFact([]any{item}); text != "" {
				items = append(items, text)
			}
		}
	default:
		if variable.name != "out.subtitles" || (present && value != nil) {
			return "", 0, false, nil
		}
	}
	newline := ""
	if strings.HasSuffix(content, "\n") {
		newline = "\n"
	}
	if len(items) == 0 {
		return "", consumed, true, nil
	}
	prefix := content[:start]
	suffix := strings.TrimSuffix(afterOpen[end+2:], "\n")
	suffix = strings.TrimSuffix(suffix, "\r")
	lines := make([]string, 0, len(items))
	for _, item := range items {
		lines = append(lines, prefix+item+suffix)
	}
	return strings.Join(lines, "\n") + newline, consumed, true, nil
}

type expressionError struct {
	code    string
	message string
}

func evaluateExpression(inner string, facts map[string]any, known map[string]bool) (string, []Warning, *expressionError) {
	trimmed := strings.TrimSpace(inner)
	if trimmed == "" {
		return "", nil, &expressionError{code: "empty_expression", message: "表达式不能为空。"}
	}
	expr, err := parseExpression(trimmed)
	if err != nil {
		return "", nil, &expressionError{code: "invalid_expression", message: err.Error()}
	}
	if err := validateExpression(expr, known); err != nil {
		return "", nil, &expressionError{code: "invalid_expression", message: err.Error()}
	}
	text, _, warnings := expr.evaluate(facts)
	return text, warnings, nil
}

func validateExpression(expr templateExpression, known map[string]bool) error {
	switch node := expr.(type) {
	case variableExpression:
		if !known[node.name] && !(known[varsWildcard] && IsVarsPath(node.name)) {
			return fmt.Errorf("未知变量 %q", node.name)
		}
	case callExpression:
		def, ok := lookupFunction(node.name)
		if !ok {
			return fmt.Errorf("未知函数 %q", node.name)
		}
		if len(node.args) < def.minArgs || (def.maxArgs >= 0 && len(node.args) > def.maxArgs) {
			return fmt.Errorf("函数 %s 的参数数量无效", node.name)
		}
		if node.name == "link" || node.name == ImageVariable {
			return validateAttachmentCall(node, known)
		}
		for _, arg := range node.args {
			if err := validateExpression(arg, known); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateAttachmentCall(node callExpression, known map[string]bool) error {
	target := node.args[0]
	switch value := target.(type) {
	case variableExpression:
		if node.name == "link" && !isLinkVariable(value.name) {
			return fmt.Errorf("link 的第一个参数必须是链接变量或安全 URL 字面量")
		}
		if node.name == ImageVariable && !isImageVariable(value.name) {
			return fmt.Errorf("image 的参数必须是图片变量或安全 HTTPS URL 字面量")
		}
		if !known[value.name] {
			return fmt.Errorf("当前事件不支持变量 %q", value.name)
		}
	case literalExpression:
		safe := SafeExternalURL(value.value, node.name == ImageVariable)
		if node.name == ImageVariable {
			safe = SafeExternalImageURL(value.value)
		}
		if safe == "" {
			return fmt.Errorf("附件地址必须是安全的%s URL，且不能包含凭据", map[string]string{"link": "", ImageVariable: " HTTPS"}[node.name])
		}
	default:
		return fmt.Errorf("附件目标必须是变量或 URL 字面量")
	}
	if node.name == "link" {
		label, ok := node.args[1].(literalExpression)
		if !ok || !validLinkLabel(label.value) {
			return fmt.Errorf("link 的第二个参数必须是非空文字标签")
		}
	}
	return nil
}

// --- expression parser ---

type expressionParser struct {
	input string
	pos   int
}

func parseExpression(input string) (templateExpression, error) {
	p := &expressionParser{input: input}
	expr, err := p.parseDepth(0)
	if err != nil {
		return nil, err
	}
	p.skipSpace()
	if p.pos != len(p.input) {
		return nil, fmt.Errorf("表达式在位置 %d 存在意外字符", p.pos)
	}
	return expr, nil
}

func (p *expressionParser) parseDepth(depth int) (templateExpression, error) {
	if depth > maxExpressionDepth {
		return nil, fmt.Errorf("表达式嵌套超过最大深度 %d", maxExpressionDepth)
	}
	p.skipSpace()
	if p.pos >= len(p.input) {
		return nil, fmt.Errorf("表达式为空")
	}
	ch := p.input[p.pos]
	if ch == '\'' || ch == '"' {
		return p.parseString()
	}
	if ch == '-' || (ch >= '0' && ch <= '9') {
		start := p.pos
		p.pos++
		for p.pos < len(p.input) && (p.input[p.pos] == '.' || p.input[p.pos] == '_' || unicode.IsDigit(rune(p.input[p.pos]))) {
			p.pos++
		}
		return literalExpression{value: p.input[start:p.pos]}, nil
	}
	identifier, err := p.parseIdentifier()
	if err != nil {
		return nil, err
	}
	p.skipSpace()
	if p.pos >= len(p.input) || p.input[p.pos] != '(' {
		return variableExpression{name: identifier}, nil
	}
	p.pos++
	var args []templateExpression
	for {
		p.skipSpace()
		if p.pos < len(p.input) && p.input[p.pos] == ')' {
			p.pos++
			break
		}
		arg, err := p.parseDepth(depth + 1)
		if err != nil {
			return nil, err
		}
		args = append(args, arg)
		p.skipSpace()
		if p.pos >= len(p.input) {
			return nil, fmt.Errorf("函数调用未正确闭合")
		}
		if p.input[p.pos] == ')' {
			p.pos++
			break
		}
		if p.input[p.pos] != ',' {
			return nil, fmt.Errorf("函数参数之间缺少逗号（位置 %d）", p.pos)
		}
		p.pos++
	}
	return callExpression{name: identifier, args: args}, nil
}

func (p *expressionParser) parseIdentifier() (string, error) {
	start := p.pos
	for p.pos < len(p.input) {
		r := rune(p.input[p.pos])
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '.' {
			p.pos++
			continue
		}
		break
	}
	if start == p.pos {
		return "", fmt.Errorf("位置 %d 缺少标识符", p.pos)
	}
	return p.input[start:p.pos], nil
}

func (p *expressionParser) parseString() (templateExpression, error) {
	quote := p.input[p.pos]
	p.pos++
	var b strings.Builder
	for p.pos < len(p.input) {
		ch := p.input[p.pos]
		p.pos++
		if ch == quote {
			return literalExpression{value: b.String()}, nil
		}
		if ch == '\\' && p.pos < len(p.input) {
			b.WriteByte(p.input[p.pos])
			p.pos++
			continue
		}
		b.WriteByte(ch)
	}
	return nil, fmt.Errorf("字符串字面量未正确闭合")
}

func (p *expressionParser) skipSpace() {
	for p.pos < len(p.input) && unicode.IsSpace(rune(p.input[p.pos])) {
		p.pos++
	}
}

// mergeWarnings combines warnings and removes duplicates that arise when the
// same variable appears in both title and body.
func mergeWarnings(lists ...[]Warning) []Warning {
	merged := make([]Warning, 0)
	seen := make(map[string]bool)
	for _, list := range lists {
		for _, warning := range list {
			key := warning.Code + "|" + warning.Variable
			if warning.Variable == "" {
				key += warning.Message
			}
			if seen[key] {
				continue
			}
			seen[key] = true
			merged = append(merged, warning)
		}
	}
	return merged
}

// TemplateReferences returns the variable names a title/body template pair
// reads, as written (v1 names stay v1). Expressions that do not parse
// contribute nothing.
func TemplateReferences(title, body string) map[string]bool {
	refs := map[string]bool{}
	var walk func(templateExpression)
	walk = func(expr templateExpression) {
		switch node := expr.(type) {
		case variableExpression:
			refs[node.name] = true
		case callExpression:
			for _, arg := range node.args {
				walk(arg)
			}
		}
	}
	for _, source := range []string{title, body} {
		for remaining := source; ; {
			start := strings.Index(remaining, "{{")
			if start < 0 {
				break
			}
			afterOpen := remaining[start+2:]
			end := strings.Index(afterOpen, "}}")
			if end < 0 {
				break
			}
			if expr, err := parseExpression(strings.TrimSpace(afterOpen[:end])); err == nil {
				walk(expr)
			}
			remaining = afterOpen[end+2:]
		}
	}
	return refs
}
