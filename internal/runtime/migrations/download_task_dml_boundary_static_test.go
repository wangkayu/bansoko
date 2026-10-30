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

// Only registered code may write download_tasks directly or issue provider
// lookups. MatchAdmission is the accepted read-only pre-admission path; submit
// preparation and submit recovery are separate capabilities, and sync and
// product management reads are not submit recovery.
func TestDownloadTaskDMLAndProviderQueryBoundaries(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	allowed := map[string]bool{
		"download/application/admission.go:MatchAdmission":                     true,
		"download/application/typed_submit.go:typedSubmit":                     true,
		"download/application/typed_submit_recovery.go:typedSubmitRecovery":    true,
		"download/application/typed_sync.go:typedSync":                         true,
		"download/application/service.go:syncProvider":                         true,
		"configuration/application/qb_management.go:GetTorrentFiles":           true,
		"configuration/application/qb_management.go:ListTorrents":              true,
		"configuration/application/qb_management.go:DeleteTorrent":             true, // read-only ownership check before a manager delete
		"configuration/application/qb_management.go:ensureTorrentExists":       true,
		"transport/httpapi/server_qb_management.go:ListQBittorrentTorrents":    true,
		"transport/httpapi/server_qb_management.go:GetQBittorrentTorrentFiles": true,
		// The data-deleting completion action lists the other qB tasks to prove
		// none shares its content. A read, never submit recovery.
		"downloadcompletion/service.go:Others": true,
		// The periodic seed-limit reconciliation lists the live tasks to decide
		// whether a tag frozen on a finished row is still referenced. A read,
		// never submit recovery.
		"downloadcompletion/service.go:List": true,
	}
	dml := regexp.MustCompile(`(?is)\b(?:insert\s+into|update|delete\s+from|truncate(?:\s+table)?)\s+(?:public\s*\.\s*)?download_tasks\b`)
	residual := regexp.MustCompile(`(?is)^\s*(?:--[^\n]*\n\s*)*DELETE\s+FROM\s+download_tasks\s+WHERE\s+id\s*=\s*(?:\$1|sqlc\.arg\(download_task_id\))\s*$`)
	query := regexp.MustCompile(`(?i)^(Lookup.*(OperationKey|InfoHash).*|GetTorrentFiles|ListTorrents|PrepareSubmission|lookupProvider|syncProvider)$`)
	checkSQL := func(rel, s string) {
		if !dml.MatchString(s) {
			return
		}
		if (rel == "workitems/adapter/persistence/query.sql" || rel == "workitems/adapter/persistence/generated/query.sql.go") && residual.MatchString(s) {
			return
		}
		t.Errorf("unregistered task DML: %s", rel)
	}
	err := filepath.WalkDir(filepath.Join(root, "internal"), func(path string, e os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e.IsDir() || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(filepath.Join(root, "internal"), path)
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(rel, "testutil/") {
			return nil
		} // fixture arranger, not production
		if strings.HasSuffix(path, ".sql") {
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for _, stmt := range strings.Split(string(raw), "-- name:") {
				if !dml.MatchString(stmt) {
					continue
				}
				pos := strings.Index(strings.ToUpper(stmt), "DELETE FROM DOWNLOAD_TASKS")
				if pos >= 0 {
					checkSQL(rel, strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(stmt[pos:]), ";")))
				} else {
					checkSQL(rel, stmt)
				}
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		// Includes package-level generated SQL constants, not just function bodies.
		ast.Inspect(f, func(n ast.Node) bool {
			if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
				s, _ := strconv.Unquote(lit.Value)
				checkSQL(rel, s)
			}
			return true
		})
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			key := rel + ":" + fn.Name.Name
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if call, ok := n.(*ast.CallExpr); ok {
					name := ""
					switch c := call.Fun.(type) {
					case *ast.SelectorExpr:
						name = c.Sel.Name
					case *ast.Ident:
						name = c.Name
					}
					if query.MatchString(name) && !strings.HasPrefix(rel, "download/qbittorrent/") && !allowed[key] {
						t.Errorf("unregistered provider query: %s -> %s", key, name)
					}
				}
				return true
			})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "internal/download/application/typed_submit_recovery.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "download_start_submit_lookup") || !strings.Contains(string(raw), "if !gate.Granted") {
		t.Fatal("submit recovery lookup has no durable ticket gate")
	}
}
