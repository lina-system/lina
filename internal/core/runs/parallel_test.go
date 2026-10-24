// SPDX-License-Identifier: Apache-2.0
package runs_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestTopLevelTestsCallParallel keeps the package safe to run with the race
// detector. A top-level test calls t.Parallel first, or stays serial and says
// why in a comment that contains "serial".
func TestTopLevelTestsCallParallel(t *testing.T) {
	t.Parallel()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("test file path unavailable")
	}
	dir := filepath.Dir(file)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	seen := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, "_test.go") {
			continue
		}
		path := filepath.Join(dir, name)
		parsed, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range parsed.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || !isTopLevelTest(fn) {
				continue
			}
			seen++
			if callsParallelFirst(fn) || explainsSerial(fset, parsed, fn) {
				continue
			}
			pos := fset.Position(fn.Pos())
			t.Errorf("%s:%d: %s must call t.Parallel first or comment why it stays serial", pos.Filename, pos.Line, fn.Name.Name)
		}
	}
	if seen == 0 {
		t.Fatal("no top-level tests found")
	}
}

func isTopLevelTest(fn *ast.FuncDecl) bool {
	name := fn.Name.Name
	if !strings.HasPrefix(name, "Test") || len(name) == len("Test") || name[len("Test")] < 'A' || name[len("Test")] > 'Z' {
		return false
	}
	if fn.Type.Params == nil || len(fn.Type.Params.List) != 1 || len(fn.Type.Params.List[0].Names) != 1 {
		return false
	}
	star, ok := fn.Type.Params.List[0].Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	sel, ok := star.X.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "testing" && sel.Sel.Name == "T"
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
	if !ok || len(call.Args) != 0 {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Parallel" {
		return false
	}
	recv, ok := sel.X.(*ast.Ident)
	return ok && recv.Name == fn.Type.Params.List[0].Names[0].Name
}

func explainsSerial(fset *token.FileSet, file *ast.File, fn *ast.FuncDecl) bool {
	if fn.Doc != nil && strings.Contains(strings.ToLower(fn.Doc.Text()), "serial") {
		return true
	}
	if fn.Body == nil {
		return false
	}
	open := fset.Position(fn.Body.Lbrace)
	var first token.Position
	if len(fn.Body.List) > 0 {
		first = fset.Position(fn.Body.List[0].Pos())
	}
	for _, group := range file.Comments {
		for _, comment := range group.List {
			at := fset.Position(comment.Pos())
			inside := at.Filename == open.Filename && at.Line == open.Line+1
			if !inside && first.Line > 0 {
				inside = at.Filename == first.Filename && at.Line < first.Line && at.Line > open.Line
			}
			if inside && strings.Contains(strings.ToLower(comment.Text), "serial") {
				return true
			}
		}
	}
	return false
}
