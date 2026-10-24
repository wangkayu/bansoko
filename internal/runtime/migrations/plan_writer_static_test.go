package migrations

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// Inspect parsed SQL literals, including generated adapters. Runtime Plan
// writers and locking readers must use the fixed owner-held boundaries.
func TestS3PlanRuntimeWriterClosure(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	dml := regexp.MustCompile(`(?is)\b(insert\s+into|update|delete\s+from|truncate(?:\s+table)?)\s+(?:public\.)?postprocessing_plans\b`)
	err := filepath.WalkDir(filepath.Join(root, "internal"), func(path string, e os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e.IsDir() {
			if strings.Contains(filepath.ToSlash(path), "/runtime/migrations") || strings.Contains(filepath.ToSlash(path), "/testutil") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		for _, name := range []string{"VerifiedArtifactSink", "OneShotVerifiedArtifactSink", "SetVerifiedArtifactSink"} {
			if strings.Contains(string(body), name) {
				t.Errorf("retired planning sink %s remains at %s", name, path)
			}
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			sql, err := strconv.Unquote(lit.Value)
			if err != nil {
				return true
			}
			if dml.MatchString(sql) || (strings.Contains(strings.ToLower(sql), "postprocessing_plans") && regexp.MustCompile(`(?i)\bfor\s+(update|share|no\s+key\s+update|key\s+share)\b`).MatchString(sql)) {
				t.Errorf("Plan runtime bare writer/lock at %s: %s", path, sql)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "internal/workitems/adapter/persistence/query.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if dml.Match(raw) {
		t.Fatal("Plan writer in sqlc source")
	}
}
