package port

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func repoPath(rel string) string { return filepath.Join("..", "..", "..", rel) }

func sortedStrings(values []string) []string {
	out := append([]string(nil), values...)
	sort.Strings(out)
	return out
}

func registeredCodes() []string {
	codes := []string{}
	for _, spec := range MessageSpecs() {
		codes = append(codes, string(spec.Code))
	}
	return codes
}

func registeredKinds() []string {
	kinds := []string{}
	for _, kind := range ActivityKinds() {
		kinds = append(kinds, string(kind))
	}
	return kinds
}

func TestMessageCatalogIsWellFormed(t *testing.T) {
	codeShape := regexp.MustCompile(`^[a-z][a-z0-9_]{0,127}$`)
	seen := map[MessageCode]bool{}
	paramTypes := map[string]MessageParamType{}
	for _, spec := range MessageSpecs() {
		if !codeShape.MatchString(string(spec.Code)) {
			t.Errorf("code %q does not fit the column CHECK", spec.Code)
		}
		if seen[spec.Code] {
			t.Errorf("code %q is registered twice", spec.Code)
		}
		seen[spec.Code] = true
		if !IsActivityKind(string(spec.Kind)) {
			t.Errorf("code %q has unknown kind %q", spec.Code, spec.Kind)
		}
		names := map[string]bool{}
		for _, param := range spec.Params {
			if names[param.Name] {
				t.Errorf("code %q declares param %q twice", spec.Code, param.Name)
			}
			names[param.Name] = true
			if param.Type != MessageParamString && param.Type != MessageParamInteger {
				t.Errorf("code %q param %q has unknown type %q", spec.Code, param.Name, param.Type)
			}
			if previous, ok := paramTypes[param.Name]; ok && previous != param.Type {
				t.Errorf("param %q is %q in one message and %q in another", param.Name, previous, param.Type)
			}
			paramTypes[param.Name] = param.Type
		}
	}
	if len(ActivityKinds()) != 18 {
		t.Errorf("expected 18 journal kinds, got %d: %v", len(ActivityKinds()), ActivityKinds())
	}
	if IsActivityKind("rss_refresh_failed") {
		t.Error("rss_refresh_failed is retired")
	}
}

// TestEveryMessageCodeConstantIsRegistered: each MessageCode constant of the
// catalog has a spec, and the catalog has no spec without a constant.
func TestEveryMessageCodeConstantIsRegistered(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "activity_messages.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	constants := []string{}
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			value := spec.(*ast.ValueSpec)
			if ident, ok := value.Type.(*ast.Ident); !ok || ident.Name != "MessageCode" {
				continue
			}
			for _, literal := range value.Values {
				code, err := strconv.Unquote(literal.(*ast.BasicLit).Value)
				if err != nil {
					t.Fatal(err)
				}
				constants = append(constants, code)
				if _, ok := LookupMessage(MessageCode(code)); !ok {
					t.Errorf("message code constant %q is not registered", code)
				}
			}
		}
	}
	if strings.Join(sortedStrings(constants), ",") != strings.Join(sortedStrings(registeredCodes()), ",") {
		t.Errorf("constants %v and registry %v differ", sortedStrings(constants), sortedStrings(registeredCodes()))
	}
}

// TestWritersUseCatalogConstants: outside this package no code builds a
// message code from a string (a literal in a MessageCode field or a
// MessageCode conversion), so every code a writer can pass is one of the
// registered constants above.
func TestWritersUseCatalogConstants(t *testing.T) {
	root := repoPath("internal")
	self, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if abs, _ := filepath.Abs(path); abs == self || entry.Name() == "generated" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !strings.Contains(string(raw), "MessageCode") {
			return nil
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, raw, 0)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(node ast.Node) bool {
			switch typed := node.(type) {
			case *ast.KeyValueExpr:
				if key, ok := typed.Key.(*ast.Ident); ok && key.Name == "MessageCode" {
					if _, literal := typed.Value.(*ast.BasicLit); literal {
						t.Errorf("%s: message code written as a string literal", fset.Position(typed.Pos()))
					}
				}
			case *ast.CallExpr:
				if selector, ok := typed.Fun.(*ast.SelectorExpr); ok && selector.Sel.Name == "MessageCode" {
					t.Errorf("%s: message code converted from a string", fset.Position(typed.Pos()))
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func openAPISchemas(t *testing.T) map[string]map[string]any {
	t.Helper()
	raw, err := os.ReadFile(repoPath("api/openapi.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Components struct {
			Schemas map[string]map[string]any `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return doc.Components.Schemas
}

func schemaEnum(t *testing.T, schemas map[string]map[string]any, name string) []string {
	t.Helper()
	schema, ok := schemas[name]
	if !ok {
		t.Fatalf("schema %s missing", name)
	}
	values := []string{}
	list, _ := schema["enum"].([]any)
	for _, value := range list {
		text, _ := value.(string)
		values = append(values, text)
	}
	return values
}

// TestMessageCatalogMirrors: the OpenAPI enums and params schema and the
// migration's kind CHECK mirror the catalog.
func TestMessageCatalogMirrors(t *testing.T) {
	schemas := openAPISchemas(t)
	if got, want := schemaEnum(t, schemas, "SubscriptionLogMessageCode"), registeredCodes(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("SubscriptionLogMessageCode enum %v differs from the catalog %v", got, want)
	}
	if got, want := sortedStrings(schemaEnum(t, schemas, "SubscriptionLogKind")), registeredKinds(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("SubscriptionLogKind enum %v differs from the catalog kinds %v", got, want)
	}

	params := schemas["SubscriptionLogMessageParams"]
	if params["additionalProperties"] != false {
		t.Error("SubscriptionLogMessageParams must reject additional properties")
	}
	properties, _ := params["properties"].(map[string]any)
	declared := map[string]MessageParamType{}
	for _, spec := range MessageSpecs() {
		for _, param := range spec.Params {
			declared[param.Name] = param.Type
		}
	}
	if len(properties) != len(declared) {
		t.Errorf("params schema has %d properties, catalog declares %d", len(properties), len(declared))
	}
	for name, kind := range declared {
		property, ok := properties[name].(map[string]any)
		if !ok {
			t.Errorf("params schema lacks %q", name)
			continue
		}
		if property["type"] != string(kind) {
			t.Errorf("params schema %q is %v, catalog says %q", name, property["type"], kind)
		}
		if kind == MessageParamString && property["maxLength"] != MaxMessageParamBytes {
			t.Errorf("params schema %q maxLength %v, writer bound %d", name, property["maxLength"], MaxMessageParamBytes)
		}
		if kind == MessageParamInteger && property["minimum"] != 0 {
			t.Errorf("params schema %q must have minimum 0, got %v", name, property["minimum"])
		}
	}

	migration, err := os.ReadFile(repoPath("db/migrations/092_activity_message_codes.sql"))
	if err != nil {
		t.Fatal(err)
	}
	check := regexp.MustCompile(`subscription_log_entries_kind_check CHECK \(kind IN \(([^)]*)\)\)`).FindSubmatch(migration)
	if check == nil {
		t.Fatal("092 kind CHECK not found")
	}
	values := []string{}
	for _, value := range strings.Split(string(check[1]), ",") {
		values = append(values, strings.Trim(strings.TrimSpace(value), "'"))
	}
	if got, want := sortedStrings(values), registeredKinds(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("092 kind CHECK %v differs from the catalog kinds %v", got, want)
	}
}

func TestReleaseVerdictMessagesBelongToReleaseProcessed(t *testing.T) {
	for _, verdict := range []string{"discovered", "waiting_metadata", "parse_failed", "accepted", "bogus"} {
		spec, ok := LookupMessage(ReleaseVerdictMessage(verdict))
		if !ok || spec.Kind != ActivityKindReleaseProcessed {
			t.Errorf("verdict %q maps to %q (kind %q)", verdict, ReleaseVerdictMessage(verdict), spec.Kind)
		}
	}
}
