// SPDX-License-Identifier: Apache-2.0
package records

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRecordsTopLevelTestsAreParallel locks the package contract: every
// top-level test calls t.Parallel first, unless its doc comment says why it
// stays serial. The shared tables actor and todoDateFields stay read-only so
// parallel tests can share them. Package recordsMu may be contended; tests
// must not depend on which test acquires it first.
func TestRecordsTopLevelTestsAreParallel(t *testing.T) {
	t.Parallel()
	fset := token.NewFileSet()
	matches, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var missing []string
	var writes []string
	for _, name := range matches {
		if !strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		file, err := parser.ParseFile(fset, name, src, parser.ParseComments)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || !strings.HasPrefix(fn.Name.Name, "Test") || !takesTestingT(fn) {
				continue
			}
			if callsParallelFirst(fn) || serialReason(fn) {
				continue
			}
			missing = append(missing, name+":"+fn.Name.Name)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.AssignStmt:
				for _, lhs := range node.Lhs {
					if sharedTableWrite(lhs) {
						writes = append(writes, fset.Position(node.Pos()).String())
					}
				}
			case *ast.IncDecStmt:
				if sharedTableWrite(node.X) {
					writes = append(writes, fset.Position(node.Pos()).String())
				}
			case *ast.UnaryExpr:
				if node.Op == token.AND && sharedTableRoot(node.X) {
					writes = append(writes, fset.Position(node.Pos()).String())
				}
			}
			return true
		})
	}
	if len(missing) > 0 {
		t.Fatalf("top-level tests missing t.Parallel or a serial reason: %s", strings.Join(missing, ", "))
	}
	if len(writes) > 0 {
		t.Fatalf("actor and todoDateFields are read-only: %s", strings.Join(writes, ", "))
	}
}

func takesTestingT(fn *ast.FuncDecl) bool {
	if fn.Type.Params == nil || len(fn.Type.Params.List) != 1 {
		return false
	}
	star, ok := fn.Type.Params.List[0].Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	sel, ok := star.X.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == "T"
}

func callsParallelFirst(fn *ast.FuncDecl) bool {
	if fn.Body == nil || len(fn.Body.List) == 0 {
		return false
	}
	expr, ok := fn.Body.List[0].(*ast.ExprStmt)
	if !ok {
		return false
	}
	call, ok := expr.X.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Parallel" || len(call.Args) != 0 {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && id.Name == "t"
}

func serialReason(fn *ast.FuncDecl) bool {
	if fn.Doc == nil {
		return false
	}
	return strings.Contains(fn.Doc.Text(), "stays serial:")
}

func sharedTableWrite(expr ast.Expr) bool {
	switch node := expr.(type) {
	case *ast.Ident:
		return node.Name == "actor" || node.Name == "todoDateFields"
	case *ast.SelectorExpr:
		return sharedTableRoot(node.X)
	case *ast.IndexExpr:
		return sharedTableRoot(node.X)
	default:
		return false
	}
}

func sharedTableRoot(expr ast.Expr) bool {
	id, ok := expr.(*ast.Ident)
	return ok && (id.Name == "actor" || id.Name == "todoDateFields")
}
