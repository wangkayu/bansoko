package naming

import (
	"fmt"
	"strings"

	"github.com/dop251/goja/ast"
	"github.com/dop251/goja/parser"
)

// This file analyses the expression a naming template contributes, and it
// analyses exactly the string the engine hands to goja after preprocessing —
// not the IIFE wrapper the engine generates around it. Analysing the wrapper
// was the source of two defects: the wrapper's own `const` declarations looked
// like template references, and its generated shape hid which text the template
// actually wrote.
//
// The analyser answers four questions, and nothing else:
//
//  1. does every reference into a context root name a field the catalog
//     advertises (full path, not just the first segment);
//  2. does every `vars` reference name a group/key present in the injected
//     snapshot;
//  3. does every identifier name something that can exist — a context root, a
//     registered helper, a documented JS global, or a name the template itself
//     declares;
//  4. does a string-literal regex-helper argument satisfy the same pattern
//     syntax and size limits used at render time? This check lives in the
//     expression walk because this is where the parsed call AST is available.
//
// It deliberately does not restrict the language: a construct it cannot analyse
// is skipped rather than refused, so a legitimate template is never rejected
// because the analyser is incomplete. Only a reference it can prove wrong is an
// error. Compilation guarantees syntax and static-reference legality; it does
// not guarantee that a template renders successfully for a concrete file.

// templateGlobals is the documented whitelist of JS globals a template may
// reference. It covers the standard value/constructor surface a formatting
// expression needs; adding to it is a deliberate change, not an accident.
var templateGlobals = map[string]bool{
	"undefined": true, "NaN": true, "Infinity": true,
	"String": true, "Number": true, "Boolean": true, "BigInt": true, "Symbol": true,
	"Object": true, "Array": true, "Map": true, "Set": true, "Date": true,
	"Math": true, "JSON": true, "RegExp": true, "Promise": true,
	"Error": true, "TypeError": true, "RangeError": true, "SyntaxError": true,
	"parseInt": true, "parseFloat": true, "isNaN": true, "isFinite": true,
	"encodeURIComponent": true, "decodeURIComponent": true,
}

// templateRootAliases are the wrapper's own bindings that reach the template at
// runtime. `context` is not advertised by the catalog, but templates written
// against it must keep working, so its member chain is validated as the root it
// dereferences rather than rejected.
var templateRootAliases = map[string]bool{"context": true, "__ctx": true}

type catalogFieldIndex struct {
	// leaves is every advertised full path: the complete reference must hit it.
	leaves map[string]bool
	// nodes is every leaf plus every prefix of a leaf: a member chain may
	// traverse these, but must not end on one that is not a leaf.
	nodes map[string]bool
}

// validateTemplateLimits keeps the unbounded-expansion guard that predates the
// reference analysis: it is a size limit on the written template, not a
// reference check.
func validateTemplateLimits(source string, kind TemplateKind) error {
	if oversizedStringPattern.MatchString(source) || strings.Contains(source, ".repeat(") || strings.Contains(source, "Array(") {
		return &Error{Code: ErrCodeTemplateCompileError, Field: string(kind), Message: "unbounded string expansion is not allowed; use bounded helpers"}
	}
	return nil
}

// validateNamingExpression analyses one template expression. `vars` is the
// loaded naming-vars snapshot; nil means no snapshot was supplied and vars
// references are not checked, while a non-nil empty snapshot means "nothing is
// loaded", so every vars reference is refused.
//
// It returns the catalog fact paths the expression reads outside any
// conditional: those are the facts a render may not silently leave empty.
func validateNamingExpression(expression string, kind TemplateKind, vars map[string]map[string]string) (map[string]bool, error) {
	a, err := analyzeNamingExpression(languageProfileOf(), expression, kind, vars)
	if err != nil {
		return nil, err
	}
	return a.required, nil
}

func analyzeNamingExpression(profile *languageProfile, expression string, kind TemplateKind, vars map[string]map[string]string) (*expressionAnalyzer, error) {
	analyzer := newExpressionAnalyzer(profile, kind, vars)
	if err := analyzer.analyze(expression); err != nil {
		return nil, err
	}
	return analyzer, nil
}

func newExpressionAnalyzer(profile *languageProfile, kind TemplateKind, vars map[string]map[string]string) *expressionAnalyzer {
	return &expressionAnalyzer{profile: profile, kind: kind, vars: vars, index: profile.index, required: map[string]bool{}, optionalPaths: map[string]bool{}, referenced: map[string]bool{}}
}

// analyze parses and walks one template expression (or plain text). It is the
// only entry point that touches the parser, so a caller that only needs the
// interpolation spans can run the same walk with a widened identifier set (see
// allowShorthands) instead of scanning the source text.
func (a *expressionAnalyzer) analyze(expression string) error {
	if strings.TrimSpace(expression) == "" {
		return nil
	}
	program, err := parser.ParseFile(nil, "naming-expression", "("+expression+")", 0)
	if err != nil {
		return &Error{Code: ErrCodeTemplateCompileError, Field: string(a.kind), Message: err.Error()}
	}
	if len(program.Body) == 1 {
		if statement, ok := program.Body[0].(*ast.ExpressionStatement); ok {
			if literal, ok := statement.Expression.(*ast.TemplateLiteral); ok && literal.Tag == nil && len(literal.Elements) > 0 && literal.Elements[0].Valid {
				a.literalHead = literal.Elements[0].Parsed.String()
			}
		}
	}
	a.push()
	for _, statement := range program.Body {
		if err := a.statement(statement); err != nil {
			return err
		}
	}
	return nil
}

type expressionAnalyzer struct {
	profile *languageProfile
	kind    TemplateKind
	vars    map[string]map[string]string
	index   catalogFieldIndex
	scopes  []map[string]bool
	// Byte offsets in the parenthesized source for safe literal coercion.
	interpolations [][2]int
	// optional counts the conditionals currently being walked. A fact read
	// inside one is not required — the expression itself defines what happens
	// when the fact is absent, so an empty value is the template's own design
	// rather than missing evidence. required collects the facts read outside
	// any conditional, which is the set a render may not leave empty.
	optional int
	required map[string]bool
	// optionalPaths holds the fact paths a surrounding conditional already
	// guards, so a reference to one of them inside a branch stays optional.
	optionalPaths map[string]bool
	// referenced holds every catalog fact the template reads, conditional or
	// not: the set of values a render may look at.
	referenced map[string]bool
	// literalHead is the text a top-level untagged template literal opens with,
	// before its first interpolation (the whole text when it has none); empty
	// for any other expression. Every render of such a template starts with it.
	literalHead string
	// allowShorthands accepts a registered shorthand name as an identifier. It
	// exists only for the compile-time pre-pass that locates interpolation spans
	// in a template which still contains shorthands: that pass never validates a
	// template, because validation runs on the expanded expression.
	allowShorthands bool
}

func (a *expressionAnalyzer) push() { a.scopes = append(a.scopes, map[string]bool{}) }

func (a *expressionAnalyzer) pop() { a.scopes = a.scopes[:len(a.scopes)-1] }

func (a *expressionAnalyzer) declare(name string) {
	if name == "" || len(a.scopes) == 0 {
		return
	}
	a.scopes[len(a.scopes)-1][name] = true
}

// known reports whether an identifier may exist at this point: a context root,
// a wrapper alias, a `vars` reference, a registered helper, a documented JS
// global, or a name declared in an enclosing scope.
func (a *expressionAnalyzer) known(name string) bool {
	if a.allowShorthands {
		if _, ok := shorthandExpansions[name]; ok {
			return true
		}
	}
	if a.profile.isRoot(name) || name == "vars" || templateRootAliases[name] || templateGlobals[name] {
		return true
	}
	for _, definition := range helperDefinitions {
		if definition.Name == name {
			return true
		}
	}
	for i := len(a.scopes) - 1; i >= 0; i-- {
		if a.scopes[i][name] {
			return true
		}
	}
	return false
}

func (a *expressionAnalyzer) unknownField(path string) error {
	return &Error{Code: ErrCodeTemplateCompileError, Field: string(a.kind), Message: fmt.Sprintf("unknown context field %s", path)}
}

func (a *expressionAnalyzer) unknownIdentifier(name string) error {
	return &Error{Code: ErrCodeTemplateCompileError, Field: string(a.kind), Message: fmt.Sprintf("unknown template identifier %s", name)}
}

func (a *expressionAnalyzer) dynamicAccess() error {
	return &Error{Code: ErrCodeTemplateCompileError, Field: string(a.kind), Message: "dynamic context field access is not allowed"}
}

func (a *expressionAnalyzer) unknownVars(message string) error {
	return unknownNamingVars(string(a.kind), message)
}

// ---------------------------------------------------------------------------
// statements

func (a *expressionAnalyzer) statement(stmt ast.Statement) error {
	switch node := stmt.(type) {
	case nil:
		return nil
	case *ast.ExpressionStatement:
		return a.expression(node.Expression)
	case *ast.BlockStatement:
		a.push()
		defer a.pop()
		a.declareHoisted(node.List)
		for _, inner := range node.List {
			if err := a.statement(inner); err != nil {
				return err
			}
		}
		return nil
	case *ast.VariableStatement:
		for _, binding := range node.List {
			if err := a.binding(binding); err != nil {
				return err
			}
		}
		return nil
	case *ast.LexicalDeclaration:
		for _, binding := range node.List {
			if err := a.binding(binding); err != nil {
				return err
			}
		}
		return nil
	case *ast.ReturnStatement:
		return a.expression(node.Argument)
	case *ast.ThrowStatement:
		return a.expression(node.Argument)
	case *ast.IfStatement:
		if err := a.expression(node.Test); err != nil {
			return err
		}
		if err := a.statement(node.Consequent); err != nil {
			return err
		}
		return a.statement(node.Alternate)
	case *ast.WhileStatement:
		if err := a.expression(node.Test); err != nil {
			return err
		}
		return a.statement(node.Body)
	case *ast.DoWhileStatement:
		if err := a.statement(node.Body); err != nil {
			return err
		}
		return a.expression(node.Test)
	case *ast.ForStatement:
		if err := a.forLoopInitializer(node.Initializer); err != nil {
			return err
		}
		if err := a.expression(node.Test); err != nil {
			return err
		}
		if err := a.expression(node.Update); err != nil {
			return err
		}
		return a.statement(node.Body)
	case *ast.ForInStatement:
		if err := a.forInto(node.Into); err != nil {
			return err
		}
		if err := a.expression(node.Source); err != nil {
			return err
		}
		return a.statement(node.Body)
	case *ast.ForOfStatement:
		if err := a.forInto(node.Into); err != nil {
			return err
		}
		if err := a.expression(node.Source); err != nil {
			return err
		}
		return a.statement(node.Body)
	case *ast.SwitchStatement:
		if err := a.expression(node.Discriminant); err != nil {
			return err
		}
		for _, caseStatement := range node.Body {
			if err := a.expression(caseStatement.Test); err != nil {
				return err
			}
			for _, inner := range caseStatement.Consequent {
				if err := a.statement(inner); err != nil {
					return err
				}
			}
		}
		return nil
	case *ast.TryStatement:
		a.push()
		defer a.pop()
		if node.Body != nil {
			a.declareHoisted(node.Body.List)
			if err := a.statement(node.Body); err != nil {
				return err
			}
		}
		if node.Catch != nil {
			a.push()
			if err := a.bindingTarget(node.Catch.Parameter); err != nil {
				a.pop()
				return err
			}
			if node.Catch.Body != nil {
				a.declareHoisted(node.Catch.Body.List)
				if err := a.statement(node.Catch.Body); err != nil {
					a.pop()
					return err
				}
			}
			a.pop()
		}
		if node.Finally != nil {
			if err := a.statement(node.Finally); err != nil {
				return err
			}
		}
		return nil
	case *ast.LabelledStatement:
		return a.statement(node.Statement)
	case *ast.FunctionDeclaration:
		if node.Function == nil {
			return nil
		}
		if node.Function.Name != nil {
			a.declare(node.Function.Name.Name.String())
		}
		return a.function(node.Function.ParameterList, node.Function.Body, node.Function.DeclarationList)
	case *ast.ClassDeclaration:
		// Class bodies are not analysed: a construct we do not model is
		// skipped rather than refused.
		if node.Class != nil && node.Class.Name != nil {
			a.declare(node.Class.Name.Name.String())
		}
		return nil
	default:
		// Unknown statement shape: accept it.
		return nil
	}
}

func (a *expressionAnalyzer) forLoopInitializer(initializer ast.ForLoopInitializer) error {
	switch node := initializer.(type) {
	case nil:
		return nil
	case *ast.ForLoopInitializerExpression:
		return a.expression(node.Expression)
	case *ast.ForLoopInitializerVarDeclList:
		for _, binding := range node.List {
			if err := a.binding(binding); err != nil {
				return err
			}
		}
		return nil
	case *ast.ForLoopInitializerLexicalDecl:
		for _, binding := range node.LexicalDeclaration.List {
			if err := a.binding(binding); err != nil {
				return err
			}
		}
		return nil
	default:
		return nil
	}
}

func (a *expressionAnalyzer) forInto(into ast.ForInto) error {
	switch node := into.(type) {
	case nil:
		return nil
	case *ast.ForIntoExpression:
		return a.expression(node.Expression)
	case *ast.ForDeclaration:
		return a.bindingTarget(node.Target)
	default:
		return nil
	}
}

// declareHoisted declares the names a block or function body introduces before
// its statements are analysed, so a legitimate use-before-declaration is not
// mistaken for a typo. It walks the given statements, descends into blocks and
// declarations, and does not analyse references.
func (a *expressionAnalyzer) declareHoisted(list []ast.Statement) {
	for _, stmt := range list {
		switch node := stmt.(type) {
		case *ast.VariableStatement:
			for _, binding := range node.List {
				a.declareTarget(binding.Target)
			}
		case *ast.LexicalDeclaration:
			for _, binding := range node.List {
				a.declareTarget(binding.Target)
			}
		case *ast.FunctionDeclaration:
			if node.Function != nil && node.Function.Name != nil {
				a.declare(node.Function.Name.Name.String())
			}
		case *ast.ClassDeclaration:
			if node.Class != nil && node.Class.Name != nil {
				a.declare(node.Class.Name.Name.String())
			}
		case *ast.BlockStatement:
			a.declareHoisted(node.List)
		case *ast.TryStatement:
			if node.Body != nil {
				a.declareHoisted(node.Body.List)
			}
			if node.Catch != nil && node.Catch.Body != nil {
				a.declareHoisted(node.Catch.Body.List)
			}
			if node.Finally != nil {
				a.declareHoisted(node.Finally.List)
			}
		case *ast.IfStatement:
			if consequent, ok := node.Consequent.(*ast.BlockStatement); ok {
				a.declareHoisted(consequent.List)
			}
			if alternate, ok := node.Alternate.(*ast.BlockStatement); ok {
				a.declareHoisted(alternate.List)
			}
		case *ast.ForStatement:
			if body, ok := node.Body.(*ast.BlockStatement); ok {
				a.declareHoisted(body.List)
			}
		case *ast.WhileStatement:
			if body, ok := node.Body.(*ast.BlockStatement); ok {
				a.declareHoisted(body.List)
			}
		case *ast.DoWhileStatement:
			if body, ok := node.Body.(*ast.BlockStatement); ok {
				a.declareHoisted(body.List)
			}
		case *ast.LabelledStatement:
			a.declareHoisted([]ast.Statement{node.Statement})
		}
	}
}

// declareTarget declares every name a binding target introduces.
func (a *expressionAnalyzer) declareTarget(target ast.BindingTarget) {
	switch node := target.(type) {
	case nil:
		return
	case *ast.Identifier:
		a.declare(node.Name.String())
	case *ast.ArrayPattern:
		for _, element := range node.Elements {
			if target, ok := element.(ast.BindingTarget); ok {
				a.declareTarget(target)
			}
		}
		if target, ok := node.Rest.(ast.BindingTarget); ok {
			a.declareTarget(target)
		}
	case *ast.ObjectPattern:
		for _, property := range node.Properties {
			a.declareProperty(property)
		}
		if target, ok := node.Rest.(ast.BindingTarget); ok {
			a.declareTarget(target)
		}
	}
}

func (a *expressionAnalyzer) declareProperty(property ast.Property) {
	switch node := property.(type) {
	case *ast.PropertyShort:
		a.declareTarget(&node.Name)
	case *ast.PropertyKeyed:
		if target, ok := node.Value.(ast.BindingTarget); ok {
			a.declareTarget(target)
		}
	case *ast.SpreadElement:
		if target, ok := node.Expression.(ast.BindingTarget); ok {
			a.declareTarget(target)
		}
	}
}

func (a *expressionAnalyzer) binding(binding *ast.Binding) error {
	if binding == nil {
		return nil
	}
	if err := a.bindingTarget(binding.Target); err != nil {
		return err
	}
	return a.expression(binding.Initializer)
}

// bindingTarget declares a binding target and walks any computed member
// expressions inside it (an object pattern key can be computed).
func (a *expressionAnalyzer) bindingTarget(target ast.BindingTarget) error {
	switch node := target.(type) {
	case nil:
		return nil
	case *ast.Identifier:
		a.declare(node.Name.String())
		return nil
	case *ast.ArrayPattern:
		for _, element := range node.Elements {
			if target, ok := element.(ast.BindingTarget); ok {
				if err := a.bindingTarget(target); err != nil {
					return err
				}
			} else if expression, ok := element.(ast.Expression); ok {
				if err := a.expression(expression); err != nil {
					return err
				}
			}
		}
		if target, ok := node.Rest.(ast.BindingTarget); ok {
			return a.bindingTarget(target)
		}
		return nil
	case *ast.ObjectPattern:
		for _, property := range node.Properties {
			if err := a.patternProperty(property); err != nil {
				return err
			}
		}
		if target, ok := node.Rest.(ast.BindingTarget); ok {
			return a.bindingTarget(target)
		}
		return nil
	default:
		return nil
	}
}

func (a *expressionAnalyzer) patternProperty(property ast.Property) error {
	switch node := property.(type) {
	case *ast.PropertyShort:
		a.declareTarget(&node.Name)
		return nil
	case *ast.PropertyKeyed:
		if node.Computed {
			if err := a.expression(node.Key); err != nil {
				return err
			}
		}
		if target, ok := node.Value.(ast.BindingTarget); ok {
			return a.bindingTarget(target)
		}
		return nil
	case *ast.SpreadElement:
		if target, ok := node.Expression.(ast.BindingTarget); ok {
			return a.bindingTarget(target)
		}
		return nil
	default:
		return nil
	}
}

func (a *expressionAnalyzer) parameters(list *ast.ParameterList) error {
	if list == nil {
		return nil
	}
	for _, binding := range list.List {
		if err := a.bindingTarget(binding.Target); err != nil {
			return err
		}
	}
	if target, ok := list.Rest.(ast.BindingTarget); ok {
		return a.bindingTarget(target)
	}
	return nil
}

// function analyses a function body in its own scope: parameters and hoisted
// declarations are declared first, then the body is walked.
func (a *expressionAnalyzer) function(params *ast.ParameterList, body *ast.BlockStatement, declarations []*ast.VariableDeclaration) error {
	a.push()
	defer a.pop()
	if err := a.parameters(params); err != nil {
		return err
	}
	for _, declaration := range declarations {
		for _, binding := range declaration.List {
			a.declareTarget(binding.Target)
		}
	}
	if body == nil {
		return nil
	}
	a.declareHoisted(body.List)
	return a.statement(body)
}

// ---------------------------------------------------------------------------
// expressions

func (a *expressionAnalyzer) expression(expr ast.Expression) error {
	switch node := expr.(type) {
	case nil:
		return nil
	case *ast.Identifier:
		if !a.known(node.Name.String()) {
			return a.unknownIdentifier(node.Name.String())
		}
		return nil
	case *ast.DotExpression:
		return a.memberChain(node)
	case *ast.BracketExpression:
		return a.memberChain(node)
	case *ast.Optional:
		return a.expression(node.Expression)
	case *ast.OptionalChain:
		return a.expression(node.Expression)
	case *ast.TemplateLiteral:
		if node.Tag == nil {
			for _, value := range node.Expressions {
				a.interpolations = append(a.interpolations, [2]int{int(value.Idx0()) - 2, int(value.Idx1()) - 2})
			}
		}
		return a.expressions(node.Expressions)
	case *ast.CallExpression:
		if err := a.callee(node.Callee); err != nil {
			return err
		}
		name := ""
		if identifier, ok := node.Callee.(*ast.Identifier); ok {
			name = identifier.Name.String()
			if err := validateLiteralRegexArgument(a.kind, name, node.ArgumentList); err != nil {
				return err
			}
		}
		// defaultIfEmpty(value, fallback) and the deprecated
		// defaultValue(fallback, value) render the fallback whether or not the
		// value exists, so only the value argument is optional. coalesce(...)
		// returns the first non-empty argument, so every argument is a fallback,
		// and chain(...) is a list of independent part expressions, so every
		// part is referenced-not-required too.
		switch name {
		case "defaultIfEmpty", "defaultValue":
			valueIndex := 0
			if name == "defaultValue" {
				valueIndex = 1
			}
			for index, argument := range node.ArgumentList {
				if index == valueIndex {
					a.optional++
				}
				err := a.expression(argument)
				if index == valueIndex {
					a.optional--
				}
				if err != nil {
					return err
				}
			}
			return nil
		case "coalesce", "chain":
			a.optional++
			defer func() { a.optional-- }()
		}
		return a.expressions(node.ArgumentList)
	case *ast.NewExpression:
		if err := a.callee(node.Callee); err != nil {
			return err
		}
		return a.expressions(node.ArgumentList)
	case *ast.ConditionalExpression:
		// The test only selects a branch, so a fact it reads is not required by
		// itself. Both branches may rely on that same fact being absent: the
		// shipped templates write their optional tags exactly that way
		// (`${File.Media.VideoCodec ? ` [${File.Media.VideoCodec}]` : ''}`), so
		// the paths the test reads stay optional inside the branches. A fact a
		// branch reads that the test does not mention is required: nothing in
		// the expression handles its absence.
		saved := a.required
		a.required = map[string]bool{}
		testErr := a.expression(node.Test)
		guarded := a.required
		a.required = saved
		if testErr != nil {
			return testErr
		}
		if a.optionalPaths == nil {
			a.optionalPaths = map[string]bool{}
		}
		for path := range guarded {
			a.optionalPaths[path] = true
		}
		defer func() {
			for path := range guarded {
				delete(a.optionalPaths, path)
			}
		}()
		if err := a.expression(node.Consequent); err != nil {
			return err
		}
		return a.expression(node.Alternate)
	case *ast.BinaryExpression:
		if operator := node.Operator.String(); operator == "||" || operator == "??" {
			// `X || 'default'` and `X ?? 'default'` render without X by
			// design. `&&` has no fallback for its right side, so it is not an
			// optional context.
			a.optional++
			defer func() { a.optional-- }()
		}
		if err := a.expression(node.Left); err != nil {
			return err
		}
		return a.expression(node.Right)
	case *ast.AssignExpression:
		if err := a.expression(node.Left); err != nil {
			return err
		}
		return a.expression(node.Right)
	case *ast.UnaryExpression:
		return a.expression(node.Operand)
	case *ast.SequenceExpression:
		return a.expressions(node.Sequence)
	case *ast.ArrayLiteral:
		return a.expressions(node.Value)
	case *ast.ObjectLiteral:
		for _, property := range node.Value {
			if err := a.objectProperty(property); err != nil {
				return err
			}
		}
		return nil
	case *ast.ArrowFunctionLiteral:
		a.push()
		defer a.pop()
		if err := a.parameters(node.ParameterList); err != nil {
			return err
		}
		for _, declaration := range node.DeclarationList {
			for _, binding := range declaration.List {
				a.declareTarget(binding.Target)
			}
		}
		switch body := node.Body.(type) {
		case *ast.ExpressionBody:
			return a.expression(body.Expression)
		case *ast.BlockStatement:
			a.declareHoisted(body.List)
			return a.statement(body)
		default:
			return nil
		}
	case *ast.FunctionLiteral:
		return a.function(node.ParameterList, node.Body, node.DeclarationList)
	case *ast.YieldExpression:
		return a.expression(node.Argument)
	case *ast.AwaitExpression:
		return a.expression(node.Argument)
	case *ast.SpreadElement:
		return a.expression(node.Expression)
	default:
		// Literals, `this`, and any construct this analyser does not model are
		// accepted: only references we can prove wrong are errors.
		return nil
	}
}

func (a *expressionAnalyzer) expressions(list []ast.Expression) error {
	for _, expression := range list {
		if err := a.expression(expression); err != nil {
			return err
		}
	}
	return nil
}

func (a *expressionAnalyzer) objectProperty(property ast.Property) error {
	switch node := property.(type) {
	case *ast.PropertyShort:
		// A shorthand property references the outer binding of the same name;
		// a keyed one only names the key.
		if node.Initializer == nil {
			if !a.known(node.Name.Name.String()) {
				return a.unknownIdentifier(node.Name.Name.String())
			}
			return nil
		}
		return a.expression(node.Initializer)
	case *ast.PropertyKeyed:
		if node.Computed {
			if err := a.expression(node.Key); err != nil {
				return err
			}
		}
		return a.expression(node.Value)
	case *ast.SpreadElement:
		return a.expression(node.Expression)
	default:
		return nil
	}
}

// callee walks a call or construction target. A member chain target is
// validated as a reference; anything else is walked as an expression.
func (a *expressionAnalyzer) callee(target ast.Expression) error {
	switch target.(type) {
	case *ast.DotExpression, *ast.BracketExpression:
		return a.memberChain(target)
	default:
		return a.expression(target)
	}
}

// ---------------------------------------------------------------------------
// member chains

type memberChainResult struct {
	root     string
	segments []string
	dynamic  bool
	ok       bool
	// nodes are the chain's nodes from the root identifier outwards:
	// nodes[0] is the root, nodes[i] the member access adding segments[i-1]
	// (aligned only for a chain that is not dynamic). dynamicMembers are the
	// non-literal bracket indices.
	nodes          []ast.Expression
	dynamicMembers []ast.Expression
}

// resolveMemberChain flattens a member chain into its root identifier and the
// path segments read from it. Static string indices count as segments; any
// other index marks the chain dynamic.
func resolveMemberChain(expr ast.Expression) memberChainResult {
	var result memberChainResult
	node := expr
	for {
		switch current := node.(type) {
		case *ast.Optional:
			node = current.Expression
		case *ast.OptionalChain:
			node = current.Expression
		case *ast.DotExpression:
			result.segments = append([]string{current.Identifier.Name.String()}, result.segments...)
			result.nodes = append([]ast.Expression{current}, result.nodes...)
			node = current.Left
		case *ast.BracketExpression:
			if key, ok := current.Member.(*ast.StringLiteral); ok {
				result.segments = append([]string{key.Value.String()}, result.segments...)
				result.nodes = append([]ast.Expression{current}, result.nodes...)
			} else {
				result.dynamic = true
				result.dynamicMembers = append(result.dynamicMembers, current.Member)
			}
			node = current.Left
		case *ast.Identifier:
			result.root = current.Name.String()
			result.nodes = append([]ast.Expression{current}, result.nodes...)
			result.ok = true
			return result
		default:
			return memberChainResult{dynamic: result.dynamic}
		}
	}
}

func (a *expressionAnalyzer) memberChain(expr ast.Expression) error {
	chain := resolveMemberChain(expr)
	if !chain.ok {
		// A chain that does not start at an identifier (`foo().bar`, `[1,2][0]`)
		// is walked as an expression instead.
		return a.expressionWithoutChain(expr)
	}
	if err := a.checkChain(chain); err != nil {
		return err
	}
	return nil
}

// expressionWithoutChain walks the parts of a chain that do not resolve to an
// identifier root, so their sub-expressions are still analysed.
func (a *expressionAnalyzer) expressionWithoutChain(expr ast.Expression) error {
	switch node := expr.(type) {
	case *ast.Optional:
		return a.expressionWithoutChain(node.Expression)
	case *ast.OptionalChain:
		return a.expressionWithoutChain(node.Expression)
	case *ast.DotExpression:
		return a.expressionWithoutChain(node.Left)
	case *ast.BracketExpression:
		if err := a.expressionWithoutChain(node.Left); err != nil {
			return err
		}
		return a.expression(node.Member)
	case *ast.CallExpression:
		if err := a.callee(node.Callee); err != nil {
			return err
		}
		return a.expressions(node.ArgumentList)
	default:
		return a.expression(expr)
	}
}

func (a *expressionAnalyzer) checkChain(chain memberChainResult) error {
	if !a.known(chain.root) {
		return a.unknownIdentifier(chain.root)
	}
	switch {
	case chain.root == "vars":
		return a.checkVarsChain(chain)
	case a.profile.isRoot(chain.root):
		return a.checkFieldChain(chain.root, chain.segments, chain.dynamic)
	case templateRootAliases[chain.root]:
		if len(chain.segments) == 0 {
			return nil
		}
		inner := chain.segments[0]
		if inner == "Vars" || inner == "vars" {
			return a.checkVarsChain(memberChainResult{root: "vars", segments: chain.segments[1:], dynamic: chain.dynamic})
		}
		if a.profile.isRoot(inner) {
			return a.checkFieldChain(inner, chain.segments[1:], chain.dynamic)
		}
		return a.unknownField(chain.root + "." + inner)
	default:
		// A property read from a local binding or another expression: its shape
		// is not the catalog's business.
		return nil
	}
}

func (a *expressionAnalyzer) checkVarsChain(chain memberChainResult) error {
	if a.vars == nil {
		// No snapshot was supplied: vars references are not checked here.
		return nil
	}
	if chain.dynamic {
		return a.unknownVars("dynamic or unsupported vars reference")
	}
	if len(chain.segments) < 2 {
		return nil
	}
	if len(chain.segments) != 2 {
		return a.unknownVars("dynamic or unsupported vars reference")
	}
	return validateNamingVarsPath(a.vars, chain.segments[0], chain.segments[1], string(a.kind))
}

// checkFieldChain enforces the catalog as the leaf whitelist: the chain may
// traverse intermediate nodes, but the complete reference must end on a
// whitelisted leaf. Segments after a leaf are JS-level access on that value —
// a method call such as `File.Basename.split(' ')` — and are allowed.
func (a *expressionAnalyzer) checkFieldChain(root string, segments []string, dynamic bool) error {
	if dynamic && !a.index.leaves[root] {
		return a.dynamicAccess()
	}
	if !a.index.nodes[root] {
		return a.unknownField(root)
	}
	path := root
	for _, segment := range segments {
		if segment == "" {
			return a.dynamicAccess()
		}
		next := path + "." + segment
		if a.index.nodes[next] {
			path = next
			continue
		}
		if a.index.leaves[path] {
			// Reached a whitelisted leaf; the rest is access on its value.
			a.recordRequired(path)
			return nil
		}
		return a.unknownField(next)
	}
	if a.index.leaves[path] {
		a.recordRequired(path)
		return nil
	}
	return a.unknownField(path)
}

// recordRequired notes one catalog fact the template reads where an empty value
// changes the rendered name instead of being handled by the expression itself.
func (a *expressionAnalyzer) recordRequired(path string) {
	if a.referenced != nil {
		a.referenced[path] = true
	}
	if a.optional != 0 || a.optionalPaths[path] || a.required == nil {
		return
	}
	a.required[path] = true
}
