package notification

import (
	"fmt"
	"strings"
)

// TemplateReferencesVars reports whether a notification template references any
// `vars.*` variable inside a `{{...}}` expression. Plain message text outside
// `{{...}}` and string literals inside expressions are ignored. An unparseable
// template is conservatively reported as a reference; callers that need a
// diagnostic use CheckTemplateReferencesVars.
func TemplateReferencesVars(source string) bool {
	found, err := CheckTemplateReferencesVars(source)
	return err != nil || found
}

// CheckTemplateReferencesVars is TemplateReferencesVars with fail-closed parse
// diagnostics for migration preflight callers.
func CheckTemplateReferencesVars(source string) (bool, error) {
	if !strings.Contains(source, "{{") {
		return false, nil
	}
	remaining := source
	for len(remaining) > 0 {
		start := strings.Index(remaining, "{{")
		if start < 0 {
			break
		}
		afterOpen := remaining[start+2:]
		end := strings.Index(afterOpen, "}}")
		if end < 0 {
			return false, fmt.Errorf("parse notification template: unclosed expression")
		}
		inner := strings.TrimSpace(afterOpen[:end])
		expr, err := parseExpression(inner)
		if err != nil {
			return false, fmt.Errorf("parse notification template expression: %w", err)
		}
		if exprReferencesVars(expr) {
			return true, nil
		}
		remaining = afterOpen[end+2:]
	}
	return false, nil
}

func exprReferencesVars(expr templateExpression) bool {
	switch node := expr.(type) {
	case variableExpression:
		return IsVarsPath(node.name) || node.name == "vars" || strings.HasPrefix(node.name, "vars.")
	case callExpression:
		for _, arg := range node.args {
			if exprReferencesVars(arg) {
				return true
			}
		}
	}
	return false
}
