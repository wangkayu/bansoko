package namingvars

import (
	"fmt"
	"reflect"
	"sort"

	"github.com/dop251/goja"
	"github.com/dop251/goja/ast"
	"github.com/dop251/goja/parser"
)

// protectInterpolations checks values before template-literal coercion can
// erase null/undefined. Parse the bounded source, never regex-match JS syntax.
// Only ast-package structs are walked; file/source metadata is not traversed.
func protectInterpolations(source string, runtime *goja.Runtime) (string, error) {
	program, err := parser.ParseFile(nil, "naming-vars", source, 0)
	if err != nil {
		return "", err
	}
	const helper = "__varsPresent"
	type insertion struct {
		offset int
		text   string
	}
	edits := []insertion{}
	seen := map[uintptr]bool{}
	var walk func(reflect.Value) error
	walk = func(v reflect.Value) error {
		if !v.IsValid() {
			return nil
		}
		switch v.Kind() {
		case reflect.Interface:
			if !v.IsNil() {
				return walk(v.Elem())
			}
		case reflect.Pointer:
			if v.IsNil() || seen[v.Pointer()] {
				return nil
			}
			seen[v.Pointer()] = true
			if v.CanInterface() {
				if literal, ok := v.Interface().(*ast.TemplateLiteral); ok && literal.Tag == nil {
					for _, expr := range literal.Expressions {
						start, end := int(expr.Idx0())-1, int(expr.Idx1())-1
						if start < 0 || end > len(source) || start >= end {
							return fmt.Errorf("invalid Vars interpolation span")
						}
						edits = append(edits, insertion{start, helper + "(("}, insertion{end, "))"})
					}
				}
			}
			return walk(v.Elem())
		case reflect.Struct:
			if v.Type().PkgPath() != "github.com/dop251/goja/ast" {
				return nil
			}
			for i := 0; i < v.NumField(); i++ {
				if err := walk(v.Field(i)); err != nil {
					return err
				}
			}
		case reflect.Slice:
			for i := 0; i < v.Len(); i++ {
				if err := walk(v.Index(i)); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk(reflect.ValueOf(program)); err != nil {
		return "", err
	}
	if err := runtime.Set(helper, func(value goja.Value) goja.Value {
		if value == nil || goja.IsNull(value) || goja.IsUndefined(value) {
			panic(runtime.NewTypeError("naming vars interpolation is missing; choose an explicit fallback"))
		}
		return value
	}); err != nil {
		return "", err
	}
	if _, err := runtime.RunString(`Object.defineProperty(globalThis, "__varsPresent", {writable:false, configurable:false});`); err != nil {
		return "", err
	}
	sort.SliceStable(edits, func(i, j int) bool { return edits[i].offset > edits[j].offset })
	for _, edit := range edits {
		source = source[:edit.offset] + edit.text + source[edit.offset:]
	}
	return source, nil
}
