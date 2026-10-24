package application

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"runtime"
	"testing"
)

func TestCompositionRootHasNoStartupOrElapsedCatalogRefresh(t *testing.T) {
	_, filename, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(filename), "..", "..", ".."))
	path := filepath.Join(root, "cmd", "bansoko-server", "main.go")
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatal(err)
	}

	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if function.Name.Name == "runCatalogRefreshScheduler" {
			t.Fatal("catalog scheduler function must not exist")
		}
		ast.Inspect(function.Body, func(node ast.Node) bool {
			selector, ok := node.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			switch selector.Sel.Name {
			case "ListScheduledProviderRefreshTargets", "RefreshProviderSeason":
				t.Fatalf("composition function %s can trigger provider catalog I/O through %s", function.Name.Name, selector.Sel.Name)
			}
			return true
		})
	}
}
