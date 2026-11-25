package naming

import (
	"fmt"
	"strings"

	"github.com/dop251/goja/ast"
	"github.com/dop251/goja/parser"
)

// TemplateReferencesVars reports whether a naming template expression reads the
// naming Vars namespace (`vars`, `context.vars`, `context.Vars`, `__ctx.vars`,
// `__ctx.Vars`, including bracket access and destructuring) via goja AST
// analysis. Plain template text, string literals, comments, object literal keys,
// and locally shadowed `vars`/`context`/`__ctx` bindings are ignored. An
// unparseable template is conservatively reported as a reference; callers that
// need a diagnostic use CheckTemplateReferencesVars.
func TemplateReferencesVars(source string) bool {
	found, err := CheckTemplateReferencesVars(source)
	return err != nil || found
}

// CheckTemplateReferencesVars is TemplateReferencesVars with fail-closed parse
// diagnostics for migration preflight callers.
func CheckTemplateReferencesVars(source string) (bool, error) {
	if strings.TrimSpace(source) == "" {
		return false, nil
	}
	expression := "`" + source + "`"
	program, err := parser.ParseFile(nil, "naming-expression", "("+expression+")", 0)
	if err != nil {
		// Fall back to parsing as a raw expression in case a stored template
		// already carries enclosing backticks or expression syntax.
		program, err = parser.ParseFile(nil, "naming-expression", "("+source+")", 0)
		if err != nil {
			return false, fmt.Errorf("parse naming template: %w", err)
		}
	}
	w := &varsRefWalker{}
	w.push()
	w.declareHoisted(program.Body)
	for _, stmt := range program.Body {
		w.statement(stmt)
		if w.found {
			return true, nil
		}
	}
	return w.found, nil
}

// RulesVarsReferences returns the sorted field names within rules whose
// template references naming Vars. Any unparseable template aborts the scan so
// an upgrade preflight cannot mistake incomplete analysis for a clean result.
func RulesVarsReferences(rules NamingRules) ([]string, error) {
	checks := []struct {
		field  string
		source string
	}{
		{"movie.directory_template", rules.Movie.DirectoryTemplate},
		{"movie.video_filename_template", rules.Movie.VideoFilenameTemplate},
		{"subtitle_suffix_template", rules.SubtitleSuffixTemplate},
		{"tv.directory_template", rules.TV.DirectoryTemplate},
		{"tv.video_filename_template", rules.TV.VideoFilenameTemplate},
		{"unknown.directory_template", rules.Unknown.DirectoryTemplate},
		{"unknown.video_filename_template", rules.Unknown.VideoFilenameTemplate},
	}
	var hits []string
	for _, item := range checks {
		found, err := CheckTemplateReferencesVars(item.source)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", item.field, err)
		}
		if found {
			hits = append(hits, item.field)
		}
	}
	return hits, nil
}

type varsRefWalker struct {
	scopes []map[string]bool
	found  bool
}

func (w *varsRefWalker) push() { w.scopes = append(w.scopes, map[string]bool{}) }
func (w *varsRefWalker) pop()  { w.scopes = w.scopes[:len(w.scopes)-1] }

func (w *varsRefWalker) declare(name string) {
	if name == "" || len(w.scopes) == 0 {
		return
	}
	w.scopes[len(w.scopes)-1][name] = true
}

func (w *varsRefWalker) isShadowed(name string) bool {
	for i := len(w.scopes) - 1; i >= 0; i-- {
		if w.scopes[i][name] {
			return true
		}
	}
	return false
}

func (w *varsRefWalker) isUnshadowedVars(name string) bool {
	return name == "vars" && !w.isShadowed("vars")
}

func (w *varsRefWalker) isUnshadowedContextAlias(name string) bool {
	return templateRootAliases[name] && !w.isShadowed(name)
}

func isVarsKey(name string) bool {
	return name == "vars" || name == "Vars"
}

func (w *varsRefWalker) declareHoisted(statements []ast.Statement) {
	for _, stmt := range statements {
		switch node := stmt.(type) {
		case *ast.VariableStatement:
			for _, b := range node.List {
				w.declareTarget(b.Target)
			}
		case *ast.FunctionDeclaration:
			if node.Function != nil && node.Function.Name != nil {
				w.declare(node.Function.Name.Name.String())
			}
		case *ast.ClassDeclaration:
			if node.Class != nil && node.Class.Name != nil {
				w.declare(node.Class.Name.Name.String())
			}
		case *ast.BlockStatement:
			w.declareHoisted(node.List)
		case *ast.TryStatement:
			if node.Body != nil {
				w.declareHoisted(node.Body.List)
			}
			if node.Catch != nil && node.Catch.Body != nil {
				w.declareHoisted(node.Catch.Body.List)
			}
			if node.Finally != nil {
				w.declareHoisted(node.Finally.List)
			}
		case *ast.IfStatement:
			if b, ok := node.Consequent.(*ast.BlockStatement); ok {
				w.declareHoisted(b.List)
			}
			if b, ok := node.Alternate.(*ast.BlockStatement); ok {
				w.declareHoisted(b.List)
			}
		case *ast.ForStatement:
			if b, ok := node.Body.(*ast.BlockStatement); ok {
				w.declareHoisted(b.List)
			}
		case *ast.WhileStatement:
			if b, ok := node.Body.(*ast.BlockStatement); ok {
				w.declareHoisted(b.List)
			}
		case *ast.DoWhileStatement:
			if b, ok := node.Body.(*ast.BlockStatement); ok {
				w.declareHoisted(b.List)
			}
		case *ast.LabelledStatement:
			w.declareHoisted([]ast.Statement{node.Statement})
		}
	}
}

func (w *varsRefWalker) declareTarget(target ast.BindingTarget) {
	switch node := target.(type) {
	case nil:
		return
	case *ast.Identifier:
		w.declare(node.Name.String())
	case *ast.ArrayPattern:
		for _, element := range node.Elements {
			if t, ok := element.(ast.BindingTarget); ok {
				w.declareTarget(t)
			}
		}
		if t, ok := node.Rest.(ast.BindingTarget); ok {
			w.declareTarget(t)
		}
	case *ast.ObjectPattern:
		for _, prop := range node.Properties {
			switch p := prop.(type) {
			case *ast.PropertyShort:
				w.declareTarget(&p.Name)
			case *ast.PropertyKeyed:
				if t, ok := p.Value.(ast.BindingTarget); ok {
					w.declareTarget(t)
				}
			case *ast.SpreadElement:
				if t, ok := p.Expression.(ast.BindingTarget); ok {
					w.declareTarget(t)
				}
			}
		}
		if t, ok := node.Rest.(ast.BindingTarget); ok {
			w.declareTarget(t)
		}
	}
}

// patternReadsVars reports whether an ObjectPattern destructures a `vars` or
// `Vars` property (including string-literal computed keys or rest patterns).
func patternReadsVars(pattern *ast.ObjectPattern) bool {
	if pattern == nil {
		return false
	}
	if pattern.Rest != nil {
		return true
	}
	for _, prop := range pattern.Properties {
		switch p := prop.(type) {
		case *ast.PropertyShort:
			if isVarsKey(p.Name.Name.String()) {
				return true
			}
		case *ast.PropertyKeyed:
			if key, ok := propertyKeyName(p); ok && isVarsKey(key) {
				return true
			}
		case *ast.SpreadElement:
			return true
		}
	}
	return false
}

func propertyKeyName(p *ast.PropertyKeyed) (string, bool) {
	if p == nil {
		return "", false
	}
	switch k := p.Key.(type) {
	case *ast.StringLiteral:
		return k.Value.String(), true
	case *ast.Identifier:
		if !p.Computed {
			return k.Name.String(), true
		}
	}
	return "", false
}

func (w *varsRefWalker) isContextAliasExpr(expr ast.Expression) bool {
	if expr == nil {
		return false
	}
	for {
		switch node := expr.(type) {
		case *ast.Optional:
			expr = node.Expression
		case *ast.OptionalChain:
			expr = node.Expression
		case *ast.Identifier:
			return w.isUnshadowedContextAlias(node.Name.String())
		default:
			return false
		}
	}
}

func (w *varsRefWalker) statement(stmt ast.Statement) {
	if w.found || stmt == nil {
		return
	}
	switch node := stmt.(type) {
	case *ast.ExpressionStatement:
		w.expression(node.Expression)
	case *ast.BlockStatement:
		w.push()
		defer w.pop()
		w.declareHoisted(node.List)
		for _, inner := range node.List {
			w.statement(inner)
			if w.found {
				return
			}
		}
	case *ast.VariableStatement:
		for _, b := range node.List {
			w.binding(b)
		}
	case *ast.LexicalDeclaration:
		for _, b := range node.List {
			w.binding(b)
		}
	case *ast.ReturnStatement:
		w.expression(node.Argument)
	case *ast.ThrowStatement:
		w.expression(node.Argument)
	case *ast.IfStatement:
		w.expression(node.Test)
		w.statement(node.Consequent)
		w.statement(node.Alternate)
	case *ast.WhileStatement:
		w.expression(node.Test)
		w.statement(node.Body)
	case *ast.DoWhileStatement:
		w.statement(node.Body)
		w.expression(node.Test)
	case *ast.ForStatement:
		w.push()
		defer w.pop()
		switch init := node.Initializer.(type) {
		case *ast.ForLoopInitializerExpression:
			w.expression(init.Expression)
		case *ast.ForLoopInitializerVarDeclList:
			for _, b := range init.List {
				w.binding(b)
			}
		case *ast.ForLoopInitializerLexicalDecl:
			for _, b := range init.LexicalDeclaration.List {
				w.binding(b)
			}
		}
		w.expression(node.Test)
		w.expression(node.Update)
		w.statement(node.Body)
	case *ast.ForInStatement:
		w.push()
		defer w.pop()
		w.forInto(node.Into)
		w.expression(node.Source)
		w.statement(node.Body)
	case *ast.ForOfStatement:
		w.push()
		defer w.pop()
		w.forInto(node.Into)
		w.expression(node.Source)
		w.statement(node.Body)
	case *ast.SwitchStatement:
		w.expression(node.Discriminant)
		for _, cs := range node.Body {
			w.expression(cs.Test)
			for _, inner := range cs.Consequent {
				w.statement(inner)
			}
		}
	case *ast.TryStatement:
		w.push()
		if node.Body != nil {
			w.declareHoisted(node.Body.List)
			w.statement(node.Body)
		}
		w.pop()
		if node.Catch != nil {
			w.push()
			if node.Catch.Parameter != nil {
				w.bindingTarget(node.Catch.Parameter)
			}
			if node.Catch.Body != nil {
				w.declareHoisted(node.Catch.Body.List)
				w.statement(node.Catch.Body)
			}
			w.pop()
		}
		if node.Finally != nil {
			w.push()
			w.declareHoisted(node.Finally.List)
			w.statement(node.Finally)
			w.pop()
		}
	case *ast.FunctionDeclaration:
		if node.Function != nil {
			if node.Function.Name != nil {
				w.declare(node.Function.Name.Name.String())
			}
			w.function(node.Function.ParameterList, node.Function.Body, node.Function.DeclarationList)
		}
	case *ast.LabelledStatement:
		w.statement(node.Statement)
	}
}

func (w *varsRefWalker) forInto(into ast.ForInto) {
	switch node := into.(type) {
	case *ast.ForIntoVar:
		w.binding(node.Binding)
	case *ast.ForIntoExpression:
		w.expression(node.Expression)
	case *ast.ForDeclaration:
		w.bindingTarget(node.Target)
	}
}

func (w *varsRefWalker) binding(b *ast.Binding) {
	if w.found || b == nil {
		return
	}
	// Evaluate initializer BEFORE declaring the lexical binding so
	// `const vars = vars.a` catches the outer `vars`, while `const vars = {a: 1}; vars.a`
	// sees the shadowed `vars` afterwards.
	if b.Initializer != nil {
		if pat, ok := b.Target.(*ast.ObjectPattern); ok && w.isContextAliasExpr(b.Initializer) && patternReadsVars(pat) {
			w.found = true
			return
		}
		w.expression(b.Initializer)
	}
	w.bindingTarget(b.Target)
}

func (w *varsRefWalker) bindingTarget(target ast.BindingTarget) {
	if w.found || target == nil {
		return
	}
	switch node := target.(type) {
	case *ast.Identifier:
		w.declare(node.Name.String())
	case *ast.ArrayPattern:
		for _, element := range node.Elements {
			if t, ok := element.(ast.BindingTarget); ok {
				w.bindingTarget(t)
			} else if expr, ok := element.(ast.Expression); ok {
				w.expression(expr)
			}
		}
		if t, ok := node.Rest.(ast.BindingTarget); ok {
			w.bindingTarget(t)
		}
	case *ast.ObjectPattern:
		for _, prop := range node.Properties {
			switch p := prop.(type) {
			case *ast.PropertyShort:
				if p.Initializer != nil {
					w.expression(p.Initializer)
				}
				w.declareTarget(&p.Name)
			case *ast.PropertyKeyed:
				if p.Computed {
					w.expression(p.Key)
				}
				if t, ok := p.Value.(ast.BindingTarget); ok {
					w.bindingTarget(t)
				} else if expr, ok := p.Value.(ast.Expression); ok {
					w.expression(expr)
				}
			case *ast.SpreadElement:
				if t, ok := p.Expression.(ast.BindingTarget); ok {
					w.bindingTarget(t)
				}
			}
		}
		if t, ok := node.Rest.(ast.BindingTarget); ok {
			w.bindingTarget(t)
		}
	}
}

func (w *varsRefWalker) parameters(list *ast.ParameterList) {
	if list == nil {
		return
	}
	for _, b := range list.List {
		if b.Initializer != nil {
			w.expression(b.Initializer)
		}
		w.bindingTarget(b.Target)
	}
	if t, ok := list.Rest.(ast.BindingTarget); ok {
		w.bindingTarget(t)
	}
}

func (w *varsRefWalker) function(params *ast.ParameterList, body *ast.BlockStatement, declarations []*ast.VariableDeclaration) {
	w.push()
	defer w.pop()
	w.parameters(params)
	for _, decl := range declarations {
		for _, b := range decl.List {
			w.declareTarget(b.Target)
		}
	}
	if body == nil {
		return
	}
	w.declareHoisted(body.List)
	w.statement(body)
}

func (w *varsRefWalker) expression(expr ast.Expression) {
	if w.found || expr == nil {
		return
	}
	switch node := expr.(type) {
	case *ast.Identifier:
		if w.isUnshadowedVars(node.Name.String()) {
			w.found = true
		}
	case *ast.DotExpression, *ast.BracketExpression:
		w.memberChain(node)
	case *ast.Optional:
		w.expression(node.Expression)
	case *ast.OptionalChain:
		w.expression(node.Expression)
	case *ast.TemplateLiteral:
		if node.Tag != nil {
			w.expression(node.Tag)
		}
		w.expressions(node.Expressions)
	case *ast.CallExpression:
		w.expression(node.Callee)
		w.expressions(node.ArgumentList)
	case *ast.NewExpression:
		w.expression(node.Callee)
		w.expressions(node.ArgumentList)
	case *ast.ConditionalExpression:
		w.expression(node.Test)
		w.expression(node.Consequent)
		w.expression(node.Alternate)
	case *ast.BinaryExpression:
		w.expression(node.Left)
		w.expression(node.Right)
	case *ast.AssignExpression:
		// Handle destructuring assignment such as `({ vars } = context)`.
		if pat, ok := node.Left.(*ast.ObjectPattern); ok && w.isContextAliasExpr(node.Right) && patternReadsVars(pat) {
			w.found = true
			return
		}
		if obj, ok := node.Left.(*ast.ObjectLiteral); ok && w.isContextAliasExpr(node.Right) {
			for _, prop := range obj.Value {
				switch p := prop.(type) {
				case *ast.PropertyShort:
					if isVarsKey(p.Name.Name.String()) {
						w.found = true
						return
					}
				case *ast.PropertyKeyed:
					if key, ok := propertyKeyName(p); ok && isVarsKey(key) {
						w.found = true
						return
					}
				case *ast.SpreadElement:
					w.found = true
					return
				}
			}
		}
		w.expression(node.Left)
		w.expression(node.Right)
	case *ast.UnaryExpression:
		w.expression(node.Operand)
	case *ast.SequenceExpression:
		w.expressions(node.Sequence)
	case *ast.ArrayLiteral:
		w.expressions(node.Value)
	case *ast.ObjectLiteral:
		for _, prop := range node.Value {
			switch p := prop.(type) {
			case *ast.PropertyShort:
				if p.Initializer != nil {
					w.expression(p.Initializer)
				} else if w.isUnshadowedVars(p.Name.Name.String()) {
					w.found = true
					return
				}
			case *ast.PropertyKeyed:
				if p.Computed {
					w.expression(p.Key)
				}
				w.expression(p.Value)
			case *ast.SpreadElement:
				w.expression(p.Expression)
			}
		}
	case *ast.ArrowFunctionLiteral:
		w.push()
		defer w.pop()
		w.parameters(node.ParameterList)
		for _, decl := range node.DeclarationList {
			for _, b := range decl.List {
				w.declareTarget(b.Target)
			}
		}
		switch body := node.Body.(type) {
		case *ast.ExpressionBody:
			w.expression(body.Expression)
		case *ast.BlockStatement:
			w.declareHoisted(body.List)
			w.statement(body)
		}
	case *ast.FunctionLiteral:
		w.function(node.ParameterList, node.Body, node.DeclarationList)
	case *ast.YieldExpression:
		w.expression(node.Argument)
	case *ast.AwaitExpression:
		w.expression(node.Argument)
	case *ast.SpreadElement:
		w.expression(node.Expression)
	}
}

func (w *varsRefWalker) expressions(list []ast.Expression) {
	for _, expr := range list {
		w.expression(expr)
		if w.found {
			return
		}
	}
}

func (w *varsRefWalker) memberChain(expr ast.Expression) {
	chain := resolveMemberChain(expr)
	if chain.ok {
		if w.isUnshadowedVars(chain.root) {
			w.found = true
			return
		}
		if w.isUnshadowedContextAlias(chain.root) && len(chain.segments) > 0 && isVarsKey(chain.segments[0]) {
			w.found = true
			return
		}
		// Walk dynamic bracket index expressions so e.g. `arr[vars.a.b]` is caught.
		for _, dyn := range chain.dynamicMembers {
			w.expression(dyn)
			if w.found {
				return
			}
		}
		return
	}
	// Chain does not start at an identifier (e.g. `foo(vars.a.b).bar`): walk subexpressions.
	switch node := expr.(type) {
	case *ast.Optional:
		w.memberChain(node.Expression)
	case *ast.OptionalChain:
		w.memberChain(node.Expression)
	case *ast.DotExpression:
		w.expression(node.Left)
	case *ast.BracketExpression:
		w.expression(node.Left)
		w.expression(node.Member)
	default:
		w.expression(expr)
	}
}
